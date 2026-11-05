// Package translate 把 Pi 子进程(--mode rpc)在 stdout 输出的 JSONL 事件翻译成
// /agent 页前端认的事件形态({type, data},reducer 按类型名子串分类)。
// 翻译规则的权威来源:docs/pi-agent-integration/07-poc-results.md §十.2。
// 注意:终态与压缩事件用前端 agent-workspace 精确匹配的名字
// (agent_done/agent_error/agent_cancelled、context_compacting/context_compacted),
// 改名必须与前端同步(见 TestWorkspaceTerminalNamesLocked)。
// 纯内存翻译,无 IO、不依赖 Pi 进程;调用方串行喂入事件(Translator 非并发安全)。
package translate

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"xlyra/server/internal/agent/session"
)

// Event 是前端事件。Data 恒为非 nil map(没有载荷时为空 map),
// 保证序列化后是 {} 而不是 null。
type Event struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Approval 是 extension_ui_request.message 的 JSON 约定(07 §十.3):
//
//	{"toolCallId":"...","toolName":"write","path":"out/a.txt","command":["touch","x"],"note":"..."}
type Approval struct {
	ToolCallID string   `json:"toolCallId"`
	ToolName   string   `json:"toolName"`
	Path       string   `json:"path"`
	Command    []string `json:"command"`
	Note       string   `json:"note"`
}

// OtherOptionMarker 是回答 JSON 里表示选中「其他」的约定标记:前端固定
// 追加「其他」选项,不占模型的标签空间,用哨兵值避免与真实标签冲突。
const OtherOptionMarker = "__other__"

// questionTitlePrefix 是 question 工具经 ctx.ui.editor 发起请求的 title
// 前缀,翻译层据此把它与其他 editor 请求区分开。
const questionTitlePrefix = "xlyra:question:"

// toolCall 记录一次已见的 tool_execution_start,供审批请求关联与兜底。
type toolCall struct {
	id      string
	name    string
	path    string
	command []string
	ended   bool // 已见 tool_execution_end
}

// Translator 保存跨事件流的翻译状态:contentIndex→toolCallId 映射、
// 已见的工具调用,以及本轮是否已发 error(agent_start 时重置)。
// 一个实例对应一段连续喂入的事件流(通常是一个 Pi 子进程的生命周期)。
type Translator struct {
	indexToID map[string]string // toolcall_start 的 contentIndex → 工具调用 id
	tools     map[string]*toolCall
	order     []*toolCall // tool_execution_start 到达顺序
	errorSent bool        // 本轮已发 agent_error;之后的 agent_settled 不再发 agent_done
	// 助手消息以 error 结束时先暂存:Pi 会在 agent_end 里用 willRetry 告知是否自动重试,
	// 重试中的失败不是终态,不能提前发 agent_error。
	pendingError    bool
	pendingErrorMsg string
	// 其他 UI 请求(cancel/select 之外)不产出事件,但 Pi 会一直等待对应的
	// extension_ui_response;id 记在这里供 runner 按取消回写,避免 Pi 永久阻塞。
	cancelUIRequestIDs []string
}

func New() *Translator {
	return &Translator{indexToID: map[string]string{}, tools: map[string]*toolCall{}}
}

// Feed 翻译一行 Pi 事件(原始 JSON 字节,不含换行),返回 0..n 个前端事件。
// 空白行、未知或应忽略的类型返回 (nil, nil);非法 JSON 返回 error,
// 调用方可据此把解析异常收口为 error 事件。
func (t *Translator) Feed(line []byte) ([]Event, error) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	kind, _ := raw["type"].(string)
	switch kind {
	case "agent_start":
		t.errorSent = false
		t.pendingError = false
		return []Event{event("agent_start", nil)}, nil
	case "agent_end":
		return t.agentEnd(raw), nil
	case "auto_retry_start":
		return t.autoRetryStart(raw), nil
	case "message_update":
		return t.messageUpdate(raw), nil
	case "message_end":
		return t.messageEnd(raw), nil
	case "tool_execution_start":
		return t.toolExecutionStart(raw), nil
	case "tool_execution_end":
		return t.toolExecutionEnd(raw), nil
	case "extension_ui_request":
		return t.extensionUIRequest(raw), nil
	case "compaction_start":
		reason, _ := raw["reason"].(string)
		return []Event{event("context_compacting", map[string]any{"manual": reason == "manual"})}, nil
	case "compaction_end":
		return []Event{compactionEnd(raw)}, nil
	case "agent_settled":
		return t.agentSettled(raw), nil
	default:
		// 忽略清单(response、auto_retry_*、entry_appended、queue_update、turn_*、
		// agent_end、message_start、tool_execution_update)与未知类型,见 07 §十.2。
		return nil, nil
	}
}

