package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/runner"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/httpx"
)

// fakeRunner 记录调用并返回预设结果,模拟 runner.Runner 的接口子集。
type fakeRunner struct {
	health runner.Health

	sendErr    error
	sendInput  *runner.SendInput
	sendResult runner.SendResult

	retryErr    error
	retryInput  *runner.RetryInput
	retryResult runner.SendResult

	compactErr    error
	compactCalls  []compactCall
	compactResult runner.CompactResult

	stopErr    error
	stoppedIDs []string

	grantErr  error
	grantInfo []grantCall
}

type compactCall struct {
	sessionID string
	model     string
}

type grantCall struct {
	sessionID string
	input     runner.GrantInput
}

func (f *fakeRunner) Send(_ context.Context, in runner.SendInput) (runner.SendResult, error) {
	cp := in
	f.sendInput = &cp
	return f.sendResult, f.sendErr
}

func (f *fakeRunner) Retry(_ context.Context, in runner.RetryInput) (runner.SendResult, error) {
	cp := in
	f.retryInput = &cp
	return f.retryResult, f.retryErr
}

func (f *fakeRunner) Compact(_ context.Context, sessionID, model string) (runner.CompactResult, error) {
	f.compactCalls = append(f.compactCalls, compactCall{sessionID: sessionID, model: model})
	return f.compactResult, f.compactErr
}

func (f *fakeRunner) Stop(_ context.Context, sessionID string) error {
	f.stoppedIDs = append(f.stoppedIDs, sessionID)
	return f.stopErr
}

func (f *fakeRunner) Grant(_ context.Context, sessionID string, in runner.GrantInput) error {
	f.grantInfo = append(f.grantInfo, grantCall{sessionID: sessionID, input: in})
	return f.grantErr
}

func (f *fakeRunner) Health() runner.Health {
	return f.health
}

func newTestHandler(t *testing.T, fake *fakeRunner, options ...func(*Deps)) (*Handler, *session.Store, *runlog.Registry) {
	t.Helper()
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	runs := runlog.NewRegistry()
	deps := Deps{
		Runner: fake,
		Store:  store,
		Runs:   runs,
		ModelMemory: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"default_model": "m-default", "sessions": map[string]string{}}})
		}),
		ValidateAttachments: agentproxy.ValidateAttachments,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxBodyBytes:        1 << 20,
	}
	for _, option := range options {
		option(&deps)
	}
	return NewHandler(deps), store, runs
}

func agentRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1/agent", func(g chi.Router) {
		g.Get("/health", h.Health)
		g.Get("/model-memory", h.ModelMemory)
		g.Get("/version", h.FeatureRemoved)
		g.Post("/upgrade", h.FeatureRemoved)
		g.Get("/memory", h.FeatureRemoved)
		g.Put("/memory/{target}", h.FeatureRemoved)
		g.Get("/learning/status", h.FeatureRemoved)
		g.Get("/skills", h.ListSkills)
		g.Get("/skills/{name}", h.GetSkill)
		g.Get("/skills/{name}/file", h.GetSkillFile)
		g.Put("/skills/{name}", h.PutSkill)
		g.Delete("/skills/{name}", h.DeleteSkill)
		g.Get("/workspace/file", h.GetWorkspaceFile)
		g.Put("/workspace/file", h.PutWorkspaceFile)
		g.Delete("/workspace/file", h.DeleteWorkspaceFile)
		g.Get("/config", h.GetConfig)
		g.Put("/config", h.PutConfig)
		g.Get("/sessions", h.ListSessions)
		g.Post("/sessions", h.CreateSession)
		g.Get("/sessions/{id}/transcript", h.SessionTranscript)
		g.Delete("/sessions/{id}", h.DeleteSession)
		g.Post("/sessions/{id}/fork", h.ForkSession)
		g.Get("/sessions/{id}/files", h.SessionFile)
		g.Head("/sessions/{id}/files", h.SessionFile)
		g.Get("/sessions/{id}/changes", h.SessionChanges)
		g.Get("/sessions/{id}/changes/{turnID}/diff", h.SessionChangeDiff)
		g.Get("/sessions/{id}/artifacts", h.SessionArtifacts)
		g.Get("/sessions/{id}/artifacts.zip", h.SessionArtifactsZip)
		g.Post("/sessions/{id}/retry", h.RetrySession)
		g.Post("/sessions/{id}/stop", h.StopSession)
		g.Post("/sessions/{id}/grant-access", h.GrantAccess)
		g.Post("/sessions/{id}/revoke-access", h.RevokeAccess)
		g.Get("/sessions/{id}/events", h.SessionEvents)
	})
	return r
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeBody(t, rec, &body)
	return body.Error.Code, body.Error.Message
}

