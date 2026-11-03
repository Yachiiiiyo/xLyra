package app

import (
	"strings"
	"testing"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/sandbox"
)

func TestDecideSandbox(t *testing.T) {
	t.Parallel()
	on := isolationDecision{Mode: "on"}
	good := &isolation.Report{OK: true, Checks: []isolation.Check{{Name: "无网络", Passed: true}}}
	bad := &isolation.Report{Checks: []isolation.Check{
		{Name: "无网络", Passed: false}, {Name: "零残留", Passed: false},
	}}
	for _, bashMode := range []string{"auto", "off"} {
		for _, isoMode := range []string{"", "off", "degraded", "failed", "on"} {
			for _, detect := range []sandbox.Capability{{Reason: "Landlock 不可用"}, {OK: true}} {
				for _, check := range []*isolation.Report{nil, bad, good} {
					iso := isolationDecision{Mode: isoMode}
					got := decideSandbox(bashMode, iso, detect, check)
					switch {
					case isoMode != "on":
						if got.Mode != "off" || got.PiLandlock || got.Bash || !strings.Contains(got.Reason, "uid 隔离未启用") {
							t.Fatalf("bash=%s iso=%s: %+v", bashMode, isoMode, got)
						}
					case !detect.OK:
						if got.Mode != "unavailable" || got.Reason != detect.Reason || got.PiLandlock || got.Bash {
							t.Fatalf("bash=%s detect=%+v: %+v", bashMode, detect, got)
						}
					case check == nil || !check.OK:
						if got.Mode != "failed" || got.PiLandlock || got.Bash || !strings.Contains(got.Reason, "沙箱自检失败") {
							t.Fatalf("bash=%s check=%+v: %+v", bashMode, check, got)
						}
					default:
						if got.Mode != "on" || !got.PiLandlock || got.Bash != (bashMode != "off") {
							t.Fatalf("bash=%s: %+v", bashMode, got)
						}
						if bashMode == "off" && !strings.Contains(got.Reason, "AGENT_BASH=off") {
							t.Fatalf("Reason = %q", got.Reason)
						}
					}
				}
			}
		}
	}
	if got := decideSandbox("auto", on, sandbox.Capability{OK: true}, bad); !strings.Contains(got.Reason, "无网络") || !strings.Contains(got.Reason, "零残留") {
		t.Fatalf("失败项未汇总: %q", got.Reason)
	}
}

func TestExecSocketPathLength(t *testing.T) {
	t.Parallel()
	const fixed = len("/sessions/") + 36 + len("/exec/exec.sock")
	dataDir := strings.Repeat("a", 104-fixed)
	if got := execSocketPathLength(dataDir); got != 104 {
		t.Fatalf("length = %d, want 104", got)
	}
	if got := execSocketPathLength(dataDir + "b"); got != 105 {
		t.Fatalf("length = %d, want 105", got)
	}
}
