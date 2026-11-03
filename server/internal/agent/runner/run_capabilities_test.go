package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"xlyra/server/internal/agent/capabilities"
)

const skillAlphaMD = `---
name: alpha
description: 测试技能 alpha
---
alpha 说明
`

const skillBetaMD = `---
name: beta
description: 测试技能 beta
---
beta 说明
`

// failingCapabilities 模拟 Snapshot 出错,验证设置读取失败不阻断对话。
type failingCapabilities struct{}

func (failingCapabilities) Snapshot() (capabilities.Snapshot, error) {
	return capabilities.Snapshot{}, errors.New("capabilities boom")
}

func withCapabilities(store *capabilities.Store) envOption {
	return func(cfg *Config, _ *[]string) { cfg.Capabilities = store }
}

func withFailingCapabilities() envOption {
	return func(cfg *Config, _ *[]string) { cfg.Capabilities = failingCapabilities{} }
}

func mustPutSkillForRun(t *testing.T, store *capabilities.Store, name, content string) {
	t.Helper()
	if _, err := store.PutSkill(name, content); err != nil {
		t.Fatalf("PutSkill(%s): %v", name, err)
	}
}

// lastStartArgs 返回最近一次 Pi 启动的参数。
func lastStartArgs(t *testing.T, record string) []string {
	t.Helper()
	var args []string
	for _, item := range readRecords(t, record) {
		if item["kind"] == "start" {
			args = recordArgs(t, item)
		}
	}
	if args == nil {
		t.Fatal("没有启动记录")
	}
	return args
}

// skillArgs 收集启动参数里全部 --skill 的值。
func skillArgs(args []string) []string {
	var result []string
	for i, arg := range args {
		if arg == "--skill" && i+1 < len(args) {
			result = append(result, args[i+1])
		}
	}
	return result
}

// piAgentFile 读取会话 pi-agent 目录下的文件;不存在时第二返回值为 false。
func piAgentFile(t *testing.T, env *testEnv, sessionID, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(env.store.Dir(sessionID), "pi-agent", name))
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(data), true
}

// TestPrepareSkillsArgs:启动参数固定带 --no-skills,再对启用的技能目录逐个
// 追加 --skill;停用技能与总开关关闭都不传对应目录。
func TestPrepareSkillsArgs(t *testing.T) {
	t.Run("all enabled", func(t *testing.T) {
		root := t.TempDir()
		store := capabilities.NewStore(filepath.Join(root, "caps"))
		mustPutSkillForRun(t, store, "alpha", skillAlphaMD)
		mustPutSkillForRun(t, store, "beta", skillBetaMD)
		env := newTestEnv(t, "basic", withCapabilities(store))
		result := sendBasic(t, env)
		collectRun(t, env.runs, result.SessionID).wait(t)

		args := lastStartArgs(t, env.record)
		if !slices.Contains(args, "--no-skills") {
			t.Fatalf("启动参数缺少 --no-skills: %v", args)
		}
		want := []string{
			filepath.Join(root, "caps", "skills", "alpha"),
			filepath.Join(root, "caps", "skills", "beta"),
		}
		if got := skillArgs(args); !slices.Equal(got, want) {
			t.Fatalf("--skill = %v, want %v", got, want)
		}
	})

	t.Run("one disabled", func(t *testing.T) {
		root := t.TempDir()
		store := capabilities.NewStore(filepath.Join(root, "caps"))
		mustPutSkillForRun(t, store, "alpha", skillAlphaMD)
		mustPutSkillForRun(t, store, "beta", skillBetaMD)
		if _, err := store.PutConfig(capabilities.Config{
			EnableSkills: true, EnableAgentsMD: true,
			DisabledSkills: []string{"beta"}, CompactTriggerRatio: 0.9,
		}); err != nil {
			t.Fatal(err)
		}
		env := newTestEnv(t, "basic", withCapabilities(store))
		result := sendBasic(t, env)
		collectRun(t, env.runs, result.SessionID).wait(t)

		want := []string{filepath.Join(root, "caps", "skills", "alpha")}
		if got := skillArgs(lastStartArgs(t, env.record)); !slices.Equal(got, want) {
			t.Fatalf("--skill = %v, want %v", got, want)
		}
	})

	t.Run("master off", func(t *testing.T) {
		root := t.TempDir()
		store := capabilities.NewStore(filepath.Join(root, "caps"))
		mustPutSkillForRun(t, store, "alpha", skillAlphaMD)
		if _, err := store.PutConfig(capabilities.Config{
			EnableSkills: false, EnableAgentsMD: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.9,
		}); err != nil {
			t.Fatal(err)
		}
		env := newTestEnv(t, "basic", withCapabilities(store))
		result := sendBasic(t, env)
		collectRun(t, env.runs, result.SessionID).wait(t)

		if got := skillArgs(lastStartArgs(t, env.record)); len(got) != 0 {
			t.Fatalf("总开关关闭时不应传 --skill: %v", got)
		}
		if !slices.Contains(lastStartArgs(t, env.record), "--no-skills") {
			t.Fatal("总开关关闭时仍应带 --no-skills")
		}
	})
}