func TestListSessionsResponseShape(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, runs := newTestHandler(t, fake)

	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.Title = "标题 A"
		m.Messages = append(m.Messages, session.UserMessage{MessageID: "msg-1", Text: "内容"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	idle, err := store.Create("m2", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Begin(meta.ID, "run-1"); err != nil {
		t.Fatal(err)
	}

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
			Preview   string `json:"preview"`
			UpdatedAt int64  `json:"updated_at"`
			Running   bool   `json:"running"`
		} `json:"data"`
	}
	decodeBody(t, rec, &body)
	if len(body.Data) != 2 {
		t.Fatalf("sessions = %d, want 2 (%s)", len(body.Data), rec.Body.String())
	}
	// 列表按 UpdatedAt 倒序，同毫秒创建时顺序不定，按 id 对号入座。
	byID := map[string]int{}
	titles := map[string]string{}
	runningFlags := map[string]bool{}
	for i, item := range body.Data {
		byID[item.SessionID] = i
		titles[item.SessionID] = item.Title
		runningFlags[item.SessionID] = item.Running
		if item.UpdatedAt <= 0 {
			t.Fatalf("updated_at = %d, want unix millis", item.UpdatedAt)
		}
	}
	if _, ok := byID[meta.ID]; !ok || titles[meta.ID] != "标题 A" || !runningFlags[meta.ID] {
		t.Fatalf("running session meta = %s, got %v/%v/%v", meta.ID, titles[meta.ID], runningFlags[meta.ID], byID)
	}
	if _, ok := byID[idle.ID]; !ok || runningFlags[idle.ID] || titles[idle.ID] == "" {
		t.Fatalf("idle session idle = %s, got %v/%v", idle.ID, titles[idle.ID], runningFlags[idle.ID])
	}
}

// doJSONAgent 把请求送进与生产一致的 chi 路由结构。
func doJSONAgent(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	agentRouter(h).ServeHTTP(rec, req)
	return rec
}

func TestCreateSessionSend(t *testing.T) {
	fake := &fakeRunner{
		sendResult: runner.SendResult{SessionID: "s-1", MessageID: "m-1", RunID: "r-1"},
	}
	handler, _, _ := newTestHandler(t, fake)

	body := `{"content":"你好","model":"gpt-test","session_id":"11111111-1111-1111-1111-111111111111","reasoning_effort":"high","permission_mode":"ask","attachments":[{"name":"a.png","mime_type":"image/png","data_url":"data:image/png;base64,aGk="}]}`
	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			SessionID string `json:"session_id"`
			MessageID string `json:"message_id"`
			RunID     string `json:"run_id"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data != (struct {
		SessionID string `json:"session_id"`
		MessageID string `json:"message_id"`
		RunID     string `json:"run_id"`
	}{SessionID: "s-1", MessageID: "m-1", RunID: "r-1"}) {
		t.Fatalf("data = %+v", payload.Data)
	}
	in := fake.sendInput
	if in == nil {
		t.Fatal("runner.Send was not called")
	}
	if in.Content != "你好" || in.Model != "gpt-test" || in.SessionID != "11111111-1111-1111-1111-111111111111" ||
		in.ReasoningEffort != "high" || in.PermissionMode != "ask" {
		t.Fatalf("send input = %+v", in)
	}
	if len(in.Attachments) != 1 || in.Attachments[0] != (runner.Attachment{Name: "a.png", MimeType: "image/png", DataURL: "data:image/png;base64,aGk="}) {
		t.Fatalf("attachments = %+v", in.Attachments)
	}
	if len(fake.compactCalls) != 0 {
		t.Fatalf("compact should not run, got %+v", fake.compactCalls)
	}
}

func TestCreateSessionCompact(t *testing.T) {
	fake := &fakeRunner{
		compactResult: runner.CompactResult{Summary: "摘要", TokensBefore: 100, TokensAfter: 40, CompactionID: "c-1"},
	}
	handler, _, _ := newTestHandler(t, fake)

	body := `{"content":"  /compact  ","session_id":"22222222-2222-2222-2222-222222222222","model":"m2"}`
	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Summary      string `json:"summary"`
			TokensBefore int    `json:"tokens_before"`
			TokensAfter  int    `json:"tokens_after"`
			CompactionID string `json:"compaction_id"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.Summary != "摘要" || payload.Data.TokensBefore != 100 || payload.Data.TokensAfter != 40 || payload.Data.CompactionID != "c-1" {
		t.Fatalf("data = %+v", payload.Data)
	}
	if len(fake.compactCalls) != 1 || fake.compactCalls[0] != (compactCall{sessionID: "22222222-2222-2222-2222-222222222222", model: "m2"}) {
		t.Fatalf("compact calls = %+v", fake.compactCalls)
	}
	if fake.sendInput != nil {
		t.Fatal("runner.Send should not run for /compact")
	}
}

func TestCreateSessionRejectsInvalidAttachments(t *testing.T) {
	fake := &fakeRunner{sendResult: runner.SendResult{SessionID: "s", RunID: "r"}}
	handler, _, _ := newTestHandler(t, fake)

	for name, body := range map[string]string{
		"missing data_url": `{"content":"hi","model":"m","attachments":[{"name":"a.png","mime_type":"image/png"}]}`,
		"not an array":     `{"content":"hi","model":"m","attachments":{"name":"a"}}`,
		"too many":         `{"content":"hi","model":"m","attachments":[` + strings.Repeat(`{"name":"a","mime_type":"text/plain","data_url":"data:text/plain;base64,aGk="},`, 5) + `]}`,
	} {
		rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", strings.TrimSuffix(body, ","))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body %s", name, rec.Code, rec.Body.String())
		}
		if code, _ := errorBody(t, rec); code != "invalid_request" {
			t.Fatalf("%s: code = %s", name, code)
		}
	}
	if fake.sendInput != nil {
		t.Fatal("runner.Send must not run when attachments are invalid")
	}
}