// CancelUIRequestIDs 返回自上次调用以来累积的「需要按取消回写」的 UI 请求
// id,并清空队列。这些 id 来自不认识的 extension_ui_request(input/editor 等)
// 或非法的 question prefill:翻译层不发事件,调用方(runner)应向 Pi 回写
// {cancelled:true} 的 extension_ui_response,否则 Pi 会永久阻塞。
// 注意:method 为 input 的请求 Pi 端可能有其他处理路径,是否回写由调用方
// 决定;目前仅在需要时消费。
func (t *Translator) CancelUIRequestIDs() []string {
	ids := t.cancelUIRequestIDs
	t.cancelUIRequestIDs = nil
	return ids
}

// Decision 在收到前端 grant-access 决定时生成 permission_result。
// 用户的审批决定不经过 Pi 的 stdout(Pi 走 stdin 的 extension_ui_response),
// 只能由调用方在此时补发;escalationID 即 extension_ui_request 的 id。
func (t *Translator) Decision(escalationID string, allowed bool) Event {
	return decisionEvent(escalationID, allowed, false)
}

// AutoDecision 生成会话内允许自动放行的 permission_result,额外带 auto 标记,
// 供前端区分「用户点击允许」与「会话内允许自动放行」。
func (t *Translator) AutoDecision(escalationID string, allowed bool) Event {
	return decisionEvent(escalationID, allowed, true)
}

func decisionEvent(escalationID string, allowed, auto bool) Event {
	data := map[string]any{"request_id": escalationID, "allowed": allowed}
	if auto {
		data["auto"] = true
	}
	return event("permission_result", data)
}

// ParseApprovalMessage 解析 extension_ui_request.message 的 JSON 约定(07 §十.3)。
// message 不是该约定的 JSON 时返回 ok=false,由调用方走兜底关联。
func ParseApprovalMessage(message string) (Approval, bool) {
	var approval Approval
	if err := json.Unmarshal([]byte(message), &approval); err != nil {
		return Approval{}, false
	}
	return approval, true
}

func (t *Translator) messageUpdate(raw map[string]any) []Event {
	nested, _ := raw["assistantMessageEvent"].(map[string]any)
	if nested == nil {
		return nil
	}
	kind, _ := nested["type"].(string)
	switch kind {
	case "text_delta":
		delta, _ := nested["delta"].(string)
		return []Event{event("text_delta", map[string]any{"delta": delta})}
	case "thinking_delta":
		delta, _ := nested["delta"].(string)
		return []Event{event("thinking", map[string]any{"delta": delta})}
	case "toolcall_start":
		id, _ := nested["id"].(string)
		name, _ := nested["toolName"].(string)
		if key := indexKey(nested["contentIndex"]); key != "" && id != "" {
			t.indexToID[key] = id // 后续 toolcall_delta 不带 id,只能靠 contentIndex 映射
		}
		return []Event{event("tool_call_start", map[string]any{
			"tool_call": map[string]any{"id": id, "name": name},
		})}
	case "toolcall_delta":
		id := t.indexToID[indexKey(nested["contentIndex"])]
		if id == "" {
			return nil // 映射不到就丢弃,不猜测(07 §十.2)
		}
		delta, _ := nested["delta"].(string)
		return []Event{event("tool_call_delta", map[string]any{
			"tool_call_id": id,
			"delta":        delta,
		})}
	case "toolcall_end":
		call, _ := nested["toolCall"].(map[string]any)
		var id, name string
		var args any
		if call != nil {
			id, _ = call["id"].(string)
			name, _ = call["name"].(string)
			args = call["arguments"]
		}
		if key := indexKey(nested["contentIndex"]); key != "" && id != "" {
			t.indexToID[key] = id
		}
		return []Event{event("tool_call", map[string]any{
			"tool_call": map[string]any{
				"id":            id,
				"name":          name,
				"raw_arguments": rawArguments(args),
			},
		})}
	default:
		return nil
	}
}

// message_end 只关心 assistant 消息:stopReason=="error" 时错误文案只能在这里
// 带给前端(Pi 没有独立的 error 事件)。终态事件名用前端 agent-workspace 精确匹配的
// agent_error(见组件里 ['agent_done','agent_error','agent_cancelled'].includes)。
func (t *Translator) messageEnd(raw map[string]any) []Event {
	message, _ := raw["message"].(map[string]any)
	if message == nil {
		return nil
	}
	if role, _ := message["role"].(string); role != "assistant" {
		return nil
	}
	stop, _ := message["stopReason"].(string)
	if stop != "error" {
		return nil
	}
	t.pendingError = true
	t.pendingErrorMsg, _ = message["errorMessage"].(string)
	return nil
}

