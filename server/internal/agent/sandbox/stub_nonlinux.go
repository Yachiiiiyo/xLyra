//go:build !linux

package sandbox

import (
	"errors"
	"os/exec"
)

// Capability 定义在 spec.go;非 Linux 平台的 Detect 恒不可用。
// 本文件是全部 Linux 专属导出 API 的 stub,保证 macOS 等平台可编译、
// 单元测试可运行。

// MaybeRunHelper 非 Linux 上没有 helper,恒返回 false。
func MaybeRunHelper() bool { return false }

// ChildCommand 非 Linux 平台不可用。
func ChildCommand(exe string, spec Spec, argv []string) (*exec.Cmd, error) {
	return nil, errors.New("sandbox: 仅 Linux 支持")
}

// RunCommand 非 Linux 平台不可用。
func RunCommand(exe string, spec Spec, argv []string) (*exec.Cmd, error) {
	return nil, errors.New("sandbox: 仅 Linux 支持")
}

// Detect 非 Linux 平台恒不可用。
func Detect() Capability { return Capability{Reason: "仅 Linux 支持"} }

// ProcsOfUID 非 Linux 平台无 /proc,返回空。
func ProcsOfUID(uid int) []Proc { return nil }

// KillUID 非 Linux 平台不可用。
func KillUID(uid int) (killed, remaining int, err error) {
	return 0, 0, errors.New("sandbox: 仅 Linux 支持")
}

// SweepUIDs 非 Linux 平台无操作。
func SweepUIDs(base, count int) int { return 0 }
