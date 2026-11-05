// questions_test.go:question 工具的运行器侧——落盘与锚点、回答回写与校验、
// 非法/其他 UI 请求按取消回写、第二个 pending、Stop 与收尾取消、白名单与提示。
package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

func newQuestionEnv(t *testing.T, options ...envOption) *testEnv {
	t.Helper()
	return newTestEnv(t, "basic", append([]envOption{withLocalFixture("question.jsonl"), withGrowSession()}, options...)...)
}

// uiResponses 按出现顺序收集假 Pi 收到的 extension_ui_response 命令。
func uiResponses(t *testing.T, record string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, item := range readRecords(t, record) {
		if item["kind"] != "command" {
			continue
		}
		command, _ := item["command"].(map[string]any)
		if command["type"] == "extension_ui_response" {
			result = append(result, command)
		}
	}
	return result
}

func eventsOfType(events []translate.Event, eventType string) []translate.Event {
	var result []translate.Event
	for _, event := range events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

func onlyQuestion(t *testing.T, env *testEnv, sessionID string) session.Question {
	t.Helper()
	meta, err := env.store.Get(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Questions) == 0 {
		t.Fatal("meta.Questions 为空")
	}
	return meta.Questions[len(meta.Questions)-1]
}

func TestQuestionAnswerFlow(t *testing.T) {
	env := newQuestionEnv(t)
	result := sendBasic(t, env)
	c := collectRun(t, env.runs, result.SessionID)
	waitForEvent(t, c, "question_request")

	q := onlyQuestion(t, env, result.SessionID)
	if q.QuestionID != "q-1" || q.ToolCallID != "call-1" || q.AfterEntryID != "a-1" ||
		q.AnsweredAt != nil || q.AskedAt.IsZero() || len(q.Questions) != 2 ||
		!q.Questions[1].MultiSelect || q.Questions[0].Options[1].Description != "更快" {
		t.Fatalf("落盘的提问不符: %+v", q)
	}

	answer := func(answers ...session.QuestionAnswer) error {
		return env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
			SessionID: result.SessionID, QuestionID: "q-1", Answers: answers,
		})
	}
	sel := func(labels ...string) session.QuestionAnswer { return session.QuestionAnswer{Selected: labels} }
	other := session.OtherOptionMarker
	invalid := map[string][]session.QuestionAnswer{
		"条数不足":     {sel("A 方案")},
		"条数过多":     {sel("A 方案"), sel("中文"), sel("中文")},
		"空选择":      {sel(), sel("中文")},
		"未知标签":     {sel("C 方案"), sel("中文")},
		"重复标签":     {sel("A 方案"), sel("中文", "中文")},
		"重复其他":     {sel("A 方案"), {Selected: []string{other, other}, Other: "x"}},
		"单选多选":     {sel("A 方案", "B 方案"), sel("中文")},
		"其他为空":     {sel("A 方案"), {Selected: []string{other}, Other: "   "}},
		"其他超长":     {sel("A 方案"), {Selected: []string{other}, Other: strings.Repeat("字", 2001)}},
		"未选其他却填文字": {sel("A 方案"), {Selected: []string{"中文"}, Other: "补充"}},
	}
	for name, answers := range invalid {
		if err := answer(answers...); !errors.Is(err, ErrInvalidAnswer) {
			t.Errorf("%s: err = %v, want ErrInvalidAnswer", name, err)
		}
	}
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: " ",
	}); !errors.Is(err, ErrInvalidAnswer) {
		t.Errorf("空 question_id: err = %v", err)
	}
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: "q-404", Skipped: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("未知问题: err = %v, want ErrNotFound", err)
	}
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: "00000000-0000-0000-0000-000000000000", QuestionID: "q-1", Skipped: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("未知会话: err = %v, want ErrNotFound", err)
	}
	if responses := uiResponses(t, env.record); len(responses) != 0 {
		t.Fatalf("校验失败不应回写: %v", responses)
	}
	if q := onlyQuestion(t, env, result.SessionID); q.AnsweredAt != nil {
		t.Fatal("校验失败不应落盘回答")
	}

	// 单选 + 多选(含「其他」,2000 字边界)
	long := strings.Repeat("字", 2000)
	if err := answer(sel("B 方案"), session.QuestionAnswer{Selected: []string{"英文", other}, Other: long}); err != nil {
		t.Fatalf("合法回答: %v", err)
	}
	events := c.wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("回答后 run 应正常结束: %v", eventTypes(events))
	}
	wantValue := `{"answers":[{"selected":["B 方案"]},{"selected":["英文","__other__"],"other":"` + long + `"}]}`
	responses := uiResponses(t, env.record)
	if want := []map[string]any{{"type": "extension_ui_response", "id": "q-1", "value": wantValue}}; !reflect.DeepEqual(responses, want) {
		t.Fatalf("回写 = %v\nwant %v", responses, want)
	}
	answered := eventsOfType(events, "question_answered")
	if len(answered) != 1 || answered[0].Data["question_id"] != "q-1" || answered[0].Data["skipped"] != nil || answered[0].Data["cancelled"] != nil {
		t.Fatalf("question_answered = %+v", answered)
	}
	q = onlyQuestion(t, env, result.SessionID)
	wantAnswers := []session.QuestionAnswer{{Selected: []string{"B 方案"}}, {Selected: []string{"英文", other}, Other: long}}
	if q.AnsweredAt == nil || q.Skipped || q.Cancelled || q.Auto || !reflect.DeepEqual(q.Answers, wantAnswers) {
		t.Fatalf("回答后 meta 不符: %+v", q)
	}
	// run 已结束
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: "q-1", Skipped: true,
	}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("run 结束后: err = %v, want ErrNotRunning", err)
	}

	// 第二轮:跳过,answers 被忽略(即使非法)
	second, err := env.runner.Send(context.Background(), SendInput{SessionID: result.SessionID, Content: "第二轮", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	c = collectRun(t, env.runs, second.SessionID)
	waitForEvent(t, c, "question_request")
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: "q-2", Skipped: true, Answers: []session.QuestionAnswer{sel("不存在")},
	}); err != nil {
		t.Fatalf("跳过: %v", err)
	}
	events = c.wait(t)
	responses = uiResponses(t, env.record)
	if got := responses[len(responses)-1]; !reflect.DeepEqual(got, map[string]any{
		"type": "extension_ui_response", "id": "q-2", "value": `{"answers":[],"skipped":true}`,
	}) {
		t.Fatalf("跳过回写 = %v", got)
	}
	answered = eventsOfType(events, "question_answered")
	if len(answered) != 1 || answered[0].Data["skipped"] != true {
		t.Fatalf("跳过事件 = %+v", answered)
	}
	q = onlyQuestion(t, env, result.SessionID)
	if q.QuestionID != "q-2" || q.AfterEntryID != "a-2" || !q.Skipped || q.Cancelled || len(q.Answers) != 0 || q.AnsweredAt == nil {
		t.Fatalf("跳过后 meta 不符: %+v", q)
	}
}

