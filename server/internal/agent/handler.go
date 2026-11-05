package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/runner"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/httpx"
)

// 错误映射用的 sentinel:与 runner/session 的导出错误同源,errors.Is 既命中
// 本包别名也命中原始包装链。
var (
	ErrInvalid       = runner.ErrInvalid
	ErrNotFound      = runner.ErrNotFound
	ErrBusy          = runner.ErrBusy
	ErrNotRunning    = runner.ErrNotRunning
	ErrInvalidAnswer = runner.ErrInvalidAnswer
	ErrCapacity      = runner.ErrCapacity
	ErrUnavailable   = runner.ErrUnavailable
)

// Runner 是 handler 依赖的 runner 能力子集;生产实现是 *runner.Runner,
// 缺配置时的降级实现见 app 包。
type Runner interface {
	Send(ctx context.Context, in runner.SendInput) (runner.SendResult, error)
	Retry(ctx context.Context, in runner.RetryInput) (runner.SendResult, error)
	Compact(ctx context.Context, sessionID, model string) (runner.CompactResult, error)
	Stop(ctx context.Context, sessionID string) error
	Grant(ctx context.Context, sessionID string, in runner.GrantInput) error
	AnswerQuestion(ctx context.Context, in runner.AnswerQuestionInput) error
	Health() runner.Health
}

// 能力接口的请求体上限:SKILL.md 整体可达 128KB(JSON 转义后膨胀),
// 放 1MB;其余小负载 256KB。
const (
	skillBodyLimit  = 1 << 20
	configBodyLimit = 256 << 10
)

// agentsMDPathName 是 workspace/file 唯一支持的路径:内置 Agent 只托管
// Pi 的全局 AGENTS.md。
const agentsMDPathName = "AGENTS.md"

// Deps 注入 handler 的全部依赖;ValidateAttachments 复用 agentproxy 的
// 导出包装,MaxBodyBytes 来自 cfg.MaxRequestBodyBytes。
type Deps struct {
	Runner              Runner
	Store               *session.Store
	Runs                *runlog.Registry
	Capabilities        *capabilities.Store
	ModelMemory         http.HandlerFunc
	ValidateAttachments func(any) error
	Logger              *slog.Logger
	MaxBodyBytes        int64
	// Status 返回启动时决定的隔离状态(只读,不改动 runner.Health 的
	// 语义);nil 或 Mode 为空时 /health 不输出隔离相关字段。
	Status func() Status
}

// Status 是 app 层注入的隔离状态,随 /health 一并输出(08 §八 P5a)。
type Status struct {
	// Mode 是隔离模式:"on" | "degraded" | "off" | "failed";空表示
	// 未评估(Pi 入口未就绪时不做探测)。
	Mode string
	// Reason 是 degraded/failed 时的中文说明(on/off 为简短说明或为空)。
	Reason string
	// WriteToolsEnabled 表示当前模式是否开放写类工具(write/edit);
	// degraded 时关写(Q9)。
	WriteToolsEnabled bool
	Sandbox           string
	SandboxReason     string
	BashEnabled       bool
}

// Handler 实现 /api/v1/agent 下内置引擎(pi)的会话与事件接口;settings、
// backgrounds 仍由 agentproxy 提供不在本 handler 内。
type Handler struct {
	runner              Runner
	store               *session.Store
	runs                *runlog.Registry
	capabilities        *capabilities.Store
	modelMemory         http.HandlerFunc
	validateAttachments func(any) error
	logger              *slog.Logger
	maxBody             int64
	status              func() Status
}