// agentEnd 决定暂存的错误是终态还是将被自动重试吞掉:willRetry 为 true 时丢弃。
func (t *Translator) agentEnd(raw map[string]any) []Event {
	if willRetry, _ := raw["willRetry"].(bool); willRetry {
		t.pendingError = false
		return nil
	}
	return t.flushPendingError()
}

func (t *Translator) flushPendingError() []Event {
	if !t.pendingError {
		return nil
	}
	t.pendingError = false
	t.errorSent = true
	return []Event{event("agent_error", map[string]any{"message": t.pendingErrorMsg})}
}

// autoRetryStart 给前端一条进度提示(含 notice 字符串的事件会被归为 notice);
// 不带上游错误原文,避免把内部细节暴露在时间线上。
func (t *Translator) autoRetryStart(raw map[string]any) []Event {
	attempt, _ := raw["attempt"].(float64)
	maxAttempts, _ := raw["maxAttempts"].(float64)
	text := "上游暂时出错,正在自动重试"
	if attempt > 0 && maxAttempts > 0 {
		text += "(第 " + strconv.Itoa(int(attempt)) + "/" + strconv.Itoa(int(maxAttempts)) + " 次)"
	}
	return []Event{event("agent_notice", map[string]any{"notice": text})}
}

func (t *Translator) toolExecutionStart(raw map[string]any) []Event {
	id, _ := raw["toolCallId"].(string)
	name, _ := raw["toolName"].(string)
	args := raw["args"]

	call := &toolCall{id: id, name: name}
	if record, ok := args.(map[string]any); ok {
		call.path, _ = record["path"].(string)
		call.command = stringSlice(record["command"])
	}
	if id != "" {
		if _, seen := t.tools[id]; !seen {
			t.order = append(t.order, call)
			t.tools[id] = call // 记住调用供审批关联(07 §十.2)
		}
	}
	// 与 toolcall_end 重复发送无害:前端 reducer 按 id 回填,不会多出步骤。
	return []Event{event("tool_call", map[string]any{
		"tool_call": map[string]any{
			"id":            id,
			"name":          name,
			"raw_arguments": rawArguments(args),
		},
	})}
}

func (t *Translator) toolExecutionEnd(raw map[string]any) []Event {
	id, _ := raw["toolCallId"].(string)
	name, _ := raw["toolName"].(string)
	isError, _ := raw["isError"].(bool)
	if call := t.tools[id]; call != nil {
		call.ended = true
	}
	result := map[string]any{
		"tool_call_id": id,
		"name":         name,
		"output":       toolOutput(raw["result"]),
		"is_error":     isError,
	}
	if images := toolResultImages(raw["result"]); len(images) > 0 {
		result["images"] = images
	}
	if name == "edit" && !isError {
		if patch := toolResultPatch(raw["result"]); patch != "" {
			result["details_patch"] = patch
		}
	}
	return []Event{event("tool_result", map[string]any{
		"tool_result": result,
	})}
}

// extensionUIRequest 翻译审批/选择弹窗请求。message 按 07 §十.3 的 JSON 约定
// 携带 toolCallId 等信息;不是 JSON 时兜底关联最近一个还没返回结果的工具
// (审批正卡着的工具大概率就是它),取不到时 tool_name 留空。
func (t *Translator) extensionUIRequest(raw map[string]any) []Event {
	method, _ := raw["method"].(string)
	id, _ := raw["id"].(string)
	title, _ := raw["title"].(string)
	if method == "editor" {
		// question 工具用 editor 承载;其余 editor 请求(与本工具无关)
		// 无法在 xLyra 前端交互,只能丢弃并交由 runner 按取消回写,
		// 否则 Pi 会一直等待回答。
		if strings.HasPrefix(title, questionTitlePrefix) {
			return t.questionRequest(raw, id, title)
		}
		t.noteCancelUIRequest(id)
		return nil
	}
	if method != "confirm" && method != "select" {
		// input/notify 等方法同样丢弃;notify 无需回写但多记无害,
		// 是否回写由 runner 决定。
		t.noteCancelUIRequest(id)
		return nil
	}
	message, _ := raw["message"].(string)

	approval, isJSON := ParseApprovalMessage(message)
	tool := t.tools[approval.ToolCallID] // 非 JSON 时 ToolCallID 为空,必然查不到
	if !isJSON {
		tool = t.latestUnfinishedTool()
	}
	var toolName, path string
	var command []string
	if tool != nil {
		toolName, path, command = tool.name, tool.path, tool.command
	}
	if isJSON {
		// 约定里的显式字段优先于已见 tool_execution_start 关联出的信息
		if approval.ToolName != "" {
			toolName = approval.ToolName
		}
		if approval.Path != "" {
			path = approval.Path
		}
		if len(approval.Command) > 0 {
			command = approval.Command
		}
	}

	escalation := map[string]any{"escalation_id": id, "tool_name": toolName}
	if path != "" {
		escalation["requested_path"] = path
		escalation["resolved_path"] = path
	}
	if len(command) > 0 {
		escalation["requested_command"] = command
	}
	// note 随事件带出:bash 审批里携带工作目录与高危命中提示,调用方据此
	// 决定是否可会话内允许,前端也可展示。
	if isJSON && approval.Note != "" {
		escalation["note"] = approval.Note
	}
	return []Event{event("escalation_request", map[string]any{"escalation": escalation})}
}

