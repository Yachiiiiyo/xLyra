package runner

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// withLocalBash 开启本机直接执行 bash(沙箱未启用路径)。
func withLocalBash(defaultTimeout, maxTimeout time.Duration) envOption {
	return func(cfg *Config, _ *[]string) {
		cfg.LocalBash = &LocalBashConfig{DefaultTimeout: defaultTimeout, MaxTimeout: maxTimeout}
	}
}

// TestLocalBashConfigValidation:LocalBash 与沙箱 bash 同开时 New 报错。
func TestLocalBashConfigValidation(t *testing.T) {
	env := newTestEnv(t, "basic")
	cfg := env.runner.cfg
	cfg.LocalBash = &LocalBashConfig{DefaultTimeout: 120 * time.Second, MaxTimeout: 600 * time.Second}
	cfg.Sandbox = &SandboxConfig{Exe: "x", PiLandlock: true, Bash: true}
	cfg.Isolation = &IsolationConfig{}
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "LocalBash") {
		t.Fatalf("同开应报错,得到 %v", err)
	}
	// 沙箱配置不带 bash 时可以共存(实际装配不会出现)。
	cfg.Sandbox.Bash = false
	if _, err := New(cfg); err != nil {
		t.Fatalf("沙箱不带 bash 时不应报错: %v", err)
	}
}

// TestLocalBashRun:LocalBash 配置下 --tools 含 bash,运行配置 bash.mode=="local"
// 且无 socket,系统提示用本机执行规则(不含“没有网络”)。
func TestLocalBashRun(t *testing.T) {
	env := newTestEnv(t, "basic", withLocalBash(120*time.Second, 600*time.Second))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)

	args := lastStartArgs(t, env.record)
	i := slices.Index(args, "--tools")
	if i < 0 || i+1 >= len(args) || !strings.Contains(args[i+1], "bash") {
		t.Fatalf("--tools 应含 bash: %v", args)
	}

	var runConfig struct {
		Runtime struct {
			Bash struct {
				Mode           string `json:"mode"`
				Socket         string `json:"socket"`
				DefaultTimeout int    `json:"defaultTimeout"`
				MaxTimeout     int    `json:"maxTimeout"`
			} `json:"bash"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(lastRunConfigJSON(t, env.record)), &runConfig); err != nil {
		t.Fatal(err)
	}
	bash := runConfig.Runtime.Bash
	if bash.Mode != "local" {
		t.Fatalf("bash.mode = %q, want local", bash.Mode)
	}
	if bash.Socket != "" {
		t.Fatalf("本机执行不应带 socket: %q", bash.Socket)
	}
	if bash.DefaultTimeout != 120 || bash.MaxTimeout != 600 {
		t.Fatalf("bash 超时 = %+v, want 120/600", bash)
	}

	j := slices.Index(args, "--system-prompt")
	if j < 0 || j+1 >= len(args) {
		t.Fatal("缺少 --system-prompt")
	}
	prompt := args[j+1]
	if !strings.Contains(prompt, "命令在本机直接执行") {
		t.Fatal("系统提示应说明命令在本机直接执行")
	}
	if strings.Contains(prompt, "没有网络") {
		t.Fatal("本机执行提示不应包含“没有网络”")
	}
	if !strings.Contains(prompt, "默认 120 秒") {
		t.Fatal("系统提示应包含默认超时 120 秒")
	}
}

// TestLocalBashSystemPrompt:systemPrompt 的 local 分支直接可测。
func TestLocalBashSystemPrompt(t *testing.T) {
	t.Parallel()

	r := &Runner{}
	if got := r.systemPrompt("", true, true, 90*time.Second, false, false, false); !strings.Contains(got, "命令在本机直接执行") || strings.Contains(got, "没有网络") {
		t.Fatalf("local 提示异常: %q", got)
	}
	if got := r.systemPrompt("", true, true, 90*time.Second, false, false, false); !strings.Contains(got, "默认 90 秒") {
		t.Fatalf("local 提示应填入默认超时: %q", got)
	}
	if got := r.systemPrompt("", true, false, 90*time.Second, false, false, false); !strings.Contains(got, "沙箱命令执行规则") || !strings.Contains(got, "没有网络") {
		t.Fatalf("沙箱提示异常: %q", got)
	}
}
