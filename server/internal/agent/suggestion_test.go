package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/agent/session"
)

func TestFilterSuggestion(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		reason string
	}{
		{name: "去除空白", input: "  继续  ", want: "继续"},
		{name: "去除suggestion包装", input: "<suggestion>继续</suggestion>", want: "继续"},
		{name: "去除response包装和引号", input: "<response>“提交吧”</response>", want: "提交吧"},
		{name: "去除output包装", input: "<output>部署</output>", want: "部署"},
		{name: "去除answer包装", input: "<answer>可以</answer>", want: "可以"},
		{name: "去除result包装", input: "<result>推送</result>", want: "推送"},
		{name: "去除中文前缀", input: "建议：/compact", want: "/compact"},
		{name: "去除英文前缀", input: "Reply: commit", want: "commit"},
		{name: "好的放行", input: "好的", want: "好的"},
		{name: "可以放行", input: "可以", want: "可以"},
		{name: "英文白名单忽略大小写", input: "OK", want: "OK"},
		{name: "单斜杠放行", input: "/", want: "/"},
		{name: "正常问题", input: "为什么会报这个错？", want: "为什么会报这个错？"},
		{name: "空", input: "  ", reason: "empty"},
		{name: "done", input: "DONE!!!", reason: "done"},
		{name: "已完成", input: "已完成。", reason: "done"},
		{name: "英文元话语", input: "No suggestion available", reason: "meta_text"},
		{name: "中文元话语", input: "无", reason: "meta_text"},
		{name: "括号元话语", input: "（继续）", reason: "meta_wrapped"},
		{name: "方括号元话语", input: "【继续】", reason: "meta_wrapped"},
		{name: "错误消息", input: "API Error: quota exceeded", reason: "error_message"},
		{name: "未识别前缀", input: "Note: 继续", reason: "prefixed_label"},
		{name: "过短", input: "是", reason: "too_short"},
		{name: "字符过多", input: strings.Repeat("很", 41), reason: "too_long"},
		{name: "英文单词过多", input: "a b c d e f g h i j k l m", reason: "too_long"},
		{name: "多句", input: "继续。然后提交", reason: "multiple_sentences"},
		{name: "英文多句", input: "Fix it. Then commit", reason: "multiple_sentences"},
		{name: "文件名里的点号放行", input: "把 main.go 也加上测试", want: "把 main.go 也加上测试"},
		{name: "版本号放行", input: "升级到 v1.2.3", want: "升级到 v1.2.3"},
		{name: "换行", input: "继续\n提交", reason: "has_formatting"},
		{name: "代码标记", input: "`继续`", reason: "has_formatting"},
		{name: "标题", input: "# 继续", reason: "has_formatting"},
		{name: "评价", input: "谢谢，太棒了", reason: "evaluative"},
		{name: "英文评价", input: "Looks good to me", reason: "evaluative"},
		{name: "中文助手口吻", input: "好的，我来看看", reason: "assistant_voice"},
		{name: "中文助手承诺", input: "我可以继续处理", reason: "assistant_voice"},
		{name: "英文助手口吻", input: "Let me check", reason: "assistant_voice"},
		{name: "英文助手附和", input: "Sure, continue", reason: "assistant_voice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := filterSuggestion(tt.input)
			_, reason := filterSuggestionReason(tt.input)
			if reason != tt.reason {
				t.Fatalf("reason = %q, want %q", reason, tt.reason)
			}
			if tt.reason != "" {
				if ok || got != "" {
					t.Fatalf("filterSuggestion = %q, %v, want reject", got, ok)
				}
				return
			}
			if !ok || got != tt.want {
				t.Fatalf("filterSuggestion = %q, %v, want %q, true", got, ok, tt.want)
			}
		})
	}
}

func TestRenderSuggestionContextTruncatesAndUsesLastParagraph(t *testing.T) {
	longUser := strings.Repeat("用", 600)
	longAssistant := strings.Repeat("助", 1600)
	entries := []map[string]any{
		suggestionTestMessage("user", longUser),
		suggestionTestMessage("assistant", "旧段落\n\n"+longAssistant),
	}
	got := renderSuggestionContext(entries)
	if !strings.Contains(got, "用户:"+strings.Repeat("用", 500)+"\n") || strings.Contains(got, strings.Repeat("用", 501)) {
		t.Fatal("用户消息未按 500 字截断")
	}
	if !strings.Contains(got, "助手:"+strings.Repeat("助", 1500)+"\n") || strings.Contains(got, strings.Repeat("助", 1501)) || strings.Contains(got, "旧段落") {
		t.Fatal("助手消息应取最后一段并按 1500 字截断")
	}
	if !strings.HasSuffix(got, suggestionPromptSuffix) {
		t.Fatalf("提示词缺少结尾: %q", got)
	}
}

