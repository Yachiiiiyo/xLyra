package config

import (
	"path/filepath"
	"testing"
)

// TestResolveAgentDataDir 覆盖 AGENT_DATA_DIR 显式配置与缺省回退两种情况。
func TestResolveAgentDataDir(t *testing.T) {
	t.Run("显式配置", func(t *testing.T) {
		t.Setenv("WORKDIR", t.TempDir())
		cfg := Config{AgentDataDir: " /data/agent "}
		if got := ResolveAgentDataDir(cfg); got != "/data/agent" {
			t.Fatalf("ResolveAgentDataDir = %q, want /data/agent", got)
		}
	})

	t.Run("缺省回退到 WORKDIR/agent", func(t *testing.T) {
		workdir := t.TempDir()
		t.Setenv("WORKDIR", workdir)
		if got := ResolveAgentDataDir(Config{}); got != filepath.Join(workdir, "agent") {
			t.Fatalf("ResolveAgentDataDir = %q, want %q", got, filepath.Join(workdir, "agent"))
		}
	})
}

// TestResolveAgentWorkspaceDir 覆盖 AGENT_WORKSPACE_DIR 显式配置、缺省回退
// 到 <数据目录>/workspace,以及相对路径的 Abs 规整。
func TestResolveAgentWorkspaceDir(t *testing.T) {
	t.Run("显式配置并规整为绝对路径", func(t *testing.T) {
		workdir := t.TempDir()
		t.Setenv("WORKDIR", workdir)
		cfg := Config{AgentWorkspaceDir: " relative/workspace "}
		want, err := filepath.Abs("relative/workspace")
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		if got := ResolveAgentWorkspaceDir(cfg); got != want {
			t.Fatalf("ResolveAgentWorkspaceDir = %q, want %q", got, want)
		}
	})

	t.Run("缺省回退到数据目录下 workspace", func(t *testing.T) {
		workdir := t.TempDir()
		t.Setenv("WORKDIR", workdir)
		want := filepath.Join(workdir, "agent", "workspace")
		if got := ResolveAgentWorkspaceDir(Config{}); got != want {
			t.Fatalf("ResolveAgentWorkspaceDir = %q, want %q", got, want)
		}
	})

	t.Run("显式数据目录缺省工作区", func(t *testing.T) {
		t.Setenv("WORKDIR", t.TempDir())
		cfg := Config{AgentDataDir: "/data/agent"}
		want, err := filepath.Abs(filepath.Join("/data/agent", "workspace"))
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		if got := ResolveAgentWorkspaceDir(cfg); got != want {
			t.Fatalf("ResolveAgentWorkspaceDir = %q, want %q", got, want)
		}
	})
}