func TestCreateSessionRejectsBadBody(t *testing.T) {
	fake := &fakeRunner{}
	handler, _, _ := newTestHandler(t, fake)

	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", "not-json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "invalid_request" {
		t.Fatalf("garbage body code = %s", code)
	}

	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", `{} {}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("double value status = %d", rec.Code)
	}

	small, _, _ := newTestHandler(t, fake, func(d *Deps) { d.MaxBodyBytes = 32 })
	rec = doJSONAgent(t, small, http.MethodPost, "/api/v1/agent/sessions", `{"content":"0123456789012345678901234567890123456789","model":"m"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "request_body_too_large" {
		t.Fatalf("oversized body code = %s", code)
	}
}

func TestRetrySession(t *testing.T) {
	fake := &fakeRunner{
		retryResult: runner.SendResult{SessionID: "s-2", MessageID: "m-2", RunID: "r-2"},
	}
	handler, _, _ := newTestHandler(t, fake)

	path := "/api/v1/agent/sessions/33333333-3333-3333-3333-333333333333/retry"
	rec := doJSONAgent(t, handler, http.MethodPost, path, `{"message_id":"orig-1","content":"重写","model":"m3","reasoning_effort":"low","permission_mode":"full"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			SessionID string `json:"session_id"`
			MessageID string `json:"message_id"`
			RunID     string `json:"run_id"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.SessionID != "s-2" || payload.Data.MessageID != "m-2" || payload.Data.RunID != "r-2" {
		t.Fatalf("data = %+v", payload.Data)
	}
	in := fake.retryInput
	if in == nil {
		t.Fatal("runner.Retry was not called")
	}
	if in.SessionID != "33333333-3333-3333-3333-333333333333" || in.MessageID != "orig-1" || in.Content != "重写" ||
		in.Model != "m3" || in.ReasoningEffort != "low" || in.PermissionMode != "full" {
		t.Fatalf("retry input = %+v", in)
	}

	// 非 UUID 的会话 id 直接 404,不进入 runner。
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/not-a-uuid/retry", `{"message_id":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid id status = %d", rec.Code)
	}
	if fake.retryInput != nil && fake.retryInput.SessionID == "not-a-uuid" {
		t.Fatal("retry must not run for invalid session id")
	}
}

func TestSessionTranscript(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, _ := newTestHandler(t, fake)

	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/transcript", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			Entries []map[string]any `json:"entries"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.Entries == nil {
		t.Fatal("entries must serialize as [], not null")
	}

	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+uuid.NewString()+"/transcript", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "not_found" {
		t.Fatalf("missing session code = %s", code)
	}
}

