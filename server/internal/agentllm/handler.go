package agentllm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/auth"
	"xlyra/server/internal/gateway"
	"xlyra/server/internal/httpx"
	"xlyra/server/internal/store"
)

const (
	DefaultTokenTTL    = 30 * time.Minute
	TokenReasonInitial = "initial"

	// BuiltinInstanceID 是内置 Agent(Pi 子进程)在 agent_runs 里的实例标识,
	// 区别于外部 runner 经 /runs/register 登记的实例。
	BuiltinInstanceID = "xlyra-builtin"
)

type Handler struct {
	logger  *slog.Logger
	repo    agentRepository
	gateway gateway.Handler
	now     func() time.Time

	imageModelsSource func() []agentproxy.ImageModelSelection
	imageGenerate     func(context.Context, gateway.ImageGenerationRequest) gateway.ImageGenerationResult
	chatCompletions   http.HandlerFunc
	imageMaxCalls     int
	imageTotalTimeout time.Duration
	imageTracker      *agentImageTracker
}

type agentRepository interface {
	FindRun(ctx context.Context, input store.AgentRunInput) (store.AgentRun, error)
	Register(ctx context.Context, input store.AgentRunInput, now time.Time) (store.AgentRun, error)
	End(ctx context.Context, input store.AgentRunInput, now time.Time) error
	FindUsableToken(ctx context.Context, token string, now time.Time) (store.AgentLLMToken, error)
	FindRenewableToken(ctx context.Context, token string, now time.Time) (store.AgentLLMToken, error)
	CreateToken(ctx context.Context, input store.AgentRunInput, token string, expiresAt time.Time) (store.AgentLLMToken, error)
	SupersedeTokens(ctx context.Context, input store.AgentRunInput, now time.Time) error
	EnsureInternalAPIKey(ctx context.Context) (store.APIKey, error)
	SyncInternalAPIKeyPolicy(ctx context.Context, sitePolicy string, siteIDs []uuid.UUID, modelPolicy string, siteModelIDs []uuid.UUID) (store.APIKey, error)
	ModelMemory(ctx context.Context) (string, map[string]string, error)
}

type credentialRequest struct {
	AgentInstanceID string `json:"agent_instance_id"`
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	Model           string `json:"model"`
	Reason          string `json:"reason"`
}

type runRequest struct {
	AgentInstanceID string `json:"agent_instance_id"`
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	Model           string `json:"model"`
}

type credentialResponse struct {
	Active    bool   `json:"active"`
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

func NewHandler(logger *slog.Logger, db *store.Store, gatewayHandler gateway.Handler) Handler {
	return Handler{logger: logger, repo: store.NewAgentRepository(db.DB()), gateway: gatewayHandler, now: time.Now, imageTracker: newAgentImageTracker()}
}

func (h Handler) Credential(w http.ResponseWriter, r *http.Request) {
	var input credentialRequest
	if err := decode(r, &input); err != nil {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "invalid credential request")
		return
	}
	if !validIdentifiers(input.AgentInstanceID, input.SessionID, input.RunID, input.Model) {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "agent_instance_id, session_id, run_id and model are required")
		return
	}
	// Only the initial issuance is accepted here. Rotation requires the current
	// token via the renew endpoint; anything else is a protocol violation.
	if input.Reason != "" && input.Reason != TokenReasonInitial {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "unsupported credential reason")
		return
	}
	now := h.now()
	runInput := store.AgentRunInput{AgentInstanceID: input.AgentInstanceID, SessionID: input.SessionID, RunID: input.RunID, Model: input.Model}
	run, err := h.repo.FindRun(r.Context(), runInput)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// This endpoint is unauthenticated: admission is anchored on the run
		// being registered by the runner (runner-key authenticated). Unknown
		// runs are rejected; the agent retries briefly on active:false while
		// its first issuance races with registration.
		h.writeCredential(w, credentialResponse{Active: false})
		return
	}
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "agent_credential_unavailable", "unable to resolve agent run")
		return
	}
	if run.AgentInstanceID != input.AgentInstanceID || run.Model != input.Model || run.Status != store.AgentRunActive {
		h.writeCredential(w, credentialResponse{Active: false})
		return
	}
	h.issueToken(w, r, runInput, now)
}

