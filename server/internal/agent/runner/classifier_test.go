package runner

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

type capturedClassifierCall struct {
	sessionID string
	model     string
	system    string
	user      string
	maxTokens int
}

func withClassifier(classify func(context.Context, string, string, string, string, int) (string, error)) envOption {
	return func(cfg *Config, _ *[]string) {
		cfg.Classify = classify
	}
}

func sendAutoApproval(t *testing.T, env *testEnv) (SendResult, *collector) {
	t.Helper()
	result, err := env.runner.Send(context.Background(), SendInput{
		Content: "请修改工作区文件", Model: "mock-1", PermissionMode: "auto",
	})
	if err != nil {
		t.Fatalf("Send(auto): %v", err)
	}
	return result, collectRun(t, env.runs, result.SessionID)
}

func latestClassifierEscalation(meta session.Meta, id string) (session.Escalation, bool) {
	for i := len(meta.Escalations) - 1; i >= 0; i-- {
		if meta.Escalations[i].EscalationID == id {
			return meta.Escalations[i], true
		}
	}
	return session.Escalation{}, false
}

func waitForStoredEscalation(t *testing.T, env *testEnv, sessionID, id string) session.Escalation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		meta, err := env.store.Get(sessionID)
		if err == nil {
			if escalation, ok := latestClassifierEscalation(meta, id); ok {
				return escalation
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待审批 %s 落盘超时", id)
	return session.Escalation{}
}

func grantClassifierEscalation(t *testing.T, env *testEnv, sessionID, id string, granted bool) {
	t.Helper()
	if err := env.runner.Grant(context.Background(), sessionID, GrantInput{
		EscalationID: id, Granted: granted,
	}); err != nil {
		t.Fatalf("Grant(%s): %v", id, err)
	}
}

func assertClassifierEventOrder(t *testing.T, events []translate.Event, id string) {
	t.Helper()
	requestIndex, resultIndex := -1, -1
	for i, event := range events {
		if event.Type == "escalation_request" {
			data, _ := event.Data["escalation"].(map[string]any)
			if data["escalation_id"] == id {
				requestIndex = i
			}
		}
		if event.Type == "permission_result" && event.Data["request_id"] == id {
			resultIndex = i
		}
	}
	if requestIndex < 0 || resultIndex < 0 || requestIndex > resultIndex {
		t.Fatalf("审批 %s 事件顺序应为请求→结果: request=%d result=%d, events=%v", id, requestIndex, resultIndex, eventTypes(events))
	}
}

func hasClassifierRequest(events []translate.Event, id string) bool {
	for _, event := range events {
		if event.Type != "escalation_request" {
			continue
		}
		data, _ := event.Data["escalation"].(map[string]any)
		if data["escalation_id"] == id {
			return true
		}
	}
	return false
}

func TestClassifierAllowAutoGrantsWithoutSessionAllow(t *testing.T) {
	calls := make(chan capturedClassifierCall, classifierMaxAttempts)
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(_ context.Context, sessionID, model, system, user string, maxTokens int) (string, error) {
			calls <- capturedClassifierCall{sessionID: sessionID, model: model, system: system, user: user, maxTokens: maxTokens}
			return "前置说明\n```json\n{\"decision\":\"allow\",\"reason\":\"仅修改工作区文件\"}\n```\n后置说明", nil
		}), func(cfg *Config, _ *[]string) {
			cfg.ClassifierModel = func() string { return "classifier-config" }
		})
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)

	var call capturedClassifierCall
	select {
	case call = <-calls:
	case <-time.After(time.Second):
		t.Fatal("分类器未被调用")
	}
	if call.sessionID != result.SessionID || call.model != "classifier-config" || call.maxTokens != classifierMaxTokens {
		t.Fatalf("分类调用参数不符: %+v", call)
	}
	if !strings.Contains(call.system, "安全审批员") || !strings.Contains(call.system, "git push --force") {
		t.Fatalf("分类器系统提示缺少安全规则: %q", call.system)
	}
	for _, want := range []string{"工具名: write", "请求路径: a.txt", "备注: 写入 a.txt"} {
		if !strings.Contains(call.user, want) {
			t.Errorf("分类器用户提示缺少 %q: %s", want, call.user)
		}
	}
	if strings.Contains(call.user, "第 1 轮动手") || strings.Contains(call.user, "已写入") {
		t.Fatalf("分类器用户提示混入助手文本或工具结果: %s", call.user)
	}

	assertClassifierEventOrder(t, events, "esc-1")
	decisions := decisionEvents(events, "esc-1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != true ||
		decisions[0].Data["reason"] != "仅修改工作区文件" {
		t.Fatalf("自动放行事件不符: %+v", decisions)
	}
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || !*escalation.Granted || !escalation.Auto || escalation.Scope != "" ||
		escalation.ClassifierReason != "仅修改工作区文件" || escalation.Reviewing {
		t.Fatalf("自动放行审批记录不符: %+v", escalation)
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SessionAllow) != 0 {
		t.Fatalf("分类器放行不应写 SessionAllow: %v", meta.SessionAllow)
	}
	if !hasUIResponse(recordedCommands(t, env.record), "esc-1", true) {
		t.Fatal("分类器放行未回写 Pi")
	}
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundReason := false
	for _, entry := range entries {
		if entry["type"] == "escalation" && entry["escalation_id"] == "esc-1" && entry["reason"] == "仅修改工作区文件" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatal("transcript 审批条目缺少分类器 reason")
	}
}