func TestDeleteSessionStopsRunningRun(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, runs := newTestHandler(t, fake)

	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Begin(meta.ID, "run-1"); err != nil {
		t.Fatal(err)
	}
	rec := doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/sessions/"+meta.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Success bool `json:"success"`
	}
	decodeBody(t, rec, &payload)
	if !payload.Success {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if len(fake.stoppedIDs) != 1 || fake.stoppedIDs[0] != meta.ID {
		t.Fatalf("stopped = %+v", fake.stoppedIDs)
	}
	if _, err := os.Stat(store.Dir(meta.ID)); !os.IsNotExist(err) {
		t.Fatalf("session dir should be removed, stat err = %v", err)
	}

	// 无进行中 run 的删除不触发 Stop。
	idle, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec = doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/sessions/"+idle.ID, "")
	if rec.Code != http.StatusOK || len(fake.stoppedIDs) != 1 {
		t.Fatalf("idle delete status = %d, stops = %+v", rec.Code, fake.stoppedIDs)
	}
}

func TestStopSession(t *testing.T) {
	fake := &fakeRunner{}
	handler, _, _ := newTestHandler(t, fake)

	path := "/api/v1/agent/sessions/44444444-4444-4444-4444-444444444444/stop"
	rec := doJSONAgent(t, handler, http.MethodPost, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Success bool `json:"success"`
	}
	decodeBody(t, rec, &payload)
	if !payload.Success || len(fake.stoppedIDs) != 1 {
		t.Fatalf("body = %s, stops = %+v", rec.Body.String(), fake.stoppedIDs)
	}

	fake.stopErr = fmt.Errorf("%w: boom", runner.ErrUnavailable)
	rec = doJSONAgent(t, handler, http.MethodPost, path, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stop failure status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "agent_unavailable" {
		t.Fatalf("stop failure code = %s", code)
	}
}

func TestGrantAccess(t *testing.T) {
	fake := &fakeRunner{}
	handler, _, _ := newTestHandler(t, fake)

	path := "/api/v1/agent/sessions/55555555-5555-5555-5555-555555555555/grant-access"
	rec := doJSONAgent(t, handler, http.MethodPost, path, `{"escalation_id":"esc-1","granted":true,"resolved_path":"/tmp/ok"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Success bool `json:"success"`
	}
	decodeBody(t, rec, &payload)
	if !payload.Success {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if len(fake.grantInfo) != 1 || fake.grantInfo[0].sessionID != "55555555-5555-5555-5555-555555555555" ||
		fake.grantInfo[0].input != (runner.GrantInput{EscalationID: "esc-1", Granted: true, ResolvedPath: "/tmp/ok"}) {
		t.Fatalf("grant calls = %+v", fake.grantInfo)
	}

	rec = doJSONAgent(t, handler, http.MethodPost, path, `{"granted":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing escalation status = %d", rec.Code)
	}
	rec = doJSONAgent(t, handler, http.MethodPost, path, `{"escalation_id":"esc-1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing granted status = %d", rec.Code)
	}

	fake.grantErr = fmt.Errorf("%w: 审批 esc-x", runner.ErrNotFound)
	rec = doJSONAgent(t, handler, http.MethodPost, path, `{"escalation_id":"esc-x","granted":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown escalation status = %d", rec.Code)
	}
}

func TestSessionEventsWithoutRunEndsImmediately(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{})

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+uuid.NewString()+"/events", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body should be empty, got %q", rec.Body.String())
	}

	// 非 UUID 的会话 id 不进入事件流,直接 404。
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/xyz/events", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid id status = %d", rec.Code)
	}
}