// TestQuestionCancelsUnhandledUIRequests:非法 question、其他 editor、input、
// notify 都按取消回写,不发 question_request、不落盘;否则 Pi 会一直阻塞。
func TestQuestionCancelsUnhandledUIRequests(t *testing.T) {
	env := newTestEnv(t, "basic", withLocalFixture("question-cancel.jsonl"), withGrowSession())
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("终态 = %v", eventTypes(events))
	}
	if len(eventsOfType(events, "question_request")) != 0 {
		t.Fatal("非法请求不应发 question_request")
	}
	var want []map[string]any
	for _, id := range []string{"bad-1", "ed-1", "in-1", "nt-1"} {
		want = append(want, map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
	}
	if got := uiResponses(t, env.record); !reflect.DeepEqual(got, want) {
		t.Fatalf("取消回写 = %v\nwant %v", got, want)
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Questions) != 0 {
		t.Fatalf("不应落盘提问: %+v", meta.Questions)
	}
}

// TestQuestionSecondPendingIgnored:已有 pending 时再来一个 question_request,
// 不登记、不落盘(按取消回写;此处无进程,只验证忽略)。
func TestQuestionSecondPendingIgnored(t *testing.T) {
	env := newTestEnv(t, "basic")
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	ar := &activeRun{sessionID: meta.ID, tr: translate.New(), pendingQuestions: map[string]session.Question{}}
	request := func(id string) translate.Event {
		return translate.Event{Type: "question_request", Data: map[string]any{"question": map[string]any{
			"question_id": id, "tool_call_id": "call-" + id,
			"questions": []map[string]any{{
				"header": "h", "question": "q", "multiSelect": false,
				"options": []map[string]any{{"label": "A"}, {"label": "B"}},
			}},
		}}}
	}
	if !env.runner.recordQuestion(ar, request("first"), "e-1") {
		t.Fatal("第一个提问应登记")
	}
	if env.runner.recordQuestion(ar, request("second"), "e-2") {
		t.Fatal("第二个提问应被忽略")
	}
	if len(ar.pendingQuestions) != 1 || ar.pendingQuestions["first"].AfterEntryID != "e-1" {
		t.Fatalf("pending = %+v", ar.pendingQuestions)
	}
	got, err := env.store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 1 || got.Questions[0].QuestionID != "first" || got.Questions[0].AfterEntryID != "e-1" ||
		got.Questions[0].Questions[0].Options[1].Label != "B" {
		t.Fatalf("meta.Questions = %+v", got.Questions)
	}
}