func TestRenderSuggestionContextKeepsLastThreeTurnsAndToolNames(t *testing.T) {
	var entries []map[string]any
	for _, turn := range []string{"一", "二", "三", "四"} {
		entries = append(entries,
			suggestionTestMessage("user", "第"+turn+"轮"),
			suggestionTestMessage("assistant", "回答"+turn),
		)
	}
	assistant := suggestionTestMessage("assistant", "")
	assistant["message"].(map[string]any)["tool_calls"] = []map[string]any{{"name": "read"}}
	entries = append(entries, assistant)
	entries = append(entries, suggestionTestMessage("tool", "不应包含的工具输出"))
	entries = append(entries, map[string]any{"type": "unknown", "content": "跳过"})
	got := renderSuggestionContext(entries)
	if strings.Contains(got, "第一轮") || strings.Contains(got, "回答一") {
		t.Fatalf("只应保留最近三轮: %q", got)
	}
	for _, turn := range []string{"二", "三", "四"} {
		if !strings.Contains(got, "第"+turn+"轮") || !strings.Contains(got, "回答"+turn) {
			t.Fatalf("缺少第%s轮: %q", turn, got)
		}
	}
	if !strings.Contains(got, "[调用工具 read]") || strings.Contains(got, "工具输出") || strings.Contains(got, "跳过") {
		t.Fatalf("工具调用或未知条目渲染错误: %q", got)
	}
	if got := renderSuggestionContext(nil); got != "" {
		t.Fatalf("空 transcript = %q", got)
	}
}

func suggestionTestMessage(role, content string) map[string]any {
	return map[string]any{"type": "message", "message": map[string]any{"role": role, "content": content}}
}

func TestSuggestionReturnsFilteredPrediction(t *testing.T) {
	var gotSessionID, gotModel, gotSystem, gotUser string
	var gotMaxTokens int
	handler, store, _ := newTestHandler(t, &fakeRunner{}, func(deps *Deps) {
		deps.SuggestionEnabled = func() bool { return true }
		deps.FastModel = func() string { return "" }
		deps.Suggest = func(ctx context.Context, sessionID, model, system, user string, maxTokens int) (string, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("模型调用应带整体超时")
			}
			gotSessionID, gotModel, gotSystem, gotUser, gotMaxTokens = sessionID, model, system, user, maxTokens
			return "建议：继续", nil
		}
	})
	meta := newSuggestionSession(t, store, "gpt-test")
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion == nil || *body.Data.Suggestion != "继续" {
		t.Fatalf("suggestion = %v", body.Data.Suggestion)
	}
	if _, err := uuid.Parse(body.Data.RequestID); err != nil {
		t.Fatalf("request_id = %q", body.Data.RequestID)
	}
	if gotSessionID != meta.ID || gotModel != "gpt-test" || gotSystem != suggestionSystemPrompt || gotMaxTokens != suggestionMaxTokens {
		t.Fatalf("Suggest 参数 = %q %q %q %d", gotSessionID, gotModel, gotSystem, gotMaxTokens)
	}
	if !strings.Contains(gotUser, "用户:修复这个报错") || !strings.Contains(gotUser, "助手:已经修复") || !strings.HasSuffix(gotUser, suggestionPromptSuffix) {
		t.Fatalf("user prompt = %q", gotUser)
	}
}

func TestResolveSuggestionModel(t *testing.T) {
	for _, tt := range []struct {
		name         string
		sessionModel string
		fastModel    string
		want         string
	}{
		{name: "均未配置"},
		{name: "使用会话模型", sessionModel: " gpt-test ", want: "gpt-test"},
		{name: "快速模型为空白", sessionModel: "gpt-test", fastModel: "  ", want: "gpt-test"},
		{name: "优先快速模型", sessionModel: "gpt-test", fastModel: " fast-test ", want: "fast-test"},
		{name: "只有快速模型", fastModel: "fast-test", want: "fast-test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSuggestionModel(tt.sessionModel, tt.fastModel); got != tt.want {
				t.Fatalf("resolveSuggestionModel(%q, %q) = %q, want %q", tt.sessionModel, tt.fastModel, got, tt.want)
			}
		})
	}
}