func TestSessionEventsStreamsAndEndsWithRun(t *testing.T) {
	handler, store, runs := newTestHandler(t, &fakeRunner{})

	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Begin(meta.ID, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	run.Publish(translate.Event{Type: "agent_start", Data: map[string]any{}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/events", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		agentRouter(handler).ServeHTTP(rec, req)
	}()

	// 缓冲重放 + 实时事件 + Finish 收尾,最终应读到完整流且连接随之结束。
	run.Publish(translate.Event{Type: "message", Data: map[string]any{"text": "hi"}})
	run.Finish()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		run.Finish()
		t.Fatal("event stream did not end after run finished")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: rollout\n") {
		t.Fatalf("missing rollout event lines: %q", body)
	}
	for _, want := range []string{`"type":"agent_start"`, `"payload":{}`, `"ordinal":1`, `"type":"message"`, `"text":"hi"`, `"ordinal":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream %q missing %s", body, want)
		}
	}
}

func TestSessionEventsClientDisconnectDoesNotLeak(t *testing.T) {
	handler, store, runs := newTestHandler(t, &fakeRunner{})

	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	run, err := runs.Begin(meta.ID, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(agentRouter(handler))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/agent/sessions/"+meta.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 先发布事件再发起请求：SSE 响应头随首次写入下发，否则 Do 会一直等
	// 到 keepalive 才返回。
	run.Publish(translate.Event{Type: "agent_start", Data: map[string]any{}})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(line) != "event: rollout" {
		t.Fatalf("first sse line = %q, err = %v", line, err)
	}
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "data:") {
		t.Fatalf("second sse line = %q, err = %v", line, err)
	}

	base := runtime.NumGoroutine()
	cancel()
	run.Finish()
	// 客户端断开后读到连接错误是预期行为，只需排空。
	_, _ = io.Copy(io.Discard, resp.Body)
	server.Close()

	// 订阅与 handler goroutine 应随客户端断开退出。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if runtime.NumGoroutine() <= base+1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not settle: base %d, now %d", base, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHealthStates(t *testing.T) {
	fake := &fakeRunner{health: runner.Health{Available: true, PiVersion: "1.2.3"}}
	handler, _, _ := newTestHandler(t, fake)

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	if body["status"] != "healthy" || body["runner"] != "builtin" || body["agent"] != "pi" || body["version"] != "1.2.3" {
		t.Fatalf("healthy body = %v", body)
	}
	if _, has := body["reason"]; has {
		t.Fatalf("healthy body should omit reason: %v", body)
	}

	fake.health = runner.Health{Reason: "未配置 Pi 命令"}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/health", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy status = %d", rec.Code)
	}
	decodeBody(t, rec, &body)
	if body["status"] != "unhealthy" || body["reason"] != "未配置 Pi 命令" {
		t.Fatalf("unhealthy body = %v", body)
	}
}

func TestModelMemoryDelegates(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{})

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/model-memory", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Data struct {
			DefaultModel string            `json:"default_model"`
			Sessions     map[string]string `json:"sessions"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.DefaultModel != "m-default" || payload.Data.Sessions == nil {
		t.Fatalf("body = %s", rec.Body.String())
	}

	bare, _, _ := newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.ModelMemory = nil })
	rec = doJSONAgent(t, bare, http.MethodGet, "/api/v1/agent/model-memory", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing delegate status = %d", rec.Code)
	}
}

