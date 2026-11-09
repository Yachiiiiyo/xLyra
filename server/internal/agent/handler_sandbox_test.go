package agent

import (
	"net/http"
	"testing"

	"xlyra/server/internal/agent/runner"
)

func TestHealthReportsSandboxStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     Status
		wantReason bool
	}{
		{name: "开启", status: Status{Mode: "on", Sandbox: "on", BashEnabled: true, WriteToolsEnabled: true}},
		{name: "不可用", status: Status{Mode: "on", Sandbox: "unavailable", SandboxReason: "Landlock 不可用", WriteToolsEnabled: true}, wantReason: true},
		{name: "自检失败", status: Status{Mode: "on", Sandbox: "failed", SandboxReason: "沙箱自检失败:无网络", WriteToolsEnabled: true}, wantReason: true},
		{name: "bash 关闭", status: Status{Mode: "on", Sandbox: "on", SandboxReason: "AGENT_BASH=off,bash 已关闭", WriteToolsEnabled: true}, wantReason: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRunner{health: runner.Health{Available: true}}
			handler, _, _ := newTestHandler(t, fake, func(d *Deps) {
				d.Status = func() Status { return tc.status }
			})
			rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/health", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("health = %d, body %s", rec.Code, rec.Body.String())
			}
			var body map[string]any
			decodeBody(t, rec, &body)
			if body["status"] != "healthy" || body["sandbox"] != tc.status.Sandbox || body["bash_enabled"] != tc.status.BashEnabled {
				t.Fatalf("health body = %v", body)
			}
			if tc.wantReason && body["sandbox_reason"] != tc.status.SandboxReason {
				t.Fatalf("sandbox_reason = %v", body["sandbox_reason"])
			}
			if !tc.wantReason {
				if _, ok := body["sandbox_reason"]; ok {
					t.Fatalf("sandbox_reason 应省略: %v", body)
				}
			}
		})
	}
}

type forgetFakeRunner struct {
	*fakeRunner
	forgotten []string
}

func (f *forgetFakeRunner) ForgetSession(id string) error {
	f.forgotten = append(f.forgotten, id)
	return nil
}

func TestDeleteSessionForgetsSession(t *testing.T) {
	base := &fakeRunner{health: runner.Health{Available: true}}
	fake := &forgetFakeRunner{fakeRunner: base}
	handler, store, _ := newTestHandler(t, base)
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	// newTestHandler 接收基础接口,替换为实现了可选清理接口的 runner。
	handler.runner = fake
	rec := doJSONAgent(t, handler, http.MethodDelete, "/api/v1/agent/sessions/"+meta.ID, "")
	if rec.Code != http.StatusOK || len(fake.forgotten) != 1 || fake.forgotten[0] != meta.ID {
		t.Fatalf("delete = %d, forgotten = %v, body %s", rec.Code, fake.forgotten, rec.Body.String())
	}
}
