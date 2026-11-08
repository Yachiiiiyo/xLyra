package config

import (
	"os"
	"path/filepath"
	"strings"
)

func ResolveWorkdir() string {
	if wd := os.Getenv("WORKDIR"); wd != "" {
		return wd
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fallbackHomeDir()
	}

	for dir := cwd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
	}

	return fallbackHomeDir()
}

func fallbackHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		cwd, _ := os.Getwd()
		return cwd
	}
	return filepath.Join(home, ".xlyra")
}

func ConfigDir(workdir string) string {
	return filepath.Join(workdir, "conf")
}

func ConfigFilePath(workdir string) string {
	return filepath.Join(ConfigDir(workdir), "config.json")
}

// ResolveAgentDataDir 返回 Agent 数据目录:配置了 AGENT_DATA_DIR 则用之,
// 否则 <WORKDIR>/agent;供运行时装配与备份共用。
func ResolveAgentDataDir(cfg Config) string {
	dataDir := strings.TrimSpace(cfg.AgentDataDir)
	if dataDir == "" {
		dataDir = filepath.Join(ResolveWorkdir(), "agent")
	}
	return dataDir
}

// ResolveAgentWorkspaceDir 返回 Agent 共享工作区目录:配置了
// AGENT_WORKSPACE_DIR 则用之,否则 <数据目录>/workspace;用 filepath.Abs
// 规整(失败则原样返回)。
func ResolveAgentWorkspaceDir(cfg Config) string {
	workspace := strings.TrimSpace(cfg.AgentWorkspaceDir)
	if workspace == "" {
		workspace = filepath.Join(ResolveAgentDataDir(cfg), "workspace")
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		return abs
	}
	return workspace
}