func TestUnsupportedEndpoints(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{})

	removed := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agent/version"},
		{http.MethodPost, "/api/v1/agent/upgrade"},
		{http.MethodGet, "/api/v1/agent/memory"},
		{http.MethodPut, "/api/v1/agent/memory/memory"},
		{http.MethodGet, "/api/v1/agent/learning/status"},
	}
	for _, tc := range removed {
		body := ""
		if tc.method == http.MethodPost || tc.method == http.MethodPut {
			body = "{}"
		}
		rec := doJSONAgent(t, handler, tc.method, tc.path, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status = %d, want 404", tc.method, tc.path, rec.Code)
		}
		if code, message := errorBody(t, rec); code != "agent_feature_unavailable" || message == "" {
			t.Fatalf("%s %s: code = %s, message = %q", tc.method, tc.path, code, message)
		}
	}

	unavailable := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agent/skills"},
		{http.MethodGet, "/api/v1/agent/skills/demo"},
		{http.MethodGet, "/api/v1/agent/skills/demo/file?path=SKILL.md"},
		{http.MethodPut, "/api/v1/agent/skills/demo"},
		{http.MethodDelete, "/api/v1/agent/skills/demo"},
		{http.MethodGet, "/api/v1/agent/workspace/file?path=AGENTS.md"},
		{http.MethodPut, "/api/v1/agent/workspace/file"},
		{http.MethodDelete, "/api/v1/agent/workspace/file?path=AGENTS.md"},
		{http.MethodGet, "/api/v1/agent/config"},
		{http.MethodPut, "/api/v1/agent/config"},
	}
	for _, tc := range unavailable {
		body := ""
		if tc.method == http.MethodPut {
			body = "{}"
		}
		rec := doJSONAgent(t, handler, tc.method, tc.path, body)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s: status = %d, want 501", tc.method, tc.path, rec.Code)
		}
		if code, message := errorBody(t, rec); code != "agent_feature_unavailable" || !strings.Contains(message, "暂未提供") {
			t.Fatalf("%s %s: code = %s, message = %q", tc.method, tc.path, code, message)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err        error
		status     int
		code       string
		hideDetail bool
	}{
		{fmt.Errorf("%w: 内容不能为空", runner.ErrInvalid), http.StatusBadRequest, "invalid_request", false},
		{fmt.Errorf("%w: 会话 x", runner.ErrNotFound), http.StatusNotFound, "not_found", false},
		{fmt.Errorf("%w: 会话已有进行中的 run", runner.ErrBusy), http.StatusConflict, "agent_busy", false},
		{fmt.Errorf("%w: 上限 4", runner.ErrCapacity), http.StatusTooManyRequests, "agent_capacity", false},
		{fmt.Errorf("%w: 未配置 Pi 命令", runner.ErrUnavailable), http.StatusServiceUnavailable, "agent_unavailable", false},
		{errors.New("boom: secret"), http.StatusInternalServerError, "internal_error", true},
	}
	for _, tc := range cases {
		fake := &fakeRunner{sendErr: tc.err}
		handler, _, _ := newTestHandler(t, fake)
		rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", `{"content":"hi","model":"m"}`)
		if rec.Code != tc.status {
			t.Fatalf("%v: status = %d, want %d, body %s", tc.err, rec.Code, tc.status, rec.Body.String())
		}
		code, message := errorBody(t, rec)
		if code != tc.code {
			t.Fatalf("%v: code = %s, want %s", tc.err, code, tc.code)
		}
		if tc.hideDetail && strings.Contains(message, "secret") {
			t.Fatalf("internal error must not leak details, message = %q", message)
		}
		if !tc.hideDetail && message == "" {
			t.Fatalf("%v: mapped message should not be empty", tc.err)
		}
	}
}