// TestPrepareAgentsMD:非空写入 PI_CODING_AGENT_DIR/AGENTS.md(0600);清空后
// 文件被删除;enable_agents_md=false 时加 --no-context-files 且文件同样清掉。
func TestPrepareAgentsMD(t *testing.T) {
	store := capabilities.NewStore(filepath.Join(t.TempDir(), "caps"))
	if err := store.PutAgentsMD("# 全局人格\n保持简洁。"); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, "basic", withCapabilities(store))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)

	content, exists := piAgentFile(t, env, result.SessionID, "AGENTS.md")
	if !exists || content != "# 全局人格\n保持简洁。" {
		t.Fatalf("AGENTS.md = %q exists=%v", content, exists)
	}
	info, err := os.Stat(filepath.Join(env.store.Dir(result.SessionID), "pi-agent", "AGENTS.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("AGENTS.md 权限异常: %v %v", info.Mode().Perm(), err)
	}
	if slices.Contains(lastStartArgs(t, env.record), "--no-context-files") {
		t.Fatal("启用 AGENTS.md 时不应带 --no-context-files")
	}

	// 清空人格:同会话下一次 run 应删除上一次的残留文件。
	if err := store.DeleteAgentsMD(); err != nil {
		t.Fatal(err)
	}
	second, err := env.runner.Send(context.Background(), SendInput{
		SessionID: result.SessionID, Content: "第二轮", Model: "mock-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, second.SessionID).wait(t)
	if _, exists := piAgentFile(t, env, result.SessionID, "AGENTS.md"); exists {
		t.Fatal("清空后 AGENTS.md 应被删除")
	}

	// 总开关关闭:即使磁盘上有 AGENTS.md 也不加载,并显式禁用上下文文件发现。
	if err := store.PutAgentsMD("# 又写了"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConfig(capabilities.Config{
		EnableSkills: true, EnableAgentsMD: false, DisabledSkills: []string{}, CompactTriggerRatio: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	third, err := env.runner.Send(context.Background(), SendInput{
		SessionID: result.SessionID, Content: "第三轮", Model: "mock-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, third.SessionID).wait(t)
	if !slices.Contains(lastStartArgs(t, env.record), "--no-context-files") {
		t.Fatal("关闭 AGENTS.md 时应带 --no-context-files")
	}
	if _, exists := piAgentFile(t, env, result.SessionID, "AGENTS.md"); exists {
		t.Fatal("关闭 AGENTS.md 时 pi-agent 下不应有该文件")
	}
}

// TestPrepareContextSettings:水位线与窗口换算进 settings.json 的
// compaction.reserveTokens 与 models.json 的 contextWindow。
func TestPrepareContextSettings(t *testing.T) {
	store := capabilities.NewStore(t.TempDir())
	if _, err := store.PutConfig(capabilities.Config{
		EnableSkills: true, EnableAgentsMD: true, DisabledSkills: []string{},
		CompactTriggerRatio: 0.8, ContextWindow: 100000,
	}); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, "basic", withCapabilities(store))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)

	settings, exists := piAgentFile(t, env, result.SessionID, "settings.json")
	if !exists {
		t.Fatal("settings.json 不存在")
	}
	var settingsFile struct {
		Compaction struct {
			Enabled       bool `json:"enabled"`
			ReserveTokens int  `json:"reserveTokens"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal([]byte(settings), &settingsFile); err != nil {
		t.Fatalf("解析 settings.json: %v\n%s", err, settings)
	}
	// 100000 × (1 − 0.8) = 20000
	if !settingsFile.Compaction.Enabled || settingsFile.Compaction.ReserveTokens != 20000 {
		t.Fatalf("compaction = %+v, want enabled+20000", settingsFile.Compaction)
	}

	models, _ := piAgentFile(t, env, result.SessionID, "models.json")
	var modelsFile struct {
		Providers map[string]struct {
			Models []struct {
				ContextWindow int `json:"contextWindow"`
				MaxTokens     int `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(models), &modelsFile); err != nil {
		t.Fatalf("解析 models.json: %v\n%s", err, models)
	}
	entry := modelsFile.Providers["xlyra"].Models[0]
	if entry.ContextWindow != 100000 {
		t.Fatalf("contextWindow = %d, want 100000", entry.ContextWindow)
	}
	if entry.MaxTokens != 8192 {
		t.Fatalf("maxTokens = %d, want 占位 8192", entry.MaxTokens)
	}
}

// TestPrepareDefaultsWithoutCapabilities:未装配能力源时技能与 AGENTS.md 全关,
// 上下文参数取默认(水位线 0.9、窗口 200000 → reserve 20000)。
func TestPrepareDefaultsWithoutCapabilities(t *testing.T) {
	env := newTestEnv(t, "basic")
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("终态 = %s, want agent_done", events[len(events)-1].Type)
	}

	args := lastStartArgs(t, env.record)
	if !slices.Contains(args, "--no-skills") {
		t.Fatal("默认应带 --no-skills")
	}
	if got := skillArgs(args); len(got) != 0 {
		t.Fatalf("默认不应传 --skill: %v", got)
	}
	if _, exists := piAgentFile(t, env, result.SessionID, "AGENTS.md"); exists {
		t.Fatal("默认不应写 AGENTS.md")
	}

	settings, _ := piAgentFile(t, env, result.SessionID, "settings.json")
	var settingsFile struct {
		Compaction struct {
			ReserveTokens int `json:"reserveTokens"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal([]byte(settings), &settingsFile); err != nil {
		t.Fatal(err)
	}
	// 200000 × (1 − 0.9) = 20000
	if settingsFile.Compaction.ReserveTokens != 20000 {
		t.Fatalf("reserveTokens = %d, want 20000", settingsFile.Compaction.ReserveTokens)
	}

	models, _ := piAgentFile(t, env, result.SessionID, "models.json")
	if !strings.Contains(models, `"contextWindow": 200000`) {
		t.Fatalf("models.json 缺少默认窗口 200000:\n%s", models)
	}
}

// readRootsFromXlyra 解析指定会话 pi-agent/xlyra.json 的 readRoots 与权限模式。
func readRootsFromXlyra(t *testing.T, env *testEnv, sessionID string) (string, []string) {
	t.Helper()
	data, exists := piAgentFile(t, env, sessionID, "xlyra.json")
	if !exists {
		t.Fatal("xlyra.json 不存在")
	}
	var parsed struct {
		PermissionMode string   `json:"permissionMode"`
		ReadRoots      []string `json:"readRoots"`
	}
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		t.Fatalf("解析 xlyra.json: %v\n%s", err, data)
	}
	return parsed.PermissionMode, parsed.ReadRoots
}

// TestXlyraReadRoots:xlyra.json 的 readRoots 与快照 SkillDirs 同步:有技能时含
// 全部启用目录(按名排序),停用/总开关关闭后不再含;未装配能力源或快照失败时
// 为空数组(而非 null),权限模式断言不受影响。
func TestXlyraReadRoots(t *testing.T) {
	root := t.TempDir()
	capsRoot := filepath.Join(root, "caps")
	store := capabilities.NewStore(capsRoot)
	mustPutSkillForRun(t, store, "alpha", skillAlphaMD)
	mustPutSkillForRun(t, store, "beta", skillBetaMD)
	env := newTestEnv(t, "basic", withCapabilities(store))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)

	// 有技能:readRoots 含两个启用目录,权限模式仍是默认 ask
	mode, got := readRootsFromXlyra(t, env, result.SessionID)
	want := []string{
		filepath.Join(capsRoot, "skills", "alpha"),
		filepath.Join(capsRoot, "skills", "beta"),
	}
	if mode != "ask" || !slices.Equal(got, want) {
		t.Fatalf("readRoots = (%q, %v), want (%q, %v)", mode, got, "ask", want)
	}

	// 停用 beta:同会话下一次 run 不再含 beta 目录
	if _, err := store.PutConfig(capabilities.Config{
		EnableSkills: true, EnableAgentsMD: true,
		DisabledSkills: []string{"beta"}, CompactTriggerRatio: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := env.runner.Send(context.Background(), SendInput{SessionID: result.SessionID, Content: "第二轮", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, second.SessionID).wait(t)
	mode, got = readRootsFromXlyra(t, env, result.SessionID)
	if mode != "ask" || !slices.Equal(got, want[:1]) {
		t.Fatalf("停用 beta 后 readRoots = (%q, %v), want (%q, %v)", mode, got, "ask", want[:1])
	}

	// 总开关关闭:空数组(逐 run 重写,不残留上一个 run 的目录)
	if _, err := store.PutConfig(capabilities.Config{
		EnableSkills: false, EnableAgentsMD: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	third, err := env.runner.Send(context.Background(), SendInput{SessionID: result.SessionID, Content: "第三轮", Model: "mock-1"})
	if err != nil {
		t.Fatal(err)
	}
	collectRun(t, env.runs, third.SessionID).wait(t)
	if _, got = readRootsFromXlyra(t, env, result.SessionID); len(got) != 0 {
		t.Fatalf("总开关关闭后 readRoots 应为空: %v", got)
	}

	// 未装配能力源:空数组
	bareEnv := newTestEnv(t, "basic")
	bare := sendBasic(t, bareEnv)
	collectRun(t, bareEnv.runs, bare.SessionID).wait(t)
	if _, got = readRootsFromXlyra(t, bareEnv, bare.SessionID); len(got) != 0 {
		t.Fatalf("未装配能力源时 readRoots 应为空: %v", got)
	}

	// 快照失败:fail-closed 退回空数组,不阻断 run
	failEnv := newTestEnv(t, "basic", withFailingCapabilities())
	failed := sendBasic(t, failEnv)
	events := collectRun(t, failEnv.runs, failed.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("快照失败不应阻断 run,终态 = %s", events[len(events)-1].Type)
	}
	if _, got = readRootsFromXlyra(t, failEnv, failed.SessionID); len(got) != 0 {
		t.Fatalf("快照失败时 readRoots 应为空: %v", got)
	}
}

// TestPrepareSnapshotErrorFallsBack:快照读取失败只记 Warn,run 照常按默认执行。
func TestPrepareSnapshotErrorFallsBack(t *testing.T) {
	env := newTestEnv(t, "basic", withFailingCapabilities())
	result := sendBasic(t, env)
	events := collectRun(t, env.runs, result.SessionID).wait(t)
	if events[len(events)-1].Type != "agent_done" {
		t.Fatalf("快照失败不应阻断 run,终态 = %s", events[len(events)-1].Type)
	}

	args := lastStartArgs(t, env.record)
	if !slices.Contains(args, "--no-skills") || len(skillArgs(args)) != 0 {
		t.Fatalf("快照失败应退回默认技能参数: %v", args)
	}
	if _, exists := piAgentFile(t, env, result.SessionID, "AGENTS.md"); exists {
		t.Fatal("快照失败不应写 AGENTS.md")
	}
	if !strings.Contains(env.logs.String(), "读取 Agent 能力快照失败") {
		t.Fatal("快照失败应记 Warn")
	}
}
