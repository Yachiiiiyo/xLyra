// questions.go:question 工具的运行器侧——事件循环落盘 pending 提问、回答
// 回写与取消收口。协议与校验约定见 docs/pi-agent-integration/11-question-tool.md。
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"xlyra/server/internal/agent/pirpc"
	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

// AnswerQuestionInput 是一次用户回答的入参;Answers 与 Questions 条数一致,
// Skipped=true 时忽略 Answers。
type AnswerQuestionInput struct {
	SessionID  string
	QuestionID string
	Answers    []session.QuestionAnswer
	Skipped    bool
}

// questionResponseValue 是回写给 Pi 的 extension_ui_response.value 的内层
// JSON(与扩展约定一致,键名 selected/other;value 本身是字符串)。
type questionResponseValue struct {
	Answers []session.QuestionAnswer `json:"answers"`
	Skipped bool                     `json:"skipped,omitempty"`
}

// recordQuestion 处理一条 question_request 事件:登记 pending、落盘
// meta.Questions。锚点取法与审批一致(优先事件循环里最后见到的 Pi 条目
// id,否则从 Pi 会话文件末尾取)。同一时刻最多 1 个 pending question:
// 再来一个直接按取消回写并忽略(Pi 同一时刻只会有一个 editor 在等,防御)。
// 返回是否登记成功;未登记的事件调用方不应发布。
func (r *Runner) recordQuestion(ar *activeRun, event translate.Event, afterEntryID string) bool {
	data, _ := event.Data["question"].(map[string]any)
	id, _ := data["question_id"].(string)
	if id == "" {
		return false
	}
	encoded, err := json.Marshal(data)
	var q session.Question
	if err != nil || json.Unmarshal(encoded, &q) != nil || q.QuestionID != id || len(q.Questions) == 0 {
		r.cancelUIWriteback(ar, id)
		return false
	}
	// 只有事件循环会登记 pending,取锚点(文件 IO)放在锁外。
	q.AfterEntryID = afterEntryID
	if q.AfterEntryID == "" {
		q.AfterEntryID = r.anchorFromSessionFile(ar)
	}
	q.AskedAt = time.Now().UTC()
	ar.stateMu.Lock()
	if len(ar.pendingQuestions) > 0 {
		ar.stateMu.Unlock()
		r.logger.Warn("已有未决提问,新提问按取消回写", "session_id", ar.sessionID, "run_id", ar.runID, "question_id", id)
		r.cancelUIWriteback(ar, id)
		return false
	}
	ar.pendingQuestions[id] = q
	if timeout := r.cfg.QuestionTimeout; timeout > 0 {
		ar.questionTimers[id] = time.AfterFunc(timeout, func() { r.autoSkipQuestion(ar, id) })
	}
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	if err := r.cfg.Store.AddQuestion(ar.sessionID, q); err != nil {
		r.logger.Error("记录提问失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	return true
}

// drainCancelUIRequests 取走翻译层累积的「需按取消回写」的 UI 请求 id
// (非法 question、其他 editor、未识别 method),逐个向 Pi 回写 cancelled。
// notify 请求的回写无害。
func (r *Runner) drainCancelUIRequests(ar *activeRun) {
	for _, id := range ar.tr.CancelUIRequestIDs() {
		r.cancelUIWriteback(ar, id)
	}
}

// cancelUIWriteback 向 Pi 回写一条 cancelled 的 extension_ui_response;
// 进程已退出时忽略(收尾路径会落盘标记取消)。
func (r *Runner) cancelUIWriteback(ar *activeRun, id string) {
	if proc := ar.proc.Load(); proc != nil {
		if err := proc.Send(map[string]any{
			"type": "extension_ui_response", "id": id, "cancelled": true,
		}); err != nil && !errors.Is(err, pirpc.ErrProcessExited) {
			r.logger.Warn("回写 UI 请求取消失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
	}
}

// AnswerQuestion 回答一个 pending question:校验答案、回写 Pi、落盘
// meta.Questions 并发布 question_answered 事件。
func (r *Runner) AnswerQuestion(_ context.Context, in AnswerQuestionInput) error {
	questionID := strings.TrimSpace(in.QuestionID)
	if questionID == "" {
		return fmt.Errorf("%w: question_id 不能为空", ErrInvalidAnswer)
	}
	if _, err := r.cfg.Store.Get(in.SessionID); err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInvalidID) {
			return fmt.Errorf("%w: 会话 %s", ErrNotFound, in.SessionID)
		}
		return err
	}
	r.mu.Lock()
	ar := r.active[in.SessionID]
	r.mu.Unlock()
	if ar == nil {
		return fmt.Errorf("%w: 会话 %s 没有进行中的 run", ErrNotRunning, in.SessionID)
	}
	ar.stateMu.Lock()
	q, ok := ar.pendingQuestions[questionID]
	if !ok {
		ar.stateMu.Unlock()
		return fmt.Errorf("%w: 问题 %s", ErrNotFound, questionID)
	}
	answers := []session.QuestionAnswer{}
	if !in.Skipped {
		var err error
		answers, err = validateQuestionAnswers(q, in.Answers)
		if err != nil {
			ar.stateMu.Unlock()
			return err
		}
	}
	ar.dropQuestionLocked(questionID)
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	return r.finishQuestion(ar, questionID, answers, in.Skipped, false)
}

// autoSkipQuestion 在提问超时无人回答时按跳过回写(auto)。
func (r *Runner) autoSkipQuestion(ar *activeRun, id string) {
	ar.stateMu.Lock()
	if _, ok := ar.pendingQuestions[id]; !ok {
		ar.stateMu.Unlock()
		return
	}
	ar.dropQuestionLocked(id)
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	_ = r.finishQuestion(ar, id, []session.QuestionAnswer{}, true, true)
}

// dropQuestionLocked 移除 pending 提问并停掉其超时计时器;调用方持有 stateMu。
func (ar *activeRun) dropQuestionLocked(id string) {
	delete(ar.pendingQuestions, id)
	if t := ar.questionTimers[id]; t != nil {
		t.Stop()
		delete(ar.questionTimers, id)
	}
}

// finishQuestion 落盘、发布并向 Pi 回写一条已出队的提问回答。
func (r *Runner) finishQuestion(ar *activeRun, questionID string, answers []session.QuestionAnswer, skipped, auto bool) error {
	// value 是 JSON 字符串(与扩展约定一致),键名 selected/other。
	value, err := json.Marshal(questionResponseValue{Answers: answers, Skipped: skipped})
	if err != nil {
		return fmt.Errorf("runner: 序列化回答失败: %w", err)
	}
	// 先落盘、发布再回写:Pi 收到回答后会继续输出,question_answered 不能
	// 排到后续事件(乃至 agent_done)之后。
	if err := r.cfg.Store.AnswerQuestion(ar.sessionID, questionID, answers, skipped, false, auto, time.Now().UTC()); err != nil {
		r.logger.Error("保存提问回答失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	for _, event := range ar.tr.QuestionAnswered(questionID, answers, skipped, false, auto) {
		r.publish(ar, event)
	}
	if proc := ar.proc.Load(); proc != nil {
		if err := proc.Send(map[string]any{
			"type": "extension_ui_response", "id": questionID, "value": string(value),
		}); err != nil && !errors.Is(err, pirpc.ErrProcessExited) {
			r.logger.Warn("回写回答失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
	}
	return nil
}

// validateQuestionAnswers 校验答案与题目匹配并返回规范化副本(切片不与
// 调用方共享)。非法返回可映射为 HTTP 400 的 ErrInvalidAnswer。
func validateQuestionAnswers(q session.Question, answers []session.QuestionAnswer) ([]session.QuestionAnswer, error) {
	if len(answers) != len(q.Questions) {
		return nil, fmt.Errorf("%w: 答案条数 %d 与问题数 %d 不一致", ErrInvalidAnswer, len(answers), len(q.Questions))
	}
	result := make([]session.QuestionAnswer, len(answers))
	for i, item := range q.Questions {
		answer := answers[i]
		if len(answer.Selected) == 0 {
			return nil, fmt.Errorf("%w: 第 %d 题未选择任何选项", ErrInvalidAnswer, i+1)
		}
		if !item.MultiSelect && len(answer.Selected) > 1 {
			return nil, fmt.Errorf("%w: 第 %d 题是单选题,最多选 1 个", ErrInvalidAnswer, i+1)
		}
		seen := make(map[string]bool, len(item.Options)+1)
		for _, label := range answer.Selected {
			if seen[label] {
				return nil, fmt.Errorf("%w: 第 %d 题选中了重复选项 %q", ErrInvalidAnswer, i+1, label)
			}
			seen[label] = true
			if label == session.OtherOptionMarker {
				continue
			}
			if !slices.ContainsFunc(item.Options, func(opt session.QuestionOption) bool { return opt.Label == label }) {
				return nil, fmt.Errorf("%w: 第 %d 题没有选项 %q", ErrInvalidAnswer, i+1, label)
			}
		}
		other := strings.TrimSpace(answer.Other)
		if slices.Contains(answer.Selected, session.OtherOptionMarker) {
			if n := utf8.RuneCountInString(other); n < 1 || n > 2000 {
				return nil, fmt.Errorf("%w: 第 %d 题选了「其他」但补充文字为空或超过 2000 字", ErrInvalidAnswer, i+1)
			}
		} else if other != "" {
			return nil, fmt.Errorf("%w: 第 %d 题未选「其他」却填写了补充文字", ErrInvalidAnswer, i+1)
		}
		result[i] = session.QuestionAnswer{
			Selected: append([]string(nil), answer.Selected...), Other: answer.Other,
		}
	}
	return result, nil
}

// cancelPendingQuestions 对所有 pending question 收口:回写 cancelled、
// 落盘标记取消并发布 question_answered(cancelled) 事件。Stop 与 run 收尾
// 共用;并发调用安全(按 pending 表去重)。
func (r *Runner) cancelPendingQuestions(ar *activeRun) {
	ar.stateMu.Lock()
	ids := make([]string, 0, len(ar.pendingQuestions))
	for id := range ar.pendingQuestions {
		ids = append(ids, id)
	}
	ar.stateMu.Unlock()
	for _, id := range ids {
		r.cancelQuestion(ar, id)
	}
}

// cancelQuestion 取消一个 pending question(Stop、进程异常退出等路径)。
func (r *Runner) cancelQuestion(ar *activeRun, id string) {
	ar.stateMu.Lock()
	if _, ok := ar.pendingQuestions[id]; !ok {
		ar.stateMu.Unlock()
		return
	}
	ar.dropQuestionLocked(id)
	ar.stateMu.Unlock()
	ar.notifyWaitChanged()
	if err := r.cfg.Store.AnswerQuestion(ar.sessionID, id, nil, false, true, false, time.Now().UTC()); err != nil {
		r.logger.Error("标记提问取消失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	for _, event := range ar.tr.QuestionAnswered(id, nil, false, true, false) {
		r.publish(ar, event)
	}
	r.cancelUIWriteback(ar, id)
}
