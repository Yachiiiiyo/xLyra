package agent

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"xlyra/server/internal/httpx"
)

const (
	suggestionSystemPrompt = `你在预测用户在与 AI 编程助手对话中「下一条最可能发送的消息」。只输出这一条消息本身，不加引号、标签、解释。必须是用户口吻的一句短话（不超过 30 个字），像真实用户会敲的指令或回答，例如「把这个函数也补上测试」「继续」「提交吧」「为什么会报这个错」。不要总结、不要寒暄、不要评价、不要用助手口吻（不要出现“让我”“我来”）。如果无法判断用户下一步会说什么，输出空内容。`
	suggestionPromptSuffix = "请给出用户下一条最可能发送的消息。"
	suggestionTimeout      = 8 * time.Second
	suggestionMaxTokens    = 128
)

var (
	suggestionLabelPattern    = regexp.MustCompile(`^\w+:\s`)
	suggestionSentencePattern = regexp.MustCompile(`[。！？!?]\s*\S|\.\s+\S`)
	suggestionWordPattern     = regexp.MustCompile(`[A-Za-z]+`)
)

type suggestionRule struct {
	reason string
	reject func(string) bool
}

var suggestionRules = []suggestionRule{
	{"empty", func(text string) bool { return text == "" }},
	{"done", func(text string) bool {
		normalized := strings.ToLower(strings.TrimFunc(text, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsPunct(r)
		}))
		return normalized == "done" || normalized == "完成" || normalized == "已完成"
	}},
	{"meta_text", func(text string) bool {
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, "nothing to suggest") || strings.HasPrefix(lower, "no suggestion") {
			return true
		}
		for _, value := range []string{"沉默", "无建议", "没有建议", "暂无建议", "无"} {
			if text == value {
				return true
			}
		}
		return false
	}},
	{"meta_wrapped", func(text string) bool {
		_, ok := stripSuggestionWrapper(text, [][2]string{{"(", ")"}, {"[", "]"}, {"（", "）"}, {"【", "】"}})
		return ok
	}},
	{"error_message", func(text string) bool {
		lower := strings.ToLower(text)
		for _, prefix := range []string{"api error:", "prompt is too long", "request timed out", "invalid api key"} {
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		}
		return false
	}},
	{"prefixed_label", func(text string) bool { return suggestionLabelPattern.MatchString(text) }},
	{"too_short", func(text string) bool {
		if utf8.RuneCountInString(text) >= 2 || strings.HasPrefix(text, "/") {
			return false
		}
		for _, allowed := range []string{"好", "嗯", "是的", "继续", "提交", "推送", "部署", "停止", "可以", "不用", "yes", "ok", "okay", "sure", "no", "commit", "push", "deploy", "continue", "stop"} {
			if strings.EqualFold(text, allowed) {
				return false
			}
		}
		return true
	}},
	{"too_long", func(text string) bool {
		return utf8.RuneCountInString(text) > 40 || len(suggestionWordPattern.FindAllString(text, -1)) > 12
	}},
	{"multiple_sentences", func(text string) bool { return suggestionSentencePattern.MatchString(text) }},
	{"has_formatting", func(text string) bool {
		return strings.ContainsAny(text, "\r\n*`") || strings.HasPrefix(text, "#")
	}},
	{"evaluative", func(text string) bool {
		lower := strings.ToLower(text)
		for _, value := range []string{"谢谢", "感谢", "辛苦了", "很好", "不错", "太棒了", "完美", "看起来不错", "没问题", "thanks", "thank you", "looks good", "sounds good", "great", "perfect", "awesome"} {
			if strings.Contains(lower, value) {
				return true
			}
		}
		return false
	}},
	{"assistant_voice", func(text string) bool {
		normalized := strings.ReplaceAll(strings.ToLower(text), "，", ",")
		for _, prefix := range []string{"让我", "我来", "我会", "我将", "我可以", "我建议", "好的,我", "这是", "这个是", "以下", "let me", "i'll", "i've", "i'm", "i can", "here's", "here is", "sure,", "of course"} {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
		return false
	}},
}