func TestSuggestionUsesFastModel(t *testing.T) {
	var gotModels []string
	handler, store, _ := newTestHandler(t, &fakeRunner{}, func(deps *Deps) {
		deps.SuggestionEnabled = func() bool { return true }
		deps.FastModel = func() string { return " fast-test " }
		deps.Suggest = func(_ context.Context, _, model, _, _ string, _ int) (string, error) {
			gotModels = append(gotModels, model)
			return "继续", nil
		}
	})
	meta := newSuggestionSession(t, store, "gpt-test")
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion == nil || *body.Data.Suggestion != "继续" {
		t.Fatalf("suggestion = %v", body.Data.Suggestion)
	}
	if got := strings.Join(gotModels, ","); got != "fast-test" {
		t.Fatalf("Suggest models = %q, want fast-test", got)
	}
}

func TestSuggestionFastModelFallback(t *testing.T) {
	for _, tt := range []struct {
		name           string
		fastModel      string
		firstErr       error
		secondText     string
		secondErr      error
		wantModels     string
		wantSuggestion string
	}{
		{name: "快速模型报错后回退成功", fastModel: "fast-test", firstErr: errors.New("upstream failed"), secondText: "继续", wantModels: "fast-test,gpt-test", wantSuggestion: "继续"},
		{name: "回退也失败", fastModel: "fast-test", firstErr: errors.New("upstream failed"), secondErr: errors.New("session failed"), wantModels: "fast-test,gpt-test"},
		{name: "快速模型超时不重试", fastModel: "fast-test", firstErr: context.DeadlineExceeded, wantModels: "fast-test"},
		{name: "上下文取消不重试", fastModel: "fast-test", firstErr: context.Canceled, wantModels: "fast-test"},
		{name: "模型相同不重复调用", fastModel: " gpt-test ", firstErr: errors.New("upstream failed"), wantModels: "gpt-test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotModels []string
			var contexts []context.Context
			handler, store, _ := newTestHandler(t, &fakeRunner{}, func(deps *Deps) {
				deps.SuggestionEnabled = func() bool { return true }
				deps.FastModel = func() string { return tt.fastModel }
				deps.Suggest = func(ctx context.Context, _, model, _, _ string, _ int) (string, error) {
					contexts = append(contexts, ctx)
					gotModels = append(gotModels, model)
					if len(gotModels) == 1 {
						return "", tt.firstErr
					}
					return tt.secondText, tt.secondErr
				}
			})
			meta := newSuggestionSession(t, store, "gpt-test")
			body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
			if got := strings.Join(gotModels, ","); got != tt.wantModels {
				t.Fatalf("Suggest models = %q, want %q", got, tt.wantModels)
			}
			if tt.wantSuggestion == "" {
				if body.Data.Suggestion != nil {
					t.Fatalf("suggestion = %v, want null", *body.Data.Suggestion)
				}
			} else if body.Data.Suggestion == nil || *body.Data.Suggestion != tt.wantSuggestion {
				t.Fatalf("suggestion = %v, want %q", body.Data.Suggestion, tt.wantSuggestion)
			}
			if len(contexts) == 2 && contexts[0] != contexts[1] {
				t.Fatal("快速模型回退应共用同一个 ctx")
			}
		})
	}
}

func TestSuggestionDisabled(t *testing.T) {
	called := false
	handler, store, _ := newTestHandler(t, &fakeRunner{}, func(deps *Deps) {
		deps.SuggestionEnabled = func() bool { return false }
		deps.Suggest = func(context.Context, string, string, string, string, int) (string, error) {
			called = true
			return "继续", nil
		}
	})
	meta := newSuggestionSession(t, store, "gpt-test")
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion != nil || called {
		t.Fatalf("关闭设置时不应调用模型: %+v, called=%v", body.Data, called)
	}
}

func TestSuggestionSkipsRunningSession(t *testing.T) {
	old := suggestionRunWait
	suggestionRunWait = 50 * time.Millisecond
	defer func() { suggestionRunWait = old }()
	handler, store, runs := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
	meta := newSuggestionSession(t, store, "gpt-test")
	if _, err := runs.Begin(meta.ID, "run-1"); err != nil {
		t.Fatal(err)
	}
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion != nil {
		t.Fatalf("运行中应返回 null: %+v", body.Data)
	}
}

// agent_done 事件先于 run 结束发出:前端此时请求预测,应等 run 收尾后照常生成。
func TestSuggestionWaitsForRunToFinish(t *testing.T) {
	handler, store, runs := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
	meta := newSuggestionSession(t, store, "gpt-test")
	run, err := runs.Begin(meta.ID, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		run.Finish()
	}()
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion == nil || *body.Data.Suggestion != "继续" {
		t.Fatalf("run 收尾后应返回预测: %+v", body.Data)
	}
}