func NewHandler(d Deps) *Handler {
	h := &Handler{
		runner:              d.Runner,
		store:               d.Store,
		runs:                d.Runs,
		capabilities:        d.Capabilities,
		modelMemory:         d.ModelMemory,
		validateAttachments: d.ValidateAttachments,
		logger:              d.Logger,
		maxBody:             d.MaxBodyBytes,
		status:              d.Status,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	return h
}

// sessionItem 对应前端 AgentSession;updated_at 为毫秒时间戳数字。
type sessionItem struct {
	SessionID    string   `json:"session_id"`
	Title        string   `json:"title"`
	Preview      string   `json:"preview"`
	UpdatedAt    int64    `json:"updated_at"`
	Running      bool     `json:"running"`
	SessionAllow []string `json:"session_allow"` // 恒为数组,空为 []
}

// sessionCommandBody 同时覆盖 POST /sessions 与 /sessions/{id}/retry 的请求体;
// attachments 保留原始 JSON,先过附件校验(需要 any 形态)再解码为强类型。
type sessionCommandBody struct {
	Content         string          `json:"content"`
	Model           string          `json:"model"`
	SessionID       string          `json:"session_id"`
	MessageID       string          `json:"message_id"`
	ReasoningEffort string          `json:"reasoning_effort"`
	PermissionMode  string          `json:"permission_mode"`
	Attachments     json.RawMessage `json:"attachments"`
	// References 是引用的其他会话 id;由 runner 校验(最多 3 个、去重、
	// 不能含当前会话、必须存在)。
	References []string `json:"references"`
}

type attachmentPayload struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	DataURL  string `json:"data_url"`
}

// ListSessions GET /sessions → {"data":[{session_id,title,preview,updated_at,running}]}
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	running := func(string) bool { return false }
	if h.runs != nil {
		running = h.runs.Running
	}
	items, err := h.store.ListItems(running)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	data := make([]sessionItem, 0, len(items))
	for _, item := range items {
		data = append(data, sessionItem{
			SessionID:    item.SessionID,
			Title:        item.Title,
			Preview:      item.Preview,
			UpdatedAt:    item.UpdatedAt,
			Running:      item.Running,
			SessionAllow: item.SessionAllow,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": data})
}

// CreateSession POST /sessions:content 为 /compact 时走手动压缩(同步返回
// 摘要与 token 变化),否则发送消息并立即返回 run 标识(事件走 SSE)。
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	var body sessionCommandBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	attachments, ok := h.parseAttachments(w, r, body.Attachments)
	if !ok {
		return
	}
	if strings.TrimSpace(body.Content) == "/compact" {
		result, err := h.runner.Compact(r.Context(), strings.TrimSpace(body.SessionID), strings.TrimSpace(body.Model))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"summary":       result.Summary,
			"tokens_before": result.TokensBefore,
			"tokens_after":  result.TokensAfter,
			"compaction_id": result.CompactionID,
		}})
		return
	}
	result, err := h.runner.Send(r.Context(), runner.SendInput{
		SessionID:       strings.TrimSpace(body.SessionID),
		Content:         body.Content,
		Model:           strings.TrimSpace(body.Model),
		ReasoningEffort: strings.TrimSpace(body.ReasoningEffort),
		PermissionMode:  body.PermissionMode,
		Attachments:     attachments,
		References:      body.References,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeSendResult(w, http.StatusOK, result)
}

// RetrySession POST /sessions/{id}/retry:从 message_id 起重发。
func (h *Handler) RetrySession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	var body sessionCommandBody
	if !h.decodeBody(w, r, &body) {
		return
	}
	attachments, ok := h.parseAttachments(w, r, body.Attachments)
	if !ok {
		return
	}
	result, err := h.runner.Retry(r.Context(), runner.RetryInput{
		SessionID:       id,
		MessageID:       strings.TrimSpace(body.MessageID),
		Content:         body.Content,
		Model:           strings.TrimSpace(body.Model),
		ReasoningEffort: strings.TrimSpace(body.ReasoningEffort),
		PermissionMode:  body.PermissionMode,
		Attachments:     attachments,
		References:      body.References,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeSendResult(w, http.StatusOK, result)
}

// ForkSession POST /sessions/{id}/fork:把会话(整体,或到 body.message_id
// 对应的那一轮结束为止)复制成一个新会话。源会话存在进行中的 run 时 409
// (分叉需要读取稳定的会话文件)。请求体可为空或 {"message_id":"…"}。
func (h *Handler) ForkSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		MessageID string `json:"message_id"`
	}
	if r.ContentLength != 0 {
		if !h.decodeBody(w, r, &body) {
			return
		}
	}
	if h.runs != nil && h.runs.Running(id) {
		h.fail(w, r, fmt.Errorf("%w: 会话 %s 有进行中的 run,不能分叉", ErrBusy, id))
		return
	}
	meta, err := h.store.Fork(id, strings.TrimSpace(body.MessageID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"data": map[string]any{
		"session_id":       meta.ID,
		"title":            meta.Title,
		"workspace_copied": false,
	}})
}