func TestClassifierFallsBackToRunModel(t *testing.T) {
	models := make(chan string, 1)
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(_ context.Context, _, model, _, _ string, _ int) (string, error) {
			models <- model
			return `{"decision":"allow","reason":"本地修改"}`, nil
		}), func(cfg *Config, _ *[]string) {
			cfg.ClassifierModel = func() string { return "  " }
		})
	_, collected := sendAutoApproval(t, env)
	collected.wait(t)
	select {
	case model := <-models:
		if model != "mock-1" {
			t.Fatalf("分类模型 = %q, want 本次 run 模型 mock-1", model)
		}
	case <-time.After(time.Second):
		t.Fatal("分类器未被调用")
	}
}

func TestClassifierBlockAutoDeniesWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			return `{"decision":"block","reason":"操作范围不明确"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got != 1 {
		t.Fatalf("block 不应重试,分类调用次数 = %d", got)
	}
	assertClassifierEventOrder(t, events, "esc-1")
	decisions := decisionEvents(events, "esc-1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != false || decisions[0].Data["auto"] != true ||
		decisions[0].Data["reason"] != "操作范围不明确" {
		t.Fatalf("自动拒绝事件不符: %+v", decisions)
	}
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || *escalation.Granted || !escalation.Auto ||
		escalation.ClassifierReason != "操作范围不明确" || escalation.Reviewing {
		t.Fatalf("block 审批记录不符: %+v", escalation)
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SessionAllow) != 0 {
		t.Fatalf("拒绝不应写 SessionAllow: %v", meta.SessionAllow)
	}
	if !hasUIResponse(recordedCommands(t, env.record), "esc-1", false) {
		t.Fatal("分类器拒绝未回写 Pi")
	}
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry["type"] == "escalation" && entry["escalation_id"] == "esc-1" {
			found = true
			if entry["granted"] != false || entry["auto"] != true || entry["reason"] != "操作范围不明确" {
				t.Fatalf("transcript 拒绝审批条目不符: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("transcript 缺少已拒绝的审批条目")
	}
}

func TestClassifierRetriesFailuresThenAllows(t *testing.T) {
	oldDelay := classifierRetryDelay
	classifierRetryDelay = time.Millisecond
	t.Cleanup(func() { classifierRetryDelay = oldDelay })
	var calls atomic.Int32
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			if calls.Add(1) <= 2 {
				return "", errors.New("上游暂时不可用")
			}
			return `{"decision":"allow","reason":"第三次判断可放行"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got != classifierMaxAttempts {
		t.Fatalf("分类调用次数 = %d, want %d", got, classifierMaxAttempts)
	}
	decisions := decisionEvents(events, "esc-1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != true {
		t.Fatalf("第三次成功后应自动放行: %+v", decisions)
	}
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || !*escalation.Granted || escalation.ClassifierReason != "第三次判断可放行" {
		t.Fatalf("重试成功后的审批记录不符: %+v", escalation)
	}
}

