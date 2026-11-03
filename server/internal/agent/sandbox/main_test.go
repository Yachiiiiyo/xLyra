package sandbox

import (
	"os"
	"testing"
)

// testProbeHook 由 linux 集成测试文件注入:测试二进制作为被沙箱执行的探针
// 再次运行时(SANDBOX_TEST_PROBE),不跑测试、执行探测后退出。TestMain 的
// 第一件事是 helper 分发——这是 helper 在测试二进制里能跑的前提。
var testProbeHook func(mode string) int

func TestMain(m *testing.M) {
	if MaybeRunHelper() {
		os.Exit(2) // 匹配时 helper 内部已 os.Exit,这里不可达
	}
	if testProbeHook != nil {
		if mode := os.Getenv("SANDBOX_TEST_PROBE"); mode != "" {
			os.Exit(testProbeHook(mode))
		}
	}
	os.Exit(m.Run())
}

// TestMaybeRunHelperNoMatch:普通启动(参数不匹配)必须原样返回 false,
// 绝不能把非 helper 进程吞掉。
func TestMaybeRunHelperNoMatch(t *testing.T) {
	if MaybeRunHelper() {
		t.Fatal("参数不匹配时 MaybeRunHelper 应返回 false")
	}
}