func assertQuestionCancelled(t *testing.T, env *testEnv, sessionID, questionID string, events []translate.Event) {
	t.Helper()
	q := onlyQuestion(t, env, sessionID)
	if q.QuestionID != questionID || !q.Cancelled || q.Skipped || q.AnsweredAt == nil {
		t.Fatalf("提问应标记取消: %+v", q)
	}
	answered := eventsOfType(events, "question_answered")
	if len(answered) != 1 || answered[0].Data["cancelled"] != true || answered[0].Data["question_id"] != questionID {
		t.Fatalf("question_answered = %+v", answered)
	}
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: sessionID, QuestionID: questionID, Skipped: true,
	}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("取消后回答: err = %v, want ErrNotRunning", err)
	}
}

func TestQuestionStopCancelsPending(t *testing.T) {
	env := newQuestionEnv(t)
	result := sendBasic(t, env)
	c := collectRun(t, env.runs, result.SessionID)
	waitForEvent(t, c, "question_request")
	if err := env.runner.Stop(context.Background(), result.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// 假 Pi 收到取消后会立即续流到 agent_settled,终态可能抢在 abort 前是
	// agent_done;这里只断言提问被取消。
	events := c.wait(t)
	responses := uiResponses(t, env.record)
	if len(responses) == 0 || !reflect.DeepEqual(responses[0], map[string]any{
		"type": "extension_ui_response", "id": "q-1", "cancelled": true,
	}) {
		t.Fatalf("Stop 回写 = %v", responses)
	}
	assertQuestionCancelled(t, env, result.SessionID, "q-1", events)
}

// TestQuestionCleanupCancelsPending:Pi 在提问未决时崩溃,收尾把提问标记取消。
func TestQuestionCleanupCancelsPending(t *testing.T) {
	// 非 grow 模式不等回答、不展开 $N:第 5 行 editor 请求写出后立即退出。
	env := newTestEnv(t, "basic", withLocalFixture("question.jsonl"), withExitAfter(5))
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	// 收尾取消发生在终态之后,question_answered 排在 agent_error 后面。
	if len(eventsOfType(events, "agent_error")) != 1 || len(eventsOfType(events, "question_request")) != 1 {
		t.Fatalf("事件 = %v", eventTypes(events))
	}
	assertQuestionCancelled(t, env, result.SessionID, "q-$N", events)
}

// TestQuestionToolAndPromptForConversationRuns:对话类 run(含编辑重发的 retry)的
// --tools 与系统提示含 question;只有 compact 不含。
func TestQuestionToolAndPromptForConversationRuns(t *testing.T) {
	env := newTestEnv(t, "basic")
	promptPath := func(sessionID string) string {
		return filepath.Join(env.store.Dir(sessionID), "pi-agent", "system-prompt.md")
	}
	check := func(stage, sessionID string, want bool) {
		t.Helper()
		tools := strings.Split(flagArg(lastStartArgs(t, env.record), "--tools"), ",")
		if slices.Contains(tools, "question") != want {
			t.Errorf("%s: --tools = %v, want question=%v", stage, tools, want)
		}
		prompt, err := os.ReadFile(promptPath(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(prompt), "question 工具") != want {
			t.Errorf("%s: 系统提示 question 说明 = %v, want %v", stage, !want, want)
		}
	}
	first := sendBasic(t, env)
	collectRun(t, env.runs, first.SessionID).wait(t)
	check("send", first.SessionID, true)

	if _, err := env.runner.Retry(context.Background(), RetryInput{
		SessionID: first.SessionID, MessageID: first.MessageID, Content: "重发", Model: "mock-1",
	}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	collectRun(t, env.runs, first.SessionID).wait(t)
	check("retry", first.SessionID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := env.runner.Compact(ctx, first.SessionID, "mock-1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	check("compact", first.SessionID, false)
}

func withQuestionTimeout(timeout time.Duration) envOption {
	return func(cfg *Config, _ *[]string) { cfg.QuestionTimeout = timeout }
}

// TestQuestionTimeoutAutoSkips:无人回答超时后自动按跳过回写,事件与 meta 带 auto。
func TestQuestionTimeoutAutoSkips(t *testing.T) {
	env := newQuestionEnv(t, withQuestionTimeout(80*time.Millisecond))
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	answered := eventsOfType(events, "question_answered")
	if len(answered) != 1 || answered[0].Data["skipped"] != true || answered[0].Data["auto"] != true {
		t.Fatalf("question_answered = %+v", answered)
	}
	responses := uiResponses(t, env.record)
	if len(responses) != 1 || responses[0]["value"] != `{"answers":[],"skipped":true}` {
		t.Fatalf("回写 = %v", responses)
	}
	q := onlyQuestion(t, env, result.SessionID)
	if !q.Skipped || !q.Auto || q.Cancelled || q.AnsweredAt == nil {
		t.Fatalf("meta = %+v", q)
	}
	// 已自动跳过后再回答:问题已不在 pending
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: "q-1", Skipped: true,
	}); !errors.Is(err, ErrNotRunning) && !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

// TestQuestionRunTimeoutPausedWhileWaiting:等待提问期间运行总时限暂停,
// 回答后 run 正常结束,而不是被超时中止。
func TestQuestionRunTimeoutPausedWhileWaiting(t *testing.T) {
	env := newQuestionEnv(t, withRunTimeout(300*time.Millisecond))
	result := sendBasic(t, env)
	c := collectRun(t, env.runs, result.SessionID)
	waitForEvent(t, c, "question_request")
	time.Sleep(700 * time.Millisecond)
	if err := env.runner.AnswerQuestion(context.Background(), AnswerQuestionInput{
		SessionID: result.SessionID, QuestionID: "q-1", Skipped: true,
	}); err != nil {
		t.Fatalf("等待超过总时限后仍应可回答: %v", err)
	}
	events := c.wait(t)
	if len(eventsOfType(events, "agent_error")) != 0 || events[len(events)-1].Type != "agent_done" {
		t.Fatalf("事件 = %v", eventTypes(events))
	}
}

// TestApprovalRunTimeoutPausedWhileWaiting:审批等待同样不消耗运行总时限。
func TestApprovalRunTimeoutPausedWhileWaiting(t *testing.T) {
	env := newTestEnv(t, "approval-allow", withDelay(5), withRunTimeout(300*time.Millisecond))
	result := sendBasic(t, env)
	c := collectRun(t, env.runs, result.SessionID)
	escalation := waitForEvent(t, c, "escalation_request")
	id := escalation.Data["escalation"].(map[string]any)["escalation_id"].(string)
	time.Sleep(700 * time.Millisecond)
	if err := env.runner.Grant(context.Background(), result.SessionID, GrantInput{EscalationID: id, Granted: true}); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	events := c.wait(t)
	if len(eventsOfType(events, "agent_error")) != 0 || len(eventsOfType(events, "agent_done")) != 1 {
		t.Fatalf("事件 = %v", eventTypes(events))
	}
}