func TestSuggestionSkipsPendingQuestionAndEscalation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*session.Meta)
	}{
		{name: "未回答question", mutate: func(meta *session.Meta) {
			meta.Questions = []session.Question{{QuestionID: "q1", AskedAt: time.Now()}}
		}},
		{name: "未处理审批", mutate: func(meta *session.Meta) {
			meta.Escalations = []session.Escalation{{EscalationID: "e1"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, store, _ := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
			meta := newSuggestionSession(t, store, "gpt-test")
			if _, err := store.Update(meta.ID, func(meta *session.Meta) error {
				tt.mutate(meta)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
			if body.Data.Suggestion != nil {
				t.Fatalf("待处理交互时应返回 null: %+v", body.Data)
			}
		})
	}
}

func TestSuggestionSkipsErrorEnding(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
	meta := newSuggestionSession(t, store, "gpt-test")
	if _, err := store.Update(meta.ID, func(meta *session.Meta) error {
		meta.RunErrors = []session.RunError{{Message: "failed", AfterEntryID: "a1", At: time.Now()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion != nil {
		t.Fatalf("错误结尾应返回 null: %+v", body.Data)
	}
}

func TestSuggestionModelErrorAndFilterRejectReturnNull(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		err  error
	}{
		{name: "模型报错", err: errors.New("upstream failed")},
		{name: "超时", err: context.DeadlineExceeded},
		{name: "过滤丢弃", text: "谢谢，太棒了"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, store, _ := newTestHandler(t, &fakeRunner{}, suggestionDeps(tt.text, tt.err))
			meta := newSuggestionSession(t, store, "gpt-test")
			body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
			if body.Data.Suggestion != nil {
				t.Fatalf("应返回 null: %+v", body.Data)
			}
		})
	}
}

func TestSuggestionNotFoundAndNilDeps(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+uuid.NewString()+"/suggestion", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在会话 status = %d", rec.Code)
	}

	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newSuggestionSession(t, store, "gpt-test")
	body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
	if body.Data.Suggestion != nil {
		t.Fatalf("nil Deps 应降级为 null: %+v", body.Data)
	}
}

func TestSuggestionSkipsMissingUserOrModel(t *testing.T) {
	for _, tt := range []struct {
		name      string
		model     string
		emptyUser bool
	}{
		{name: "无用户消息", model: "gpt-test", emptyUser: true},
		{name: "无模型", model: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler, store, _ := newTestHandler(t, &fakeRunner{}, suggestionDeps("继续", nil))
			meta := newSuggestionSession(t, store, tt.model)
			if tt.emptyUser {
				if _, err := store.Update(meta.ID, func(meta *session.Meta) error {
					meta.Messages = nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			body := doSuggestionRequest(t, handler, meta.ID, http.StatusOK)
			if body.Data.Suggestion != nil {
				t.Fatalf("应返回 null: %+v", body.Data)
			}
		})
	}
}

func suggestionDeps(text string, err error) func(*Deps) {
	return func(deps *Deps) {
		deps.SuggestionEnabled = func() bool { return true }
		deps.Suggest = func(context.Context, string, string, string, string, int) (string, error) {
			return text, err
		}
	}
}

func newSuggestionSession(t *testing.T, store *session.Store, model string) session.Meta {
	t.Helper()
	meta, err := store.Create(model, "ask")
	if err != nil {
		t.Fatal(err)
	}
	meta, err = store.Update(meta.ID, func(meta *session.Meta) error {
		meta.PiSessionID = "pisuggest"
		meta.Messages = []session.UserMessage{{MessageID: "m1", EntryID: "u1", Text: "修复这个报错"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session","id":"pisuggest"}`,
		`{"type":"message","id":"u1","message":{"role":"user","content":"修复这个报错"}}`,
		`{"type":"message","id":"a1","parentId":"u1","message":{"role":"assistant","content":[{"type":"text","text":"已经修复"}]}}`,
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(store.PiDir(meta.ID), "2025-01-01T00-00-00_pisuggest.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return meta
}

func doSuggestionRequest(t *testing.T, handler *Handler, id string, wantStatus int) struct {
	Data struct {
		Suggestion *string `json:"suggestion"`
		RequestID  string  `json:"request_id"`
	} `json:"data"`
} {
	t.Helper()
	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+id+"/suggestion", "")
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Suggestion *string `json:"suggestion"`
			RequestID  string  `json:"request_id"`
		} `json:"data"`
	}
	decodeBody(t, rec, &body)
	if body.Data.RequestID == "" {
		t.Fatal("request_id 不能为空")
	}
	return body
}