// Renew rotates a run credential for an authenticated holder: the current live
// token is the proof of possession, so an expired or stolen credential cannot
// be exchanged for a fresh one. Callers are expected to renew proactively
// using expires_at, never in reaction to a 401 from the LLM entrypoints.
func (h Handler) Renew(w http.ResponseWriter, r *http.Request) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is required")
		return
	}
	now := h.now()
	item, err := h.repo.FindRenewableToken(r.Context(), token, now)
	if err != nil {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is not renewable")
		return
	}
	runInput := store.AgentRunInput{AgentInstanceID: item.AgentInstanceID, SessionID: item.SessionID, RunID: item.RunID, Model: item.Model}
	h.issueToken(w, r, runInput, now)
}

func (h Handler) issueToken(w http.ResponseWriter, r *http.Request, runInput store.AgentRunInput, now time.Time) {
	value, expiresAt, err := h.issueRunToken(r.Context(), runInput, now)
	if err != nil {
		message := "unable to rotate agent credential"
		switch {
		case errors.Is(err, errTokenGenerate):
			message = "unable to create agent credential"
		case errors.Is(err, errTokenPersist):
			message = "unable to persist agent credential"
		}
		h.fail(w, r, http.StatusInternalServerError, "agent_credential_unavailable", message)
		return
	}
	h.writeCredential(w, credentialResponse{Active: true, Token: value, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)})
}

var (
	errTokenGenerate = errors.New("agent credential generation failed")
	errTokenPersist  = errors.New("agent credential persistence failed")
)

// issueRunToken 是签发的核心路径,不依赖 http.ResponseWriter:supersede 旧令牌
// →生成→落库,供凭据端点与内置 Pi 的进程内签发共用。
func (h Handler) issueRunToken(ctx context.Context, runInput store.AgentRunInput, now time.Time) (string, time.Time, error) {
	// Every issuance displaces previous tokens: they die after the supersede
	// grace period, so a leaked credential cannot outlive the next rotation.
	if err := h.repo.SupersedeTokens(ctx, runInput, now); err != nil {
		return "", time.Time{}, err
	}
	value, err := newToken()
	if err != nil {
		return "", time.Time{}, errTokenGenerate
	}
	expiresAt := now.Add(DefaultTokenTTL)
	if _, err := h.repo.CreateToken(ctx, runInput, value, expiresAt); err != nil {
		return "", time.Time{}, errTokenPersist
	}
	return value, expiresAt, nil
}

func (h Handler) Register(w http.ResponseWriter, r *http.Request) {
	h.runMutation(w, r, false)
}

func (h Handler) RegisterRun(ctx context.Context, agentInstanceID, sessionID, runID, model string) error {
	_, err := h.repo.Register(ctx, store.AgentRunInput{
		AgentInstanceID: agentInstanceID,
		SessionID:       sessionID,
		RunID:           runID,
		Model:           model,
	}, h.now())
	return err
}

// IssueRunToken 登记 run(幂等)并签发/轮换绑定该 run 与 model 的短期令牌,
// 供内置 Pi 进程使用:进程内调用,不经过 /credential 的 HTTP 路径。
func (h Handler) IssueRunToken(ctx context.Context, sessionID, runID, model string) (string, time.Time, error) {
	if !validIdentifiers(BuiltinInstanceID, sessionID, runID, model) {
		return "", time.Time{}, errors.New("agentllm: session_id, run_id and model are required")
	}
	runInput := store.AgentRunInput{AgentInstanceID: BuiltinInstanceID, SessionID: sessionID, RunID: runID, Model: model}
	now := h.now()
	if _, err := h.repo.Register(ctx, runInput, now); err != nil {
		return "", time.Time{}, err
	}
	return h.issueRunToken(ctx, runInput, now)
}

// EndRun 结束 run 登记并吊销其令牌,供内置 Pi 进程退出时调用。
func (h Handler) EndRun(ctx context.Context, sessionID, runID, model string) error {
	if !validIdentifiers(BuiltinInstanceID, sessionID, runID, model) {
		return errors.New("agentllm: session_id, run_id and model are required")
	}
	h.clearImageRun(sessionID, runID)
	return h.repo.End(ctx, store.AgentRunInput{
		AgentInstanceID: BuiltinInstanceID,
		SessionID:       sessionID,
		RunID:           runID,
		Model:           model,
	}, h.now())
}

func (h Handler) End(w http.ResponseWriter, r *http.Request) {
	h.runMutation(w, r, true)
}