// TestHealthReportsIsolationStatus:注入 Status 后 /health 附带隔离状态;
// degraded 仍 healthy,failed 因 runner 不可用而 503;未注入时字段缺省。
func TestHealthReportsIsolationStatus(t *testing.T) {
	withStatus := func(st Status) func(*Deps) {
		return func(d *Deps) { d.Status = func() Status { return st } }
	}

	cases := []struct {
		name          string
		health        runner.Health
		status        Status
		wantCode      int
		wantBody      string
		wantIsolation any // nil 表示字段应缺省
		wantWrite     any
	}{
		{
			name:          "on 且 runner 可用",
			health:        runner.Health{Available: true},
			status:        Status{Mode: "on", WriteToolsEnabled: true},
			wantCode:      http.StatusOK,
			wantBody:      "healthy",
			wantIsolation: "on",
			wantWrite:     true,
		},
		{
			name:          "degraded 仍 healthy 但关写",
			health:        runner.Health{Available: true},
			status:        Status{Mode: "degraded", Reason: "需要 root 运行(当前 euid=501)", WriteToolsEnabled: false},
			wantCode:      http.StatusOK,
			wantBody:      "healthy",
			wantIsolation: "degraded",
			wantWrite:     false,
		},
		{
			name:          "off 全开工具",
			health:        runner.Health{Available: true},
			status:        Status{Mode: "off", Reason: "AGENT_ISOLATION=off,仅用于本地开发", WriteToolsEnabled: true},
			wantCode:      http.StatusOK,
			wantBody:      "healthy",
			wantIsolation: "off",
			wantWrite:     true,
		},
		{
			name:          "failed 时 runner 不可用",
			health:        runner.Health{Reason: "隔离不可用"},
			status:        Status{Mode: "failed", Reason: "隔离自检失败:敏感路径不可读", WriteToolsEnabled: false},
			wantCode:      http.StatusServiceUnavailable,
			wantBody:      "unhealthy",
			wantIsolation: "failed",
			wantWrite:     false,
		},
		{
			name:          "Mode 为空时省略隔离字段",
			health:        runner.Health{Available: true},
			status:        Status{},
			wantCode:      http.StatusOK,
			wantBody:      "healthy",
			wantIsolation: nil,
			wantWrite:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRunner{health: tc.health}
			handler, _, _ := newTestHandler(t, fake, withStatus(tc.status))

			rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/health", "")
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			var body map[string]any
			decodeBody(t, rec, &body)
			if body["status"] != tc.wantBody {
				t.Fatalf("body.status = %v, want %v", body["status"], tc.wantBody)
			}
			gotIso, hasIso := body["isolation"]
			if tc.wantIsolation == nil {
				if hasIso {
					t.Fatalf("isolation 字段应缺省: %v", body)
				}
				return
			}
			if gotIso != tc.wantIsolation {
				t.Fatalf("isolation = %v, want %v", gotIso, tc.wantIsolation)
			}
			if got, want := body["write_tools_enabled"], tc.wantWrite; got != want {
				t.Fatalf("write_tools_enabled = %v, want %v", got, want)
			}
			if tc.status.Reason != "" && body["isolation_reason"] != tc.status.Reason {
				t.Fatalf("isolation_reason = %v, want %q", body["isolation_reason"], tc.status.Reason)
			}
			if tc.status.Reason == "" {
				if _, has := body["isolation_reason"]; has {
					t.Fatalf("isolation_reason 应缺省: %v", body)
				}
			}
		})
	}

	// 未注入 Status:不输出隔离字段(向后兼容)
	fake := &fakeRunner{health: runner.Health{Available: true}}
	handler, _, _ := newTestHandler(t, fake)
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	decodeBody(t, rec, &body)
	for _, key := range []string{"isolation", "isolation_reason", "write_tools_enabled"} {
		if _, has := body[key]; has {
			t.Fatalf("未注入 Status 时 %s 应缺省: %v", key, body)
		}
	}
}

// ---- P6e:grant-access 的 scope / revoke-access / 列表 session_allow ----