// filterSuggestion 清洗模型输出后按顺序套用丢弃规则。
func filterSuggestion(text string) (string, bool) {
	cleaned, reason := filterSuggestionReason(text)
	return cleaned, reason == ""
}

func filterSuggestionReason(text string) (string, string) {
	text = cleanSuggestion(text)
	for _, rule := range suggestionRules {
		if rule.reject(text) {
			return "", rule.reason
		}
	}
	return text, ""
}

func cleanSuggestion(text string) string {
	text = strings.TrimSpace(text)
	for _, tag := range []string{"suggestion", "response", "output", "answer", "result"} {
		open, close := "<"+tag+">", "</"+tag+">"
		if len(text) >= len(open)+len(close) && strings.EqualFold(text[:len(open)], open) && strings.EqualFold(text[len(text)-len(close):], close) {
			text = strings.TrimSpace(text[len(open) : len(text)-len(close)])
			break
		}
	}
	for _, label := range []string{"建议", "回复", "回答", "输出", "结果", "Suggestion", "Response", "Reply", "Output", "Result"} {
		for _, colon := range []string{":", "："} {
			prefix := label + colon
			if len(text) >= len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
				text = strings.TrimSpace(text[len(prefix):])
			}
		}
	}
	if unwrapped, ok := stripSuggestionWrapper(text, [][2]string{{"\"", "\""}, {"'", "'"}, {"“", "”"}, {"「", "」"}}); ok {
		text = strings.TrimSpace(unwrapped)
	}
	return text
}

func stripSuggestionWrapper(text string, pairs [][2]string) (string, bool) {
	for _, pair := range pairs {
		if len(text) >= len(pair[0])+len(pair[1]) && strings.HasPrefix(text, pair[0]) && strings.HasSuffix(text, pair[1]) {
			return text[len(pair[0]) : len(text)-len(pair[1])], true
		}
	}
	return text, false
}