// SessionTranscript GET /sessions/{id}/transcript → {"data":{"entries":[...]}}
func (h *Handler) SessionTranscript(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	entries, err := h.store.BuildTranscript(id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"entries": entries}})
}

// DeleteSession DELETE /sessions/{id}:先停掉进行中的 run 再删除会话目录。
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	if h.runs != nil && h.runs.Running(id) {
		if err := h.runner.Stop(r.Context(), id); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	if cleaner, ok := h.runner.(interface{ ForgetSession(string) error }); ok {
		if err := cleaner.ForgetSession(id); err != nil {
			h.logger.Warn("删除会话时清理进程失败", "session_id", id, "error", err)
		}
	}
	if err := h.store.Delete(id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// StopSession POST /sessions/{id}/stop:幂等(无进行中 run 时直接成功)。
func (h *Handler) StopSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	if err := h.runner.Stop(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// GrantAccess POST /sessions/{id}/grant-access:回写一次权限审批决定。
// scope 可选:"once"(默认,仅此一次)或 "session"(会话内允许);其他值 400。
// granted=false 时 scope 无意义,按普通拒绝处理。
func (h *Handler) GrantAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		EscalationID string `json:"escalation_id"`
		Granted      *bool  `json:"granted"`
		ResolvedPath string `json:"resolved_path"`
		Scope        string `json:"scope"`
	}
	if !h.decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.EscalationID) == "" || body.Granted == nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "escalation_id 与 granted 不能为空")
		return
	}
	scope := strings.TrimSpace(body.Scope)
	if scope == "" {
		scope = "once"
	}
	if scope != "once" && scope != "session" {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "scope 只能是 once 或 session")
		return
	}
	if !*body.Granted {
		scope = "once" // 拒绝时按普通拒绝,不写入会话内允许
	}
	input := runner.GrantInput{
		EscalationID: strings.TrimSpace(body.EscalationID),
		Granted:      *body.Granted,
		ResolvedPath: body.ResolvedPath,
	}
	if scope == "session" {
		input.Scope = "session"
	}
	if err := h.runner.Grant(r.Context(), id, input); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// AnswerQuestion POST /sessions/{id}/answer-question:回答模型通过 question 工具发出的提问。
// answers 为每题的选择(选「其他」时 selected 含 "__other__" 并带 other 文本);
// skipped=true 表示跳过,忽略 answers。具体校验在 runner(400/404/409 由 fail 映射)。
func (h *Handler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		QuestionID string `json:"question_id"`
		Answers    []struct {
			Selected []string `json:"selected"`
			Other    string   `json:"other"`
		} `json:"answers"`
		Skipped bool `json:"skipped"`
	}
	if !h.decodeBody(w, r, &body) {
		return
	}
	questionID := strings.TrimSpace(body.QuestionID)
	if questionID == "" {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "question_id 不能为空")
		return
	}
	if len(body.Answers) > maxQuestionAnswers {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "回答的题数过多")
		return
	}
	answers := make([]session.QuestionAnswer, 0, len(body.Answers))
	for _, item := range body.Answers {
		if len(item.Selected) > maxQuestionOptions+1 {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "选择的选项过多")
			return
		}
		answers = append(answers, session.QuestionAnswer{Selected: item.Selected, Other: item.Other})
	}
	if err := h.runner.AnswerQuestion(r.Context(), runner.AnswerQuestionInput{
		SessionID: id, QuestionID: questionID, Answers: answers, Skipped: body.Skipped,
	}); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// 回答体的粗略上限(防滥用);题数与选项数的精确校验在 runner。
const (
	maxQuestionAnswers = 4
	maxQuestionOptions = 6
)

