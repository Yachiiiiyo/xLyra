// P6h:分叉会话接口与发送/重试的 references 透传。
package agent

import (
	"net/http"
	"strings"
	"testing"

	"xlyra/server/internal/agent/runner"
	"xlyra/server/internal/agent/session"
)

func TestForkSessionHandler(t *testing.T) {
	fake := &fakeRunner{}
	handler, store, runs := newTestHandler(t, fake)

	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	updateSessionMeta(t, store, meta.ID, func(m *session.Meta) {
		m.Title = "源标题"
		m.Messages = []session.UserMessage{
			{MessageID: "msg-1", EntryID: "e1", Text: "第一条"},
			{MessageID: "msg-2", EntryID: "e2", Text: "第二条"},
		}
	})

	// 整会话分叉(空请求体):201 + 契约字段
	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+meta.ID+"/fork", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			SessionID      string `json:"session_id"`
			Title          string `json:"title"`
			WorkspaceCopie bool   `json:"workspace_copied"`
		} `json:"data"`
	}
	decodeBody(t, rec, &payload)
	if payload.Data.SessionID == "" || payload.Data.SessionID == meta.ID {
		t.Fatalf("session_id = %q", payload.Data.SessionID)
	}
	if payload.Data.Title != "分叉 · 源标题" {
		t.Fatalf("title = %q", payload.Data.Title)
	}
	if payload.Data.WorkspaceCopie {
		t.Fatalf("workspace_copied 应为 false: %s", rec.Body.String())
	}
	forked, err := store.Get(payload.Data.SessionID)
	if err != nil {
		t.Fatalf("新会话应已创建: %v", err)
	}
	// v1 不复制 workspace:新会话目录里只有 meta.json 与空的 pi/、workspace/
	if _, err := store.BuildTranscript(forked.ID); err != nil {
		t.Fatalf("新会话可读: %v", err)
	}

	// 按消息分叉
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+meta.ID+"/fork", `{"message_id":"msg-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("按消息分叉 status = %d, body %s", rec.Code, rec.Body.String())
	}

	// 源不存在 → 404
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/00000000-0000-4000-8000-00000000dead/fork", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失会话 status = %d", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "not_found" {
		t.Fatalf("缺失会话 code = %s", code)
	}

	// message_id 不存在 → 404
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+meta.ID+"/fork", `{"message_id":"no-such"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("缺失消息 status = %d, body %s", rec.Code, rec.Body.String())
	}

	// 非法会话 id → 404(不进入存储层)
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/not-a-uuid/fork", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非法 id status = %d", rec.Code)
	}

	// 源有进行中的 run → 409 agent_busy
	if _, err := runs.Begin(meta.ID, "run-1"); err != nil {
		t.Fatal(err)
	}
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+meta.ID+"/fork", `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("运行中 status = %d, body %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "agent_busy" {
		t.Fatalf("运行中 code = %s", code)
	}

	// 坏请求体 → 400
	bad, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+bad.ID+"/fork", "not-json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("坏请求体 status = %d", rec.Code)
	}
}

func updateSessionMeta(t *testing.T, store *session.Store, id string, fn func(*session.Meta)) {
	t.Helper()
	if _, err := store.Update(id, func(m *session.Meta) error {
		fn(m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 发送与重试的请求体带 references 时透传给 runner。
func TestSessionCommandReferencesPassThrough(t *testing.T) {
	fake := &fakeRunner{
		sendResult:  runner.SendResult{SessionID: "s-1", MessageID: "m-1", RunID: "r-1"},
		retryResult: runner.SendResult{SessionID: "s-2", MessageID: "m-2", RunID: "r-2"},
	}
	handler, store, _ := newTestHandler(t, fake)
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions",
		`{"content":"你好","model":"m","references":["`+meta.ID+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d, body %s", rec.Code, rec.Body.String())
	}
	if fake.sendInput == nil || len(fake.sendInput.References) != 1 || fake.sendInput.References[0] != meta.ID {
		t.Fatalf("send references 未透传: %+v", fake.sendInput)
	}

	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions/"+meta.ID+"/retry",
		`{"message_id":"msg-1","references":["`+meta.ID+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body %s", rec.Code, rec.Body.String())
	}
	if fake.retryInput == nil || len(fake.retryInput.References) != 1 || fake.retryInput.References[0] != meta.ID {
		t.Fatalf("retry references 未透传: %+v", fake.retryInput)
	}

	// 不带 references 字段:透传空切片(nil)
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", `{"content":"你好","model":"m"}`)
	if rec.Code != http.StatusOK || fake.sendInput.References != nil {
		t.Fatalf("无 references 时应为 nil: %+v", fake.sendInput)
	}

	// references 类型错误 → 400
	rec = doJSONAgent(t, handler, http.MethodPost, "/api/v1/agent/sessions", `{"content":"你好","model":"m","references":"not-array"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 references status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("坏 references code: %s", rec.Body.String())
	}
}
