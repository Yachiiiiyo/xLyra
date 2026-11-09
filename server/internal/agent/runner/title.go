package runner

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

const (
	titleTimeout      = 15 * time.Second
	titleMaxTokens    = 100
	titlePromptRunes  = 500
	titleResultRunes  = 40
	titleCompleteKind = "session_title"
)

const titleSystemPrompt = `根据用户的第一条消息生成会话标题。标题不超过 20 个字,使用与用户相同的语言,概括任务而不是复述原话。只输出标题本身,不要引号、句号或任何解释。`

// errTitleChanged 表示标题已被用户或其它逻辑改过,不再覆盖。
var errTitleChanged = errors.New("runner: 会话标题已变更")

// maybeGenerateTitle 在会话首条消息发送后异步用快速模型生成标题;失败保持截断标题。
// goroutine 计入 r.wg,显式停止 run 或 Shutdown 时取消;正常 run 结束不取消,
// 此时仍会落盘,只是不再发 SSE 事件。调用时 r.wg 必须已因本 run 的 admit 大于零。
func (r *Runner) maybeGenerateTitle(ar *activeRun, content string) {
	if r.cfg.CompleteText == nil {
		return
	}
	placeholder := session.TitleFrom(content)
	if meta, err := r.cfg.Store.Get(ar.sessionID); err != nil || meta.Title != placeholder {
		return
	}
	prompt := strings.TrimSpace(content)
	if utf8.RuneCountInString(prompt) > titlePromptRunes {
		prompt = string([]rune(prompt)[:titlePromptRunes])
	}
	if prompt == "" {
		return
	}
	model := r.fastModel(ar.model)
	run := ar.finishedRun()
	parent := r.bgCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, titleTimeout)
	ar.stateMu.Lock()
	if ar.stopping.Load() {
		ar.stateMu.Unlock()
		cancel()
		return
	}
	ar.titleCancel = cancel
	ar.stateMu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		defer func() {
			ar.stateMu.Lock()
			ar.titleCancel = nil
			ar.stateMu.Unlock()
		}()
		started := time.Now()
		text, err := r.cfg.CompleteText(ctx, ar.sessionID, model, titleSystemPrompt, prompt, titleMaxTokens, titleCompleteKind)
		if err != nil {
			r.logger.Debug("生成会话标题失败", "session_id", ar.sessionID, "model", model, "duration", time.Since(started), "error", err)
			return
		}
		title := cleanTitle(text)
		if title == "" {
			r.logger.Debug("生成会话标题为空", "session_id", ar.sessionID, "model", model, "duration", time.Since(started))
			return
		}
		if _, err := r.cfg.Store.Update(ar.sessionID, func(m *session.Meta) error {
			if m.Title != placeholder {
				return errTitleChanged
			}
			m.Title = title
			return nil
		}); err != nil {
			r.logger.Debug("写回会话标题跳过", "session_id", ar.sessionID, "duration", time.Since(started), "error", err)
			return
		}
		published := false
		if run != nil {
			published = run.Publish(translate.Event{Type: "session_title", Data: map[string]any{"title": title}})
		}
		r.logger.Debug("生成会话标题完成", "session_id", ar.sessionID, "model", model, "duration", time.Since(started), "title", title, "published", published)
	}()
}

// titleQuotes 是标题首尾要成对去掉的引号/书名号/反引号。
var titleQuotes = [][2]string{
	{`"`, `"`}, {"'", "'"}, {"`", "`"}, {"“", "”"}, {"‘", "’"},
	{"「", "」"}, {"『", "』"}, {"《", "》"}, {"〈", "〉"}, {"«", "»"},
}

// titleTrailingPunct 是标题末尾要去掉的句读;括号等成对符号保留(如 "foo()")。
const titleTrailingPunct = "。．.，,、;；:：!！?？…~～"

// cleanTitle 清洗模型输出:取首个非空行、去首尾空白与成对引号、去末尾标点,
// 按 rune 截到 40;结果为空表示丢弃。
func cleanTitle(text string) string {
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			line = candidate
			break
		}
	}
	for changed := true; changed; {
		changed = false
		line = strings.TrimSpace(line)
		for _, pair := range titleQuotes {
			if len(line) >= len(pair[0])+len(pair[1]) && strings.HasPrefix(line, pair[0]) && strings.HasSuffix(line, pair[1]) {
				line = line[len(pair[0]) : len(line)-len(pair[1])]
				changed = true
			}
		}
		trimmed := strings.TrimRightFunc(line, func(r rune) bool { return strings.ContainsRune(titleTrailingPunct, r) || unicode.IsSpace(r) })
		if trimmed != line {
			line = trimmed
			changed = true
		}
	}
	if utf8.RuneCountInString(line) > titleResultRunes {
		line = strings.TrimSpace(string([]rune(line)[:titleResultRunes]))
	}
	return line
}
