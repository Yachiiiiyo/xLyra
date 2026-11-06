package agentllm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

func TestValidIdentifiers(t *testing.T) {
	if !validIdentifiers("agent", "session", "run", "model") {
		t.Fatal("expected identifiers to be valid")
	}
	if validIdentifiers("agent", "", "run", "model") {
		t.Fatal("expected empty identifier to be invalid")
	}
}

type fakeAgentTokenEntry struct {
	value        string
	input        store.AgentRunInput
	expiresAt    time.Time
	supersededAt *time.Time
	revokedAt    *time.Time
}

type fakeAgentRepository struct {
	run            store.AgentRun
	runErr         error
	renewableToken store.AgentLLMToken
	renewableErr   error

	supersededInput *store.AgentRunInput
	createdInput    *store.AgentRunInput
	createdExpiry   time.Time
	registerInput   *store.AgentRunInput
	endedInput      *store.AgentRunInput
	supersedeCalls  int

	// tokens 让 FindUsableToken/End/SupersedeTokens 可以在内存里对账。
	tokens         []fakeAgentTokenEntry
	internalAPIKey store.APIKey
	internalKeyErr error
}

func (f *fakeAgentRepository) FindRun(context.Context, store.AgentRunInput) (store.AgentRun, error) {
	return f.run, f.runErr
}

func (f *fakeAgentRepository) Register(_ context.Context, input store.AgentRunInput, _ time.Time) (store.AgentRun, error) {
	f.registerInput = &input
	if f.runErr != nil {
		return store.AgentRun{}, f.runErr
	}
	if f.run.SessionID == "" && f.run.RunID == "" {
		f.run = store.AgentRun{AgentInstanceID: input.AgentInstanceID, SessionID: input.SessionID, RunID: input.RunID, Model: input.Model, Status: store.AgentRunActive}
		return f.run, nil
	}
	if f.run.AgentInstanceID != input.AgentInstanceID || f.run.Model != input.Model || f.run.Status == store.AgentRunEnded {
		return store.AgentRun{}, errors.New("agent run identity conflict")
	}
	return f.run, nil
}

func (f *fakeAgentRepository) End(_ context.Context, input store.AgentRunInput, now time.Time) error {
	f.endedInput = &input
	if f.run.SessionID == "" && f.run.RunID == "" {
		return nil
	}
	if f.run.AgentInstanceID != input.AgentInstanceID || f.run.Model != input.Model {
		return errors.New("agent run identity conflict")
	}
	f.run.Status = store.AgentRunEnded
	for i := range f.tokens {
		if f.tokens[i].input.SessionID == input.SessionID && f.tokens[i].input.RunID == input.RunID && f.tokens[i].revokedAt == nil {
			f.tokens[i].revokedAt = &now
		}
	}
	return nil
}

func (f *fakeAgentRepository) FindUsableToken(_ context.Context, token string, now time.Time) (store.AgentLLMToken, error) {
	for _, entry := range f.tokens {
		if entry.value != token {
			continue
		}
		// 与真实仓库的差别:superseded 后不享受宽限期,立即不可用,便于断言轮换效果。
		if entry.revokedAt != nil || entry.supersededAt != nil || !entry.expiresAt.After(now) {
			break
		}
		if f.run.Status != store.AgentRunActive {
			break
		}
		return store.AgentLLMToken{
			AgentInstanceID: entry.input.AgentInstanceID,
			SessionID:       entry.input.SessionID,
			RunID:           entry.input.RunID,
			Model:           entry.input.Model,
			ExpiresAt:       entry.expiresAt,
		}, nil
	}
	return store.AgentLLMToken{}, gorm.ErrRecordNotFound
}

func (f *fakeAgentRepository) FindRenewableToken(context.Context, string, time.Time) (store.AgentLLMToken, error) {
	return f.renewableToken, f.renewableErr
}

func (f *fakeAgentRepository) CreateToken(_ context.Context, input store.AgentRunInput, token string, expiresAt time.Time) (store.AgentLLMToken, error) {
	f.createdInput = &input
	f.createdExpiry = expiresAt
	f.tokens = append(f.tokens, fakeAgentTokenEntry{value: token, input: input, expiresAt: expiresAt})
	return store.AgentLLMToken{}, nil
}

func (f *fakeAgentRepository) SupersedeTokens(_ context.Context, input store.AgentRunInput, now time.Time) error {
	f.supersededInput = &input
	f.supersedeCalls++
	for i := range f.tokens {
		if f.tokens[i].input.SessionID == input.SessionID && f.tokens[i].input.RunID == input.RunID && f.tokens[i].supersededAt == nil {
			f.tokens[i].supersededAt = &now
		}
	}
	return nil
}

func (f *fakeAgentRepository) EnsureInternalAPIKey(context.Context) (store.APIKey, error) {
	return f.internalAPIKey, f.internalKeyErr
}

func (f *fakeAgentRepository) SyncInternalAPIKeyPolicy(context.Context, string, []uuid.UUID, string, []uuid.UUID) (store.APIKey, error) {
	return store.APIKey{}, errors.New("not implemented")
}