// RevokeAccess POST /sessions/{id}/revoke-access:从会话内允许列表移除工具;
// body {"tool":"write"},tool 为空表示清空全部。会话不存在返回 404。
func (h *Handler) RevokeAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Tool string `json:"tool"`
	}
	if !h.decodeBody(w, r, &body) {
		return
	}
	tool := strings.TrimSpace(body.Tool)
	meta, err := h.store.Update(id, func(m *session.Meta) error {
		if tool == "" {
			m.SessionAllow = []string{}
			return nil
		}
		kept := make([]string, 0, len(m.SessionAllow))
		for _, name := range m.SessionAllow {
			if name != tool {
				kept = append(kept, name)
			}
		}
		m.SessionAllow = kept
		return nil
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true, "session_allow": meta.SessionAllow})
}

// SessionEvents GET /sessions/{id}/events:run 不存在时也返回正确的 SSE 头并
// 立即结束(前端按流正常结束处理);run 存在则从开头重放并跟随实时事件,
// run 结束后由服务端收尾。
func (h *Handler) SessionEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	// SSE 可能持续整个 run 的生命周期:清除继承的写截止时间,避免被中途掐断。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	var events <-chan runlog.Event
	if h.runs != nil {
		if run := h.runs.Current(id); run != nil {
			events = run.Subscribe(r.Context())
		}
	}
	if events == nil {
		closed := make(chan runlog.Event)
		close(closed)
		events = closed
	}
	if err := runlog.WriteSSE(r.Context(), w, events, 0); err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Warn("Agent 事件流中断", "session_id", id, "error", err)
	}
}

// Health GET /health:前端 fetchAgentHealth 会解析 503 的 body,健康 200。
// 注入 Status 时附带上隔离状态与写工具开关:degraded 仍算 healthy(带说明),
// failed 时 runner 本身不可用(503)。
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	health := h.runner.Health()
	body := map[string]any{"runner": "builtin", "agent": "pi"}
	status := http.StatusOK
	if health.Available {
		body["status"] = "healthy"
	} else {
		status = http.StatusServiceUnavailable
		body["status"] = "unhealthy"
		body["reason"] = health.Reason
	}
	if health.PiVersion != "" {
		body["version"] = health.PiVersion
	}
	if h.status != nil {
		if st := h.status(); st.Mode != "" {
			body["isolation"] = st.Mode
			if st.Reason != "" {
				body["isolation_reason"] = st.Reason
			}
			body["write_tools_enabled"] = st.WriteToolsEnabled
			if st.Sandbox != "" {
				body["sandbox"] = st.Sandbox
				body["bash_enabled"] = st.BashEnabled
				if st.SandboxReason != "" {
					body["sandbox_reason"] = st.SandboxReason
				}
			}
		}
	}
	httpx.JSON(w, status, body)
}

// ModelMemory GET /model-memory:委托 agentllm 的实现。
func (h *Handler) ModelMemory(w http.ResponseWriter, r *http.Request) {
	if h.modelMemory == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "agent_unavailable", "模型记忆不可用")
		return
	}
	h.modelMemory(w, r)
}

// FeatureRemoved 内置引擎下已移除的 Runner 接口(memory/learning/version/upgrade)。
func (h *Handler) FeatureRemoved(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, "agent_feature_unavailable", "内置 Agent 不提供该功能")
}

// FeatureUnavailable 计划支持但尚未实现的接口;能力接口在未注入存储时
// 也降级到这里。
func (h *Handler) FeatureUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotImplemented, "agent_feature_unavailable", "内置 Agent 暂未提供,后续版本支持")
}

// requireCapabilities 取能力存储;未注入时按「暂未提供」回应。
func (h *Handler) requireCapabilities(w http.ResponseWriter, r *http.Request) (*capabilities.Store, bool) {
	if h.capabilities == nil {
		h.FeatureUnavailable(w, r)
		return nil, false
	}
	return h.capabilities, true
}