// renderSuggestionContext 把 transcript 的最近三轮渲染成预测提示词。
func renderSuggestionContext(entries []map[string]any) string {
	start := 0
	var userIndexes []int
	for i, entry := range entries {
		if message, ok := suggestionMessage(entry); ok && message["role"] == "user" {
			userIndexes = append(userIndexes, i)
		}
	}
	if len(userIndexes) > 3 {
		start = userIndexes[len(userIndexes)-3]
	}
	var lines []string
	for _, entry := range entries[start:] {
		message, ok := suggestionMessage(entry)
		if !ok {
			continue
		}
		content, _ := message["content"].(string)
		switch message["role"] {
		case "user":
			lines = append(lines, "用户:"+truncateSuggestionText(strings.TrimSpace(content), 500))
		case "assistant":
			if text := lastSuggestionParagraph(content); text != "" {
				lines = append(lines, "助手:"+truncateSuggestionText(text, 1500))
			}
			for _, name := range suggestionToolCallNames(message["tool_calls"]) {
				lines = append(lines, "[调用工具 "+name+"]")
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n" + suggestionPromptSuffix
}

func suggestionMessage(entry map[string]any) (map[string]any, bool) {
	if entry["type"] != "message" {
		return nil, false
	}
	message, ok := entry["message"].(map[string]any)
	return message, ok
}

func suggestionToolCallNames(value any) []string {
	var names []string
	appendCall := func(call map[string]any) {
		if name, _ := call["name"].(string); strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	switch calls := value.(type) {
	case []map[string]any:
		for _, call := range calls {
			appendCall(call)
		}
	case []any:
		for _, value := range calls {
			if call, ok := value.(map[string]any); ok {
				appendCall(call)
			}
		}
	}
	return names
}

func lastSuggestionParagraph(content string) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	lines := strings.Split(content, "\n")
	start := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			start = i + 1
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

func truncateSuggestionText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

func endsWithAssistantText(entries []map[string]any) bool {
	for i := len(entries) - 1; i >= 0; i-- {
		entryType, _ := entries[i]["type"].(string)
		if entryType == "run_changes" {
			continue
		}
		message, ok := suggestionMessage(entries[i])
		if !ok || message["role"] != "assistant" {
			return false
		}
		content, _ := message["content"].(string)
		return lastSuggestionParagraph(content) != ""
	}
	return false
}

// suggestionRunWait 是预测请求等待会话 run 收尾的上限。
var suggestionRunWait = 3 * time.Second

// waitRunFinished 等会话当前 run 结束;超时或请求取消返回 false。
func (h *Handler) waitRunFinished(ctx context.Context, id string) bool {
	run := h.runs.Current(id)
	if run == nil {
		return true
	}
	timer := time.NewTimer(suggestionRunWait)
	defer timer.Stop()
	select {
	case <-run.Done():
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// resolveSuggestionModel 优先使用快速模型,未配置时回退会话模型。
func resolveSuggestionModel(sessionModel, fastModel string) string {
	if fastModel = strings.TrimSpace(fastModel); fastModel != "" {
		return fastModel
	}
	return strings.TrimSpace(sessionModel)
}

// Suggestion POST /sessions/{id}/suggestion:无法给出可靠预测时统一返回
// 200 + null,让前端静默隐藏灰字。
func (h *Handler) Suggestion(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	requestID := uuid.NewString()
	id := strings.TrimSpace(r.PathValue("id"))
	suggestion := ""
	reason := ""
	defer func() {
		h.logger.Debug("Agent 输入预测完成", "session_id", id, "request_id", requestID,
			"hit", suggestion != "", "reason", reason, "duration", time.Since(startedAt))
	}()
	respond := func() {
		var value any
		if suggestion != "" {
			value = suggestion
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"suggestion": value, "request_id": requestID}})
	}
	var ok bool
	id, ok = h.sessionID(w, r)
	if !ok {
		reason = "not_found"
		return
	}
	meta, err := h.store.Get(id)
	if err != nil {
		reason = "not_found"
		h.fail(w, r, err)
		return
	}
	if h.suggestionEnabled == nil || !h.suggestionEnabled() || h.suggest == nil {
		reason = "disabled"
		respond()
		return
	}
	// 前端收到 agent_done 就会请求预测,而 run 在事件发出后还要收尾才标记结束,
	// 所以仍在运行时先短暂等它结束,等不到(多半是用户又发了新消息)再放弃。
	if h.runs != nil && h.runs.Running(id) && !h.waitRunFinished(r.Context(), id) {
		reason = "running"
		respond()
		return
	}
	for _, question := range meta.Questions {
		if question.AnsweredAt == nil {
			reason = "pending_question"
			respond()
			return
		}
	}
	for _, escalation := range meta.Escalations {
		if escalation.Granted == nil {
			reason = "pending_escalation"
			respond()
			return
		}
	}
	entries, err := h.store.BuildTranscript(id)
	if err != nil {
		reason = "transcript_error"
		h.fail(w, r, err)
		return
	}
	if !endsWithAssistantText(entries) {
		reason = "invalid_transcript_end"
		respond()
		return
	}
	if len(meta.Messages) < 1 {
		reason = "no_user_messages"
		respond()
		return
	}
	userPrompt := renderSuggestionContext(entries)
	if strings.TrimSpace(userPrompt) == "" {
		reason = "empty_context"
		respond()
		return
	}
	sessionModel := strings.TrimSpace(meta.Model)
	fastModel := ""
	if h.fastModel != nil {
		fastModel = strings.TrimSpace(h.fastModel())
	}
	model := resolveSuggestionModel(sessionModel, fastModel)
	if model == "" {
		reason = "model_unavailable"
		respond()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), suggestionTimeout)
	defer cancel()
	text, err := h.suggest(ctx, id, model, suggestionSystemPrompt, userPrompt, suggestionMaxTokens)
	if err != nil && fastModel != "" && sessionModel != "" && sessionModel != model &&
		!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
		text, err = h.suggest(ctx, id, sessionModel, suggestionSystemPrompt, userPrompt, suggestionMaxTokens)
		if err == nil {
			reason = "fast_model_fallback"
		}
	}
	if err != nil {
		reason = "suggest_error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = "timeout"
		}
		respond()
		return
	}
	text, filterReason := filterSuggestionReason(text)
	if filterReason != "" {
		reason = filterReason
		respond()
		return
	}
	suggestion = text
	respond()
}