func (f *fakeAgentRepository) ModelMemory(context.Context) (string, map[string]string, error) {
	return "", map[string]string{}, nil
}

func newTestHandler(repo agentRepository) Handler {
	return Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), repo: repo, now: time.Now, imageTracker: newAgentImageTracker(), fastTaskTracker: newAgentImageTracker()}
}

func credentialBody(reason string) *strings.Reader {
	return strings.NewReader(`{"agent_instance_id":"agent-1","session_id":"sess-1","run_id":"run-1","model":"gpt-5","reason":"` + reason + `"}`)
}

func decodeCredentialResponse(t *testing.T, rec *httptest.ResponseRecorder) credentialResponse {
	t.Helper()
	var payload credentialResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode credential response: %v", err)
	}
	return payload
}

func TestCredentialRejectsUnknownReason(t *testing.T) {
	repo := &fakeAgentRepository{}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential", credentialBody("unauthorized"))
	rec := httptest.NewRecorder()
	handler.Credential(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if repo.createdInput != nil {
		t.Fatal("no token should be created for an unknown reason")
	}
}

func TestCredentialUnknownRunIsInactive(t *testing.T) {
	repo := &fakeAgentRepository{runErr: gorm.ErrRecordNotFound}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential", credentialBody("initial"))
	rec := httptest.NewRecorder()
	handler.Credential(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if payload := decodeCredentialResponse(t, rec); payload.Active {
		t.Fatal("unknown run must not receive a credential: issuance is anchored on runner registration")
	}
	if repo.createdInput != nil {
		t.Fatal("no token should be created for an unknown run")
	}
}

func TestCredentialActiveRunIssuesToken(t *testing.T) {
	repo := &fakeAgentRepository{run: store.AgentRun{AgentInstanceID: "agent-1", SessionID: "sess-1", RunID: "run-1", Model: "gpt-5", Status: store.AgentRunActive}}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential", credentialBody("initial"))
	rec := httptest.NewRecorder()
	handler.Credential(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	payload := decodeCredentialResponse(t, rec)
	if !payload.Active || payload.Token == "" || payload.ExpiresAt == "" {
		t.Fatalf("expected active credential, got %+v", payload)
	}
	if repo.createdInput == nil || repo.createdInput.SessionID != "sess-1" || repo.createdInput.RunID != "run-1" {
		t.Fatalf("token created with unexpected identity: %+v", repo.createdInput)
	}
	if repo.supersededInput == nil {
		t.Fatal("issuance must supersede previous tokens")
	}
}

func TestCredentialPendingRunIsInactive(t *testing.T) {
	repo := &fakeAgentRepository{run: store.AgentRun{AgentInstanceID: "agent-1", SessionID: "sess-1", RunID: "run-1", Model: "gpt-5", Status: store.AgentRunPending}}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential", credentialBody("initial"))
	rec := httptest.NewRecorder()
	handler.Credential(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if payload := decodeCredentialResponse(t, rec); payload.Active {
		t.Fatal("pending run must not receive a credential: only runner-registered (active) runs qualify")
	}
	if repo.createdInput != nil {
		t.Fatal("no token should be created for a pending run")
	}
}

func TestCredentialEndedRunIsInactive(t *testing.T) {
	repo := &fakeAgentRepository{run: store.AgentRun{AgentInstanceID: "agent-1", SessionID: "sess-1", RunID: "run-1", Model: "gpt-5", Status: store.AgentRunEnded}}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential", credentialBody("initial"))
	rec := httptest.NewRecorder()
	handler.Credential(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if payload := decodeCredentialResponse(t, rec); payload.Active {
		t.Fatal("ended run must not receive a credential")
	}
	if repo.createdInput != nil {
		t.Fatal("no token should be created for an ended run")
	}
}

func TestRenewRequiresBearerToken(t *testing.T) {
	handler := newTestHandler(&fakeAgentRepository{})
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential/renew", nil)
	rec := httptest.NewRecorder()
	handler.Renew(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRenewRejectsUnrenewableToken(t *testing.T) {
	repo := &fakeAgentRepository{renewableErr: gorm.ErrRecordNotFound}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential/renew", nil)
	req.Header.Set("Authorization", "Bearer stale-token")
	rec := httptest.NewRecorder()
	handler.Renew(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if repo.createdInput != nil {
		t.Fatal("no token should be created for a non-renewable credential")
	}
}

func TestRenewRotatesUsingTokenIdentity(t *testing.T) {
	repo := &fakeAgentRepository{renewableToken: store.AgentLLMToken{
		AgentInstanceID: "agent-9",
		SessionID:       "sess-9",
		RunID:           "run-9",
		Model:           "claude-opus",
		ExpiresAt:       time.Now().Add(10 * time.Minute),
	}}
	handler := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/internal/agent-llm/credential/renew", nil)
	req.Header.Set("Authorization", "Bearer live-token")
	rec := httptest.NewRecorder()
	handler.Renew(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	payload := decodeCredentialResponse(t, rec)
	if !payload.Active || payload.Token == "" {
		t.Fatalf("expected rotated credential, got %+v", payload)
	}
	if repo.supersededInput == nil || repo.supersededInput.SessionID != "sess-9" || repo.supersededInput.RunID != "run-9" || repo.supersededInput.Model != "claude-opus" {
		t.Fatalf("supersede must use the token identity, got %+v", repo.supersededInput)
	}
	if repo.createdInput == nil || repo.createdInput.AgentInstanceID != "agent-9" {
		t.Fatalf("token created with unexpected identity: %+v", repo.createdInput)
	}
	if !repo.createdExpiry.After(time.Now().Add(DefaultTokenTTL - time.Minute)) {
		t.Fatal("new token must carry a fresh TTL")
	}
}

func TestParseUUIDsSkipsInvalidAndDuplicates(t *testing.T) {
	id := uuid.New()
	ids := parseUUIDs([]string{id.String(), "not-a-uuid", " ", id.String(), uuid.Nil.String()})
	if len(ids) != 1 || ids[0] != id {
		t.Fatalf("parseUUIDs = %v, want [%v]", ids, id)
	}
}

func TestIssueRunTokenRegistersAndIssues(t *testing.T) {
	repo := &fakeAgentRepository{}
	handler := newTestHandler(repo)
	token, expiresAt, err := handler.IssueRunToken(context.Background(), "sess-1", "run-1", "gpt-5")
	if err != nil {
		t.Fatalf("IssueRunToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if !expiresAt.After(time.Now().Add(DefaultTokenTTL - time.Minute)) {
		t.Fatal("token must carry a fresh TTL")
	}
	if repo.registerInput == nil || *repo.registerInput != (store.AgentRunInput{AgentInstanceID: BuiltinInstanceID, SessionID: "sess-1", RunID: "run-1", Model: "gpt-5"}) {
		t.Fatalf("run registered with unexpected identity: %+v", repo.registerInput)
	}
	if _, err := repo.FindUsableToken(context.Background(), token, time.Now()); err != nil {
		t.Fatalf("issued token must be usable: %v", err)
	}
}

func TestIssueRunTokenRotatesPreviousToken(t *testing.T) {
	repo := &fakeAgentRepository{}
	handler := newTestHandler(repo)
	first, _, err := handler.IssueRunToken(context.Background(), "sess-1", "run-1", "gpt-5")
	if err != nil {
		t.Fatalf("first IssueRunToken: %v", err)
	}
	second, _, err := handler.IssueRunToken(context.Background(), "sess-1", "run-1", "gpt-5")
	if err != nil {
		t.Fatalf("second IssueRunToken: %v", err)
	}
	if second == first {
		t.Fatal("reissuance must rotate the token value")
	}
	if repo.supersedeCalls < 2 {
		t.Fatalf("supersedeCalls = %d, want at least 2", repo.supersedeCalls)
	}
	if _, err := repo.FindUsableToken(context.Background(), first, time.Now()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("previous token must be superseded by reissuance")
	}
	if _, err := repo.FindUsableToken(context.Background(), second, time.Now()); err != nil {
		t.Fatalf("rotated token must be usable: %v", err)
	}
}

func TestIssueRunTokenRequiresIdentifiers(t *testing.T) {
	repo := &fakeAgentRepository{}
	handler := newTestHandler(repo)
	for _, input := range [][3]string{{"", "run-1", "gpt-5"}, {"sess-1", "", "gpt-5"}, {"sess-1", "run-1", ""}} {
		if _, _, err := handler.IssueRunToken(context.Background(), input[0], input[1], input[2]); err == nil {
			t.Fatalf("IssueRunToken(%v) must fail on empty identifiers", input)
		}
	}
	if repo.registerInput != nil || repo.createdInput != nil {
		t.Fatal("nothing should be registered or issued for empty identifiers")
	}
}

func TestEndRunRevokesTokens(t *testing.T) {
	repo := &fakeAgentRepository{}
	handler := newTestHandler(repo)
	token, _, err := handler.IssueRunToken(context.Background(), "sess-1", "run-1", "gpt-5")
	if err != nil {
		t.Fatalf("IssueRunToken: %v", err)
	}
	if err := handler.EndRun(context.Background(), "sess-1", "run-1", "gpt-5"); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if repo.endedInput == nil || repo.endedInput.AgentInstanceID != BuiltinInstanceID {
		t.Fatalf("run ended with unexpected identity: %+v", repo.endedInput)
	}
	if _, err := repo.FindUsableToken(context.Background(), token, time.Now()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("token must be unusable after EndRun")
	}
}

func TestEndRunRequiresIdentifiers(t *testing.T) {
	handler := newTestHandler(&fakeAgentRepository{})
	for _, input := range [][3]string{{"", "run-1", "gpt-5"}, {"sess-1", "", "gpt-5"}, {"sess-1", "run-1", ""}} {
		if err := handler.EndRun(context.Background(), input[0], input[1], input[2]); err == nil {
			t.Fatalf("EndRun(%v) must fail on empty identifiers", input)
		}
	}
}