// capsFail 把 capabilities 的 sentinel 错误映射为 HTTP 响应;未知错误只记
// 日志,不向客户端回显内部细节。
func (h *Handler) capsFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, capabilities.ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, capabilities.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", err.Error())
	default:
		h.logger.Error("Agent 能力接口内部错误", "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}

// ListSkills GET /skills → {"data":SkillsInfo}。
func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	info, err := store.ListSkills()
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": info})
}

// GetSkill GET /skills/{name} → {"data":SkillDetail};不存在 404。
func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	detail, err := store.GetSkill(chi.URLParam(r, "name"))
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": detail})
}

// GetSkillFile GET /skills/{name}/file?path= → {"data":{content,truncated}}。
func (h *Handler) GetSkillFile(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	content, truncated, err := store.ReadSkillFile(chi.URLParam(r, "name"), r.URL.Query().Get("path"))
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"content": content, "truncated": truncated}})
}

// PutSkill PUT /skills/{name} 体 {content} → {"data":{name,changed}}。
func (h *Handler) PutSkill(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !h.decodeBodyLimit(w, r, &body, skillBodyLimit) {
		return
	}
	name := chi.URLParam(r, "name")
	changed, err := store.PutSkill(name, body.Content)
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"name": name, "changed": changed}})
}

// DeleteSkill DELETE /skills/{name} → {"data":{name,deleted}};请求体可能
// 是 {} 或空,一律忽略。
func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "name")
	deleted, err := store.DeleteSkill(name)
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"name": name, "deleted": deleted}})
}

// GetWorkspaceFile GET /workspace/file?path=AGENTS.md → {"data":{content}};
// 文件不存在返回 404(前端把 404 当「文件不存在」);只支持 AGENTS.md,
// 其他 path 同样 404。
func (h *Handler) GetWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("path") != agentsMDPathName {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "文件不存在")
		return
	}
	content, exists, err := store.GetAgentsMD()
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	if !exists {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "文件不存在")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"content": content}})
}

// PutWorkspaceFile PUT /workspace/file 体 {path,content} → {"success":true};
// 仅支持 AGENTS.md,其他 path 400。
func (h *Handler) PutWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !h.decodeBodyLimit(w, r, &body, configBodyLimit) {
		return
	}
	if body.Path != agentsMDPathName {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "内置 Agent 仅支持 AGENTS.md")
		return
	}
	if err := store.PutAgentsMD(body.Content); err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// DeleteWorkspaceFile DELETE /workspace/file?path=AGENTS.md → {"success":true};
// 仅支持 AGENTS.md,其他 path 400。
func (h *Handler) DeleteWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("path") != agentsMDPathName {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "内置 Agent 仅支持 AGENTS.md")
		return
	}
	if err := store.DeleteAgentsMD(); err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// GetConfig GET /config → {"data":{endpoints:[],agent:{...}}};context_window
// 未设置时省略(0 表示回退 200k 兜底)。
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	config, err := store.GetConfig()
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"endpoints": []any{},
		"agent":     config,
	}})
}

// PutConfig PUT /config 体 {endpoints,agent,server?}:只取 agent 段,把已知
// 字段合并到当前配置上(缺省字段保持原值,类型不对 400),endpoints/server
// 忽略;context_window 为 0/缺失表示清除回退。
func (h *Handler) PutConfig(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCapabilities(w, r)
	if !ok {
		return
	}
	var body struct {
		Agent json.RawMessage `json:"agent"`
	}
	if !h.decodeBodyLimit(w, r, &body, configBodyLimit) {
		return
	}
	fields := map[string]json.RawMessage{}
	if len(body.Agent) > 0 {
		if err := json.Unmarshal(body.Agent, &fields); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "agent 必须是对象")
			return
		}
	}
	config, err := store.GetConfig()
	if err != nil {
		h.capsFail(w, r, err)
		return
	}
	if err := mergeAgentConfig(fields, &config); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if _, err := store.PutConfig(config); err != nil {
		h.capsFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"success": true})
}