func TestClassifierFailuresAutoDenyAfterThreeAttempts(t *testing.T) {
	tests := []struct {
		name       string
		classify   func(context.Context, string, string, string, string, int) (string, error)
		wantReason string
	}{
		{name: "调用报错", wantReason: "上游不可用", classify: func(context.Context, string, string, string, string, int) (string, error) {
			return "", errors.New("上游不可用")
		}},
		{name: "超时", wantReason: "分类器超时", classify: func(ctx context.Context, _ string, _ string, _ string, _ string, _ int) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}},
		{name: "解析失败", wantReason: "分类器返回结果无法解析", classify: func(context.Context, string, string, string, string, int) (string, error) {
			return "不是 JSON", nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldDelay, oldTimeout := classifierRetryDelay, classifierTimeout
			classifierRetryDelay = time.Millisecond
			classifierTimeout = 20 * time.Millisecond
			t.Cleanup(func() {
				classifierRetryDelay = oldDelay
				classifierTimeout = oldTimeout
			})
			var calls atomic.Int32
			env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
				withClassifier(func(ctx context.Context, sessionID, model, system, user string, maxTokens int) (string, error) {
					calls.Add(1)
					return tt.classify(ctx, sessionID, model, system, user, maxTokens)
				}))
			result, collected := sendAutoApproval(t, env)
			events := collected.wait(t)
			if got := calls.Load(); got != classifierMaxAttempts {
				t.Fatalf("失败后分类调用次数 = %d, want %d", got, classifierMaxAttempts)
			}
			assertClassifierEventOrder(t, events, "esc-1")
			decisions := decisionEvents(events, "esc-1")
			if len(decisions) != 1 || decisions[0].Data["allowed"] != false || decisions[0].Data["auto"] != true {
				t.Fatalf("三次失败后应自动拒绝: %+v", decisions)
			}
			reason, _ := decisions[0].Data["reason"].(string)
			if !strings.HasPrefix(reason, "审批模型调用失败(已重试 2 次):") || !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("失败 reason = %q, want 包含重试次数与 %q", reason, tt.wantReason)
			}
			escalation := metaEscalation(t, env, result.SessionID, "esc-1")
			if escalation.Granted == nil || *escalation.Granted || !escalation.Auto || escalation.Reviewing || escalation.ClassifierReason != reason {
				t.Fatalf("失败拒绝的审批记录不符: %+v", escalation)
			}
		})
	}
}

func TestClassifierRunEndDuringRetryPublishesNoApprovalEvents(t *testing.T) {
	oldDelay := classifierRetryDelay
	classifierRetryDelay = 500 * time.Millisecond
	t.Cleanup(func() { classifierRetryDelay = oldDelay })
	called := make(chan struct{}, 1)
	var calls atomic.Int32
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			called <- struct{}{}
			return "", errors.New("上游不可用")
		}))
	result, collected := sendAutoApproval(t, env)
	waitForStoredEscalation(t, env, result.SessionID, "esc-1")
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("分类器未开始调用")
	}
	if err := env.runner.Stop(context.Background(), result.SessionID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	events := collected.wait(t)
	if hasClassifierRequest(events, "esc-1") || len(decisionEvents(events, "esc-1")) != 0 {
		t.Fatalf("重试期间结束 run 不应补发审批事件: %v", eventTypes(events))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("run 结束后仍重试,分类调用次数 = %d", got)
	}
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || *escalation.Granted || escalation.Reviewing {
		t.Fatalf("run 结束应静默拒绝并清除 Reviewing: %+v", escalation)
	}
}