func (t *Translator) latestUnfinishedTool() *toolCall {
	for i := len(t.order) - 1; i >= 0; i-- {
		if !t.order[i].ended {
			return t.order[i]
		}
	}
	return nil
}

func (t *Translator) noteCancelUIRequest(id string) {
	if id != "" {
		t.cancelUIRequestIDs = append(t.cancelUIRequestIDs, id)
	}
}

// questionPrefill 是 editor 请求 prefill 里携带的问题 JSON(扩展 xlyra.ts
// 组装):{"toolCallId","questions":[{header,question,multiSelect,options}]},
// 多余字段忽略。问数、字段与选项数的合法性见 questionPrefillValid。
type questionPrefill struct {
	ToolCallID string                 `json:"toolCallId"`
	Questions  []session.QuestionItem `json:"questions"`
}

// questionPrefillValid 校验 prefill:题数 1–4;每题 header/question 非空;
// 选项 2–6 且 label 非空。字段长度与重复性由扩展先行校验,这里只做结构约束,
// 保证发出的 question_request 可被前端安全渲染。
func questionPrefillValid(prefill *questionPrefill) bool {
	if len(prefill.Questions) < 1 || len(prefill.Questions) > 4 {
		return false
	}
	for _, item := range prefill.Questions {
		if item.Header == "" || item.Question == "" {
			return false
		}
		if len(item.Options) < 2 || len(item.Options) > 6 {
			return false
		}
		for _, opt := range item.Options {
			if opt.Label == "" {
				return false
			}
		}
	}
	return true
}

// questionRequest 解析并翻译 question 工具的 editor 请求:prefill 合法时发
// question_request 事件(问题形态与 session.QuestionItem 一致,description
// 缺省省略);tool_call_id 优先取 prefill 里带的,title 后缀作为兑底。
// 非法时不发事件,请求 id 记入取消队列供 runner 回写取消。
func (t *Translator) questionRequest(raw map[string]any, id, title string) []Event {
	prefillJSON, _ := raw["prefill"].(string)
	var prefill questionPrefill
	if prefillJSON == "" || json.Unmarshal([]byte(prefillJSON), &prefill) != nil ||
		!questionPrefillValid(&prefill) {
		t.noteCancelUIRequest(id)
		return nil
	}
	toolCallID := prefill.ToolCallID
	if toolCallID == "" {
		toolCallID = strings.TrimPrefix(title, questionTitlePrefix)
	}
	questions := make([]map[string]any, 0, len(prefill.Questions))
	for _, item := range prefill.Questions {
		options := make([]map[string]any, 0, len(item.Options))
		for _, opt := range item.Options {
			option := map[string]any{"label": opt.Label}
			if opt.Description != "" {
				option["description"] = opt.Description
			}
			options = append(options, option)
		}
		questions = append(questions, map[string]any{
			"header": item.Header, "question": item.Question,
			"multiSelect": item.MultiSelect, "options": options,
		})
	}
	return []Event{event("question_request", map[string]any{
		"question": map[string]any{
			"question_id":  id,
			"tool_call_id": toolCallID,
			"questions":    questions,
		},
	})}
}

// QuestionAnswered 在问题被回答(提交/跳过)或收口(停止取消、超时兑底)
// 时生成 question_answered 事件。布尔字段仅为真时输出,与 AutoDecision 的
// auto 标记风格一致;answers 为空时输出空数组(skipped/cancelled 时常见)。
func (t *Translator) QuestionAnswered(id string, answers []session.QuestionAnswer, skipped, cancelled, auto bool) []Event {
	items := make([]map[string]any, 0, len(answers))
	for _, answer := range answers {
		item := map[string]any{"selected": answer.Selected}
		if answer.Other != "" {
			item["other"] = answer.Other
		}
		items = append(items, item)
	}
	data := map[string]any{"question_id": id, "answers": items}
	if skipped {
		data["skipped"] = true
	}
	if cancelled {
		data["cancelled"] = true
	}
	if auto {
		data["auto"] = true
	}
	return []Event{event("question_answered", data)}
}