func TestGrantAccessScope(t *testing.T) {
	fake := &fakeRunner{}
	handler, _, _ := newTestHandler(t, fake)
	path := "/api/v1/agent/sessions/66666666-6666-6666-6666-666666666666/grant-access"

	cases := []struct {
		name string
		body string
		want runner.GrantInput
	}{
		{"缺省 scope 按 once", `{"escalation_id":"esc-1","granted":true}`, runner.GrantInput{EscalationID: "esc-1", Granted: true}},
		{"显式 once", `{"escalation_id":"esc-1","granted":true,"scope":"once"}`, runner.GrantInput{EscalationID: "esc-1", Granted: true}},
		{"session 透传", `{"escalation_id":"esc-1","granted":true,"scope":"session"}`, runner.GrantInput{EscalationID: "esc-1", Granted: true, Scope: "session"}},
		{"拒绝携带 session 按普通拒绝", `{"escalation_id":"esc-1","granted":false,"scope":"session"}`, runner.GrantInput{EscalationID: "esc-1", Granted: false}},
	}
	for _, tc := range cases {
		rec := doJSONAgent(t, handler, http.MethodPost, path, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", tc.name, rec.Code, rec.Body.String())
		}
		if len(fake.grantInfo) != 1 || fake.grantInfo[0].input != tc.want {
			t.Fatalf("%s: grant calls = %+v, want %+v", tc.name, fake.grantInfo, tc.want)
		}
		fake.grantInfo = nil
	}

	// scope 非法值 400,不进入 runner
	for _, body := range []string{
		`{"escalation_id":"esc-1","granted":true,"scope":"always"}`,
		`{"escalation_id":"esc-1","granted":true,"scope":"ONCE"}`,
	} {
		rec := doJSONAgent(t, handler, http.MethodPost, path, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("非法 scope status = %d, body %s", rec.Code, rec.Body.String())
		}
		if code, _ := errorBody(t, rec); code != "invalid_request" {
			t.Fatalf("非法 scope code = %s", code)
		}
		if len(fake.grantInfo) != 0 {
			t.Fatalf("非法 scope 不应进入 runner: %+v", fake.grantInfo)
		}
	}
}

func TestRevokeAccess(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, _ := newTestHandler(t, fake)
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.SessionAllow = []string{"write", "bash"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agent/sessions/" + meta.ID + "/revoke-access"

	// 移除单个工具
	rec := doJSONAgent(t, handler, http.MethodPost, path, `{"tool":"write"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Success      bool     `json:"success"`
		SessionAllow []string `json:"session_allow"`
	}
	decodeBody(t, rec, &payload)
	if !payload.Success || !reflect.DeepEqual(payload.SessionAllow, []string{"bash"}) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// 不存在的工具:幂等成功
	rec = doJSONAgent(t, handler, http.MethodPost, path, `{"tool":"edit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	decodeBody(t, rec, &payload)
	if !reflect.DeepEqual(payload.SessionAllow, []string{"bash"}) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// tool 为空:清空全部,session_allow 为空数组而非 null
	rec = doJSONAgent(t, handler, http.MethodPost, path, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"session_allow":[]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// 会话不存在 404
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+uuid.NewString()+"/revoke-access", `{"tool":"write"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "not_found" {
		t.Fatalf("missing session code = %s", code)
	}
}

// 会话列表每个条目带 session_allow,恒为数组。
func TestListSessionsSessionAllow(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, _ := newTestHandler(t, fake)
	empty, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(allowed.ID, func(m *session.Meta) error {
		m.SessionAllow = []string{"write"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []struct {
			SessionID    string   `json:"session_id"`
			SessionAllow []string `json:"session_allow"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	got := map[string][]string{}
	for _, item := range payload.Data {
		if item.SessionAllow == nil {
			t.Fatalf("会话 %s 的 session_allow 不应为 null", item.SessionID)
		}
		got[item.SessionID] = item.SessionAllow
	}
	if !reflect.DeepEqual(got[empty.ID], []string{}) {
		t.Fatalf("无允许列表的会话 = %v", got[empty.ID])
	}
	if !reflect.DeepEqual(got[allowed.ID], []string{"write"}) {
		t.Fatalf("有允许列表的会话 = %v", got[allowed.ID])
	}
}