func TestClassifierHighRiskBashAutoDeniesWithoutModel(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "approval-bash-highrisk", withLocalFixture("approval-bash-highrisk.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			return `{"decision":"allow"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got != 0 {
		t.Fatalf("高危 bash 不应调用分类器,调用次数 = %d", got)
	}
	const reason = "命中高危命令规则(rm 递归删除高危目标)，『替我审批』不放行"
	decisions := decisionEvents(events, "esc-hr")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != false || decisions[0].Data["auto"] != true || decisions[0].Data["reason"] != reason {
		t.Fatalf("高危 bash 应直接自动拒绝: %+v", decisions)
	}
	assertClassifierEventOrder(t, events, "esc-hr")
	escalation := metaEscalation(t, env, result.SessionID, "esc-hr")
	if escalation.Granted == nil || *escalation.Granted || escalation.ClassifierReason != reason || escalation.Reviewing {
		t.Fatalf("高危 bash 审批记录不符: %+v", escalation)
	}
}

func TestClassifierUnavailableAutoDenies(t *testing.T) {
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession())
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	decisions := decisionEvents(events, "esc-1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != false || decisions[0].Data["auto"] != true || decisions[0].Data["reason"] != "审批模型不可用" {
		t.Fatalf("缺少分类依赖应直接自动拒绝: %+v", decisions)
	}
	assertClassifierEventOrder(t, events, "esc-1")
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || *escalation.Granted || !escalation.Auto || escalation.ClassifierReason != "审批模型不可用" {
		t.Fatalf("缺少分类依赖的审批记录不符: %+v", escalation)
	}
}

func TestClassifierDoesNotChangeAskAndFullModes(t *testing.T) {
	for _, mode := range []string{"ask", "full"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
				withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
					calls.Add(1)
					return `{"decision":"allow"}`, nil
				}))
			result, err := env.runner.Send(context.Background(), SendInput{
				Content: "请修改文件", Model: "mock-1", PermissionMode: mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			collected := collectRun(t, env.runs, result.SessionID)
			findEscalation(t, collected, "esc-1")
			escalation := waitForStoredEscalation(t, env, result.SessionID, "esc-1")
			if escalation.Reviewing {
				t.Fatalf("%s 模式不应标记 Reviewing: %+v", mode, escalation)
			}
			time.Sleep(50 * time.Millisecond)
			if calls.Load() != 0 {
				t.Fatalf("%s 模式不应调用分类器", mode)
			}
			if decisions := decisionEvents(collected.snapshot(), "esc-1"); len(decisions) != 0 {
				t.Fatalf("%s 模式仍应等待用户审批: %+v", mode, decisions)
			}
			grantClassifierEscalation(t, env, result.SessionID, "esc-1", true)
			events := collected.wait(t)
			decisions := decisionEvents(events, "esc-1")
			if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != nil {
				t.Fatalf("%s 模式用户审批行为改变: %+v", mode, decisions)
			}
		})
	}
}

func TestClassifierReviewingHiddenFromTranscriptAndNotWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(ctx context.Context, _ string, _ string, _ string, _ string, _ int) (string, error) {
			close(started)
			select {
			case <-release:
				return `{"decision":"allow","reason":"分类完成"}`, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}))
	result, collected := sendAutoApproval(t, env)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("分类器未开始调用")
	}
	escalation := waitForStoredEscalation(t, env, result.SessionID, "esc-1")
	if !escalation.Reviewing || escalation.Granted != nil {
		t.Fatalf("分类期间审批应为 Reviewing 未决状态: %+v", escalation)
	}
	if hasClassifierRequest(collected.snapshot(), "esc-1") {
		t.Fatal("分类完成前不应发布 escalation_request")
	}
	entries, err := env.store.BuildTranscript(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry["type"] == "escalation" && entry["escalation_id"] == "esc-1" {
			t.Fatalf("分类期间 transcript 不应包含审批: %+v", entry)
		}
	}
	env.runner.mu.Lock()
	ar := env.runner.active[result.SessionID]
	env.runner.mu.Unlock()
	if ar == nil {
		t.Fatal("分类期间 activeRun 不存在")
	}
	if ar.waiting() {
		t.Fatal("auto 分类不应计为等待用户")
	}

	close(release)
	released = true
	events := collected.wait(t)
	assertClassifierEventOrder(t, events, "esc-1")
	escalation = metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Reviewing || escalation.Granted == nil || !*escalation.Granted {
		t.Fatalf("分类完成后应清除 Reviewing 并写入决定: %+v", escalation)
	}
}