func (h Handler) runMutation(w http.ResponseWriter, r *http.Request, ending bool) {
	var input runRequest
	if err := decode(r, &input); err != nil || !validIdentifiers(input.AgentInstanceID, input.SessionID, input.RunID, input.Model) {
		h.fail(w, r, http.StatusBadRequest, "invalid_request", "agent_instance_id, session_id, run_id and model are required")
		return
	}
	runInput := store.AgentRunInput{AgentInstanceID: input.AgentInstanceID, SessionID: input.SessionID, RunID: input.RunID, Model: input.Model}
	var err error
	if ending {
		h.clearImageRun(input.SessionID, input.RunID)
		err = h.repo.End(r.Context(), runInput, h.now())
	} else {
		_, err = h.repo.Register(r.Context(), runInput, h.now())
	}
	if err != nil {
		h.fail(w, r, http.StatusConflict, "agent_run_conflict", "agent run identity or state conflict")
		return
	}
	h.writeCredential(w, map[string]any{"ok": true})
}

// ModelMemory serves the admin UI: last-used model overall and per session,
// so the workspace can default to what the user actually used last.
func (h Handler) ModelMemory(w http.ResponseWriter, r *http.Request) {
	defaultModel, bySession, err := h.repo.ModelMemory(r.Context())
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, "agent_model_memory_unavailable", "unable to load agent model memory")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"default_model": defaultModel, "sessions": bySession}})
}

func (h Handler) Responses(w http.ResponseWriter, r *http.Request) {
	h.llm(w, r, h.gateway.Responses)
}

func (h Handler) Messages(w http.ResponseWriter, r *http.Request) {
	h.llm(w, r, h.gateway.Messages)
}

func (h Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.llm(w, r, h.gateway.ChatCompletions)
}

func (h Handler) Models(w http.ResponseWriter, r *http.Request) {
	h.llm(w, r, h.gateway.Models)
}

func (h Handler) llm(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	token := bearer(r.Header.Get("Authorization"))
	if token == "" {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is required")
		return
	}
	item, err := h.repo.FindUsableToken(r.Context(), token, h.now())
	if err != nil {
		h.fail(w, r, http.StatusUnauthorized, "unauthorized", "temporary agent credential is invalid")
		return
	}
	if requestedModel, ok := requestModel(r); ok && requestedModel != item.Model {
		h.fail(w, r, http.StatusForbidden, "agent_model_mismatch", "request model does not match the temporary agent credential")
		return
	}
	apiKey, err := h.repo.EnsureInternalAPIKey(r.Context())
	if err != nil {
		h.fail(w, r, http.StatusServiceUnavailable, "gateway_unavailable", "internal agent gateway is not available")
		return
	}
	ctx := auth.WithAPIKey(r.Context(), apiKey)
	ctx = gateway.WithInternalRequestMetadata(ctx, map[string]any{
		"agent_instance_id": item.AgentInstanceID,
		"agent_session_id":  item.SessionID,
		"agent_run_id":      item.RunID,
		"agent_transport":   "xlyra-agent",
	})
	next(w, r.WithContext(ctx))
}

func requestModel(r *http.Request) (string, bool) {
	if r.Body == nil || r.Method != http.MethodPost {
		return "", false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.Model) == "" {
		return "", false
	}
	return strings.TrimSpace(payload.Model), true
}

// AccessPolicy mirrors the agent allowlist settings onto the internal gateway
// key. IDs arrive as strings (from the config file); invalid entries are
// skipped rather than failing the whole sync.
type AccessPolicy struct {
	SitePolicy   string
	ModelPolicy  string
	SiteIDs      []string
	SiteModelIDs []string
}

// SyncAccessPolicy applies the agent site/model allowlist to the internal API
// key and refreshes the gateway models cache for it.
func (h Handler) SyncAccessPolicy(ctx context.Context, policy AccessPolicy) error {
	key, err := h.repo.SyncInternalAPIKeyPolicy(ctx, policy.SitePolicy, parseUUIDs(policy.SiteIDs), policy.ModelPolicy, parseUUIDs(policy.SiteModelIDs))
	if err != nil {
		return err
	}
	h.gateway.InvalidateModelsCacheForAPIKey(key.ID)
	return nil
}

func parseUUIDs(values []string) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil || id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (h Handler) writeCredential(w http.ResponseWriter, payload any) {
	httpx.JSON(w, http.StatusOK, payload)
}

func (h Handler) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	httpx.Error(w, r, status, code, message)
}

func decode(r *http.Request, target any) error {
	return httpx.DecodeJSONBody(r, target)
}

func validIdentifiers(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return false
		}
	}
	return true
}

func bearer(value string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
