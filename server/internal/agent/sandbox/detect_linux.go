package sandbox

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// Detect 只做只读查询,绝不在主进程安装 Landlock/seccomp:ABI 用
// landlock_create_ruleset 的 VERSION 标志(不创建规则集),seccomp 用
// PR_GET_SECCOMP(查询内核是否支持,EINVAL 即不支持)。OK 当且仅当
// Linux、Landlock ABI≥4(网络规则可用)且 seccomp 可用。
func Detect() Capability {
	abi, err := landlockABI()
	if err != nil {
		return Capability{Reason: "Landlock 不可用:" + err.Error()}
	}
	if abi < 4 {
		return Capability{LandlockABI: abi, Reason: fmt.Sprintf("Landlock ABI %d < 4,网络规则不可用", abi)}
	}
	if _, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err != nil {
		return Capability{LandlockABI: abi, Reason: "seccomp 不可用:" + err.Error()}
	}
	if _, err := filterArchFor(runtime.GOARCH); err != nil {
		return Capability{LandlockABI: abi, Reason: err.Error()}
	}
	return Capability{OK: true, LandlockABI: abi}
}