func TestClassifierAutoDenyStreakAbortsRun(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "classifier-deny-streak", withLocalFixture("classifier-deny-streak.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			return `{"decision":"block","reason":"未获明确授权:测试拒绝"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got < 3 {
		t.Fatalf("连续拒绝熔断前的分类调用次数 = %d, want >= 3", got)
	}
	const message = "『替我审批』连续被拒 3 次，已中止本次运行。请调整任务，或改用『询问』模式后重试。"
	errors := eventsOfType(events, "agent_error")
	if len(errors) != 1 || errors[0].Data["message"] != message {
		t.Fatalf("连续拒绝熔断事件不符: %+v", errors)
	}
	for i := 1; i <= 3; i++ {
		id := "esc-d" + string(rune('0'+i))
		if decisions := decisionEvents(events, id); len(decisions) != 1 || decisions[0].Data["allowed"] != false {
			t.Fatalf("触发前的第 %d 次拒绝未照常发布: %+v", i, decisions)
		}
	}
	if decisions := decisionEvents(events, "esc-d4"); len(decisions) != 0 {
		t.Fatalf("中止后不应继续审批: %+v", decisions)
	}
	aborted := false
	for _, command := range recordedCommands(t, env.record) {
		if command["type"] == "abort" {
			aborted = true
		}
	}
	if !aborted {
		t.Fatal("连续拒绝熔断未中止 Pi")
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 1 || meta.RunErrors[0].Message != message {
		t.Fatalf("连续拒绝熔断未写入 RunErrors: %+v", meta.RunErrors)
	}
}

func TestClassifierAutoAllowResetsDenyStreak(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "classifier-deny-streak", withLocalFixture("classifier-deny-streak.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			if calls.Add(1) == 3 {
				return `{"decision":"allow","reason":"本次明确授权"}`, nil
			}
			return `{"decision":"block","reason":"未获明确授权:测试拒绝"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got != 5 {
		t.Fatalf("放行应清零连续拒绝,后续两次拒绝不应中止;分类调用次数 = %d", got)
	}
	if errors := eventsOfType(events, "agent_error"); len(errors) != 0 {
		t.Fatalf("放行清零后不应熔断: %+v", errors)
	}
	if done := eventsOfType(events, "agent_done"); len(done) != 1 {
		t.Fatalf("放行清零后 run 应正常完成: %v", eventTypes(events))
	}
	for i := 1; i <= 5; i++ {
		id := "esc-d" + string(rune('0'+i))
		if decisions := decisionEvents(events, id); len(decisions) != 1 {
			t.Fatalf("第 %d 次审批未完整发布: %+v", i, decisions)
		}
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 0 {
		t.Fatalf("正常完成不应记录 RunErrors: %+v", meta.RunErrors)
	}
}

func TestClassifierAutoDenyTotalAbortsRun(t *testing.T) {
	oldStreak, oldTotal := autoDenyStreakLimit, autoDenyTotalLimit
	autoDenyStreakLimit, autoDenyTotalLimit = 100, 2
	t.Cleanup(func() {
		autoDenyStreakLimit, autoDenyTotalLimit = oldStreak, oldTotal
	})
	var calls atomic.Int32
	env := newTestEnv(t, "classifier-deny-streak", withLocalFixture("classifier-deny-streak.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			return `{"decision":"block","reason":"未获明确授权:测试拒绝"}`, nil
		}))
	result, collected := sendAutoApproval(t, env)
	events := collected.wait(t)
	if got := calls.Load(); got < 2 {
		t.Fatalf("累计拒绝熔断前的分类调用次数 = %d, want >= 2", got)
	}
	const message = "『替我审批』累计被拒 2 次，已中止本次运行。请调整任务，或改用『询问』模式后重试。"
	errors := eventsOfType(events, "agent_error")
	if len(errors) != 1 || errors[0].Data["message"] != message {
		t.Fatalf("累计拒绝熔断事件不符: %+v", errors)
	}
	if decisions := decisionEvents(events, "esc-d3"); len(decisions) != 0 {
		t.Fatalf("累计熔断后不应继续审批: %+v", decisions)
	}
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.RunErrors) != 1 || meta.RunErrors[0].Message != message {
		t.Fatalf("累计拒绝熔断未写入 RunErrors: %+v", meta.RunErrors)
	}
}

func TestClassifierAutoDenyLimitsDoNotAffectAskAndFull(t *testing.T) {
	for _, mode := range []string{"ask", "full"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			env := newTestEnv(t, "classifier-deny-streak", withLocalFixture("classifier-deny-streak.jsonl"), withGrowSession(),
				withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
					calls.Add(1)
					return `{"decision":"block","reason":"不应调用"}`, nil
				}))
			result, err := env.runner.Send(context.Background(), SendInput{
				Content: "逐条拒绝", Model: "mock-1", PermissionMode: mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			collected := collectRun(t, env.runs, result.SessionID)
			for i := 1; i <= 5; i++ {
				id := "esc-d" + string(rune('0'+i))
				findEscalation(t, collected, id)
				grantClassifierEscalation(t, env, result.SessionID, id, false)
			}
			events := collected.wait(t)
			if calls.Load() != 0 {
				t.Fatalf("%s 模式不应调用分类器", mode)
			}
			if errors := eventsOfType(events, "agent_error"); len(errors) != 0 {
				t.Fatalf("%s 模式的用户拒绝不应触发 auto 熔断: %+v", mode, errors)
			}
			if done := eventsOfType(events, "agent_done"); len(done) != 1 {
				t.Fatalf("%s 模式连续用户拒绝后仍应正常完成: %v", mode, eventTypes(events))
			}
		})
	}
}

func TestClassifierSessionAllowRunsBeforeModel(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "approval-anchor", withLocalFixture("approval-anchor.jsonl"), withGrowSession(),
		withClassifier(func(context.Context, string, string, string, string, int) (string, error) {
			calls.Add(1)
			return `{"decision":"block","reason":"不应调用"}`, nil
		}))
	meta, err := env.store.Create("mock-1", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Update(meta.ID, func(m *session.Meta) error {
		m.SessionAllow = []string{"write"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := env.runner.Send(context.Background(), SendInput{
		SessionID: meta.ID, Content: "请修改文件", Model: "mock-1", PermissionMode: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	collected := collectRun(t, env.runs, result.SessionID)
	events := collected.wait(t)
	if calls.Load() != 0 {
		t.Fatal("会话内允许命中后不应调用分类器")
	}
	assertClassifierEventOrder(t, events, "esc-1")
	decisions := decisionEvents(events, "esc-1")
	if len(decisions) != 1 || decisions[0].Data["allowed"] != true || decisions[0].Data["auto"] != true {
		t.Fatalf("会话内允许应照旧自动放行: %+v", decisions)
	}
	escalation := metaEscalation(t, env, result.SessionID, "esc-1")
	if escalation.Granted == nil || !*escalation.Granted || escalation.Scope != "session" || escalation.Reviewing {
		t.Fatalf("会话内允许审批记录不符: %+v", escalation)
	}
}
