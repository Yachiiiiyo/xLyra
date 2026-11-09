package app

import (
	"os"
	"testing"

	"xlyra/server/internal/agent/sandbox"
)

func TestMain(m *testing.M) {
	if sandbox.MaybeRunHelper() {
		os.Exit(2) // helper 匹配时内部已退出,这里不可达
	}
	os.Exit(m.Run())
}