// mergeAgentConfig 把请求 agent 段的已知字段合并到 config;未知字段忽略,
// 缺省字段保持原值,类型不符返回带字段名的错误。
func mergeAgentConfig(fields map[string]json.RawMessage, config *capabilities.Config) error {
	boolFields := map[string]*bool{
		"enable_agents_md": &config.EnableAgentsMD,
		"enable_skills":    &config.EnableSkills,
	}
	for name, target := range boolFields {
		if value, ok := fields[name]; ok {
			if err := json.Unmarshal(value, target); err != nil {
				return fmt.Errorf("agent.%s 必须是布尔值", name)
			}
		}
	}
	if value, ok := fields["disabled_skills"]; ok {
		if err := json.Unmarshal(value, &config.DisabledSkills); err != nil {
			return errors.New("agent.disabled_skills 必须是字符串数组")
		}
	}
	if value, ok := fields["compact_trigger_ratio"]; ok {
		if err := json.Unmarshal(value, &config.CompactTriggerRatio); err != nil {
			return errors.New("agent.compact_trigger_ratio 必须是数字")
		}
	}
	if value, ok := fields["context_window"]; ok {
		if err := json.Unmarshal(value, &config.ContextWindow); err != nil {
			return errors.New("agent.context_window 必须是整数")
		}
	}
	return nil
}

func (h *Handler) writeSendResult(w http.ResponseWriter, status int, result runner.SendResult) {
	httpx.JSON(w, status, map[string]any{"data": map[string]any{
		"session_id": result.SessionID,
		"message_id": result.MessageID,
		"run_id":     result.RunID,
	}})
}

// sessionID 提取并校验路径 {id};非 UUID 直接按 404 收口,不进入存储层。
func (h *Handler) sessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, err := uuid.Parse(id); err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "会话不存在")
		return "", false
	}
	return id, true
}

// decodeBody 套上 MaxBytesReader 后解析 JSON 请求体,失败时已写好错误响应。
func (h *Handler) decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	return h.decodeBodyLimit(w, r, target, h.maxBody)
}

// decodeBodyLimit 以指定上限解析 JSON 请求体,失败时已写好错误响应。
func (h *Handler) decodeBodyLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if limit > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	if err := httpx.DecodeJSONBody(r, target); err != nil {
		if errors.Is(err, httpx.ErrRequestBodyTooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_body_too_large", "请求体超过大小限制")
		} else {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是单个合法 JSON 对象")
		}
		return false
	}
	return true
}

// parseAttachments 先用注入的校验函数(与 Runner 转发路径同一套限额)检查
// 原始 JSON,再解码为强类型附件列表。
func (h *Handler) parseAttachments(w http.ResponseWriter, r *http.Request, raw json.RawMessage) ([]runner.Attachment, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, true
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "attachments 必须是数组")
		return nil, false
	}
	if h.validateAttachments != nil {
		if err := h.validateAttachments(value); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return nil, false
		}
	}
	var payloads []attachmentPayload
	if err := json.Unmarshal(raw, &payloads); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "attachments 必须是数组")
		return nil, false
	}
	attachments := make([]runner.Attachment, 0, len(payloads))
	for _, payload := range payloads {
		attachments = append(attachments, runner.Attachment{
			Name: payload.Name, MimeType: payload.MimeType, DataURL: payload.DataURL,
		})
	}
	return attachments, true
}

// fail 把 runner/session 的 sentinel 错误映射为 HTTP 响应;未知错误只记日志,
// 不向客户端回显内部细节。
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrNotFound), errors.Is(err, session.ErrNotFound), errors.Is(err, session.ErrInvalidID):
		httpx.Error(w, r, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrInvalidAnswer):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_answer", err.Error())
	case errors.Is(err, ErrNotRunning):
		httpx.Error(w, r, http.StatusConflict, "agent_not_running", err.Error())
	case errors.Is(err, ErrBusy):
		httpx.Error(w, r, http.StatusConflict, "agent_busy", err.Error())
	case errors.Is(err, ErrCapacity):
		httpx.Error(w, r, http.StatusTooManyRequests, "agent_capacity", err.Error())
	case errors.Is(err, ErrUnavailable):
		httpx.Error(w, r, http.StatusServiceUnavailable, "agent_unavailable", err.Error())
	default:
		h.logger.Error("Agent 接口内部错误", "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务器内部错误")
	}
}