// compactionEnd 带 result 时除透传原字段外,补 tokens_before/tokens_after:
// Pi 原生字段名是 tokensBefore/estimatedTokensAfter,前端转录条目读的是
// tokens_before/tokens_after(见 agent-events.ts 及任务 P2.2 约定)。
func compactionEnd(raw map[string]any) Event {
	result, _ := raw["result"].(map[string]any)
	if result == nil {
		return event("context_compacted", nil) // 无 result:前端标为 skipped
	}
	compaction := make(map[string]any, len(result)+2)
	for key, value := range result {
		compaction[key] = value
	}
	if before, ok := result["tokensBefore"].(float64); ok {
		compaction["tokens_before"] = before
	}
	if after, ok := result["estimatedTokensAfter"].(float64); ok {
		compaction["tokens_after"] = after
	}
	return event("context_compacted", map[string]any{"compaction": compaction})
}

func (t *Translator) agentSettled(raw map[string]any) []Event {
	// 没见到 agent_end 就结算:暂存的错误仍是本轮的终态
	events := t.flushPendingError()
	aborted, _ := raw["aborted"].(bool)
	if aborted {
		t.errorSent = false
		return append(events, event("agent_cancelled", map[string]any{"aborted": true}))
	}
	if t.errorSent {
		t.errorSent = false
		return events // agent_error 已经收口本轮,agent_done 会把它覆盖成 done(07 §十.2)
	}
	return []Event{event("agent_done", nil)}
}

func event(typ string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{Type: typ, Data: data}
}

// indexKey 把 contentIndex(JSON 数字或字符串)归一成 map 键。
func indexKey(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return typed
	default:
		return ""
	}
}

// rawArguments 把工具参数归一成完整 JSON 字符串;nil 时为空串。
func rawArguments(args any) string {
	switch typed := args.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case string: // bash 类工具的 command 是单个字符串
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		var out []string
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// toolResultImages 与 transcript 使用相同的白名单与截断规则;result/details
// 形状异常时只省略 images,不影响原有工具输出。
//
// 返回 []any 而非 []map[string]any:事件最终以 JSON 传给前端,而 runner 的
// golden 比对用 reflect.DeepEqual,运行时形态必须与 JSON 反序列化结果一致。
func toolResultImages(result any) []any {
	record, _ := result.(map[string]any)
	details, _ := record["details"].(map[string]any)
	items, _ := details["images"].([]any)
	images := make([]any, 0, len(items))
	for _, value := range items {
		item, _ := value.(map[string]any)
		path, _ := item["path"].(string)
		if path == "" || len(path) > 200 || !strings.HasPrefix(path, "images/") ||
			strings.Contains(path, "..") || strings.Contains(path, `\`) {
			continue
		}
		mime, _ := item["mime"].(string)
		switch mime {
		case "image/png", "image/jpeg", "image/webp":
		default:
			mime = ""
		}
		size, _ := item["bytes"].(float64)
		if size < 0 || math.Trunc(size) != size {
			size = 0
		}
		images = append(images, map[string]any{
			"path": path, "mime": mime, "bytes": size,
			"model": toolResultImageString(item["model"]),
			"site":  toolResultImageString(item["site"]),
			"alt":   toolResultImageString(item["alt"]),
		})
	}
	return images
}

// toolResultPatch 提取 edit 结果的统一 diff,空值与超大 patch 不透传。
func toolResultPatch(result any) string {
	record, _ := result.(map[string]any)
	details, _ := record["details"].(map[string]any)
	patch, _ := details["patch"].(string)
	if patch == "" || len(patch) > 200<<10 {
		return ""
	}
	return patch
}

func toolResultImageString(value any) string {
	text, _ := value.(string)
	runes := []rune(text)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return text
}

// toolOutput 把工具结果拼成前端可展示文本:文本块用换行连接,图片块用占位。
func toolOutput(result any) string {
	if text, ok := result.(string); ok {
		return text
	}
	record, _ := result.(map[string]any)
	if record == nil {
		return ""
	}
	content, _ := record["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, item := range content {
		block, _ := item.(map[string]any)
		if block == nil {
			continue
		}
		if kind, _ := block["type"].(string); kind == "image" {
			parts = append(parts, "[image]")
			continue
		}
		if text, ok := block["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
