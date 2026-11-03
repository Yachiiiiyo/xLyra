//go:build !linux

package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNonLinuxStubs:macOS 等平台上沙箱不可用,但同名 API 必须存在且行为确定。
func TestNonLinuxStubs(t *testing.T) {
	if MaybeRunHelper() {
		t.Fatal("非 Linux 上 MaybeRunHelper 恒为 false")
	}
	cap := Detect()
	if cap.OK || !strings.Contains(cap.Reason, "Linux") {
		t.Fatalf("Detect 应不可用:%+v", cap)
	}
	if _, err := ChildCommand("/bin/x", Spec{}, []string{"x"}); err == nil {
		t.Fatal("ChildCommand 应返回错误")
	}
	if _, err := RunCommand("/bin/x", Spec{}, []string{"x"}); err == nil {
		t.Fatal("RunCommand 应返回错误")
	}
	var _ *exec.Cmd // 两个构造器返回类型必须是 *exec.Cmd
	if ProcsOfUID(1) != nil {
		t.Fatal("ProcsOfUID 应返回空")
	}
	if _, _, err := KillUID(1); err == nil {
		t.Fatal("KillUID 应返回错误")
	}
	if SweepUIDs(20000, 100) != 0 {
		t.Fatal("SweepUIDs 应返回 0")
	}
}
