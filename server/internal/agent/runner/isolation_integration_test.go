package runner

// 进程隔离的容器集成测试:需要 Linux + root + RUNNER_ISOLATION_INTEGRATION=1,
// 其余环境跳过。与 isolation 包的集成测试不同,这里走完整的 runner 执行路径
// (假 Pi 子进程真的以会话身份运行),因此测试二进制所在目录与数据目录的祖先
// 都要放宽到可穿越(0711),否则 PiUID 子进程 exec 不到测试二进制、也走不进
// 自己的会话目录。

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/runlog"
	"xlyra/server/internal/agent/session"
)

// requireRunnerIntegration 要求 Linux 容器内以 root 运行并显式设置
// RUNNER_ISOLATION_INTEGRATION=1;其余环境跳过。
func requireRunnerIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("RUNNER_ISOLATION_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + RUNNER_ISOLATION_INTEGRATION=1")
	}
}

// makeTraversable 把 dir 及其到系统临时目录之间的祖先放宽到 0711(只给穿越
// 权限),让非 root 的 PiUID 子进程能到达数据目录与测试二进制。
// 与 isolation 包集成测试的同名辅助函数同思路,本包独立一份。
func makeTraversable(t *testing.T, dir string) {
	t.Helper()
	stop := filepath.Clean(os.TempDir())
	for cur := filepath.Clean(dir); cur != stop && cur != string(filepath.Separator) && cur != "."; cur = filepath.Dir(cur) {
		if err := os.Chmod(cur, 0o711); err != nil {
			t.Fatal(err)
		}
	}
}

// statOwner 返回路径的属主 uid/gid 与权限位。
func statOwner(t *testing.T, path string) (uint32, uint32, os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("无法读取 %s 的属主信息", path)
	}
	return st.Uid, st.Gid, fi.Mode().Perm()
}

// uidProcessCount 统计 /proc 下属主为 uid 的进程数(僵尸已被回收时不计)。
func uidProcessCount(uid uint32) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		fi, err := os.Stat(filepath.Join("/proc", entry.Name()))
		if err != nil {
			continue // 进程刚退出
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == uid {
			count++
		}
	}
	return count
}

// assertNoUidProcesses 轮询断言该 uid 下再无任何残留进程(run 结束后的零残留)。
func assertNoUidProcesses(t *testing.T, uid uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		count := uidProcessCount(uid)
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uid %d 下仍有 %d 个残留进程", uid, count)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// isolationEnv 是同一次集成测试共享的环境:所有会话必须共用一个 Store
// 和 runner,才能从同一身份池中分到不同槽位。假 Pi 的记录文件按各自
// HOME 解析,因此每个会话都写自己的 pi-agent/record.jsonl。
type isolationEnv struct {
	runner *Runner
	store  *session.Store
	runs   *runlog.Registry
	issuer *fakeIssuer
}

// newIsolationEnv 只搭建共享环境,不预建会话。
func newIsolationEnv(t *testing.T) *isolationEnv {
	t.Helper()
	root := t.TempDir()
	makeTraversable(t, root)
	makeTraversable(t, filepath.Dir(os.Args[0])) // 测试二进制即假 Pi,需可穿越
	dataDir := filepath.Join(root, "agent")
	if err := isolation.PrepareDataDir(dataDir); err != nil {
		t.Fatalf("PrepareDataDir: %v", err)
	}
	store := session.NewStore(filepath.Join(dataDir, "sessions"))
	fixturePath, err := filepath.Abs("../testdata/pi-events/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sessionSource, err := filepath.Abs("../session/testdata/fork.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	extension := filepath.Join(root, "extension", "xlyra.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extension, []byte("// fake xlyra extension\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--fake-pi",
		"--fake-record", "record.jsonl", "--fake-fixture", fixturePath, "--fake-session-src", sessionSource,
		// A、B 共用一份参数,慢速事件流既允许 A 正常完成,也便于稳定 Stop B。
		"--fake-delay-ms", "20",
	}
	issuer := &fakeIssuer{}
	runs := runlog.NewRegistry()
	runner, err := New(Config{
		Store: store, Runs: runs, Tokens: issuer,
		Protocol: func(context.Context, string) (string, error) {
			return "openai-chat-completions", nil
		},
		GatewayBaseURL: "http://127.0.0.1:18080", Command: os.Args[0], Args: args,
		PathEnv: os.Getenv("PATH"), ExtensionPath: extension,
		RunTimeout: 30 * time.Second, AbortGrace: 2 * time.Second,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		// 被测对象:开启隔离,身份池取默认(UIDBase=20000)。
		Isolation: &IsolationConfig{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &isolationEnv{runner: runner, store: store, runs: runs, issuer: issuer}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runner.Shutdown(ctx)
	})
	return env
}

// newSession 在共享 Store 中创建会话;槽位要等首次 run 的 prepare 才分配。
func (env *isolationEnv) newSession(t *testing.T) string {
	t.Helper()
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return meta.ID
}

// identityOfSession 从 meta 的槽位推出会话身份。
func identityOfSession(t *testing.T, store *session.Store, id string) isolation.Identity {
	t.Helper()
	meta, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := isolation.IdentityFor(isolation.Config{}, meta.Slot)
	if err != nil {
		t.Fatalf("IdentityFor(slot=%d): %v", meta.Slot, err)
	}
	return identity
}

// startRecordOf 读取记录文件里唯一一条 start 记录。
func startRecordOf(t *testing.T, record string) map[string]any {
	t.Helper()
	for _, rec := range readRecords(t, record) {
		if rec["kind"] == "start" {
			return rec
		}
	}
	t.Fatal("没有启动记录")
	return nil
}

// probeRecordOf 读取记录文件里唯一一条目录探测记录。
func probeRecordOf(t *testing.T, record string) map[string]any {
	t.Helper()
	for _, rec := range readRecords(t, record) {
		if rec["kind"] == "probe" {
			return rec
		}
	}
	t.Fatal("没有目录探测记录")
	return nil
}

// TestRunnerIsolationIntegration 完整 Send:Pi 以会话身份运行,目录布局正确,
// 会话之间不能互读私有目录,正常结束与 Stop 后零残留。
func TestRunnerIsolationIntegration(t *testing.T) {
	requireRunnerIntegration(t)

	env := newIsolationEnv(t)
	sessionA := env.newSession(t)
	sessionB := env.newSession(t)
	// 两个会话都创建后才能确定探测路径;首个 run 前补进共享假 Pi 参数。
	probePath := filepath.Join(env.store.Dir(sessionB), "pi-agent")
	env.runner.cfg.Args = append(env.runner.cfg.Args, "--fake-probe-path", probePath)

	// 先让 B 正常跑完一轮,使其 pi-agent/ 已归 B 身份所有;A 随后的探测
	// 才是真实的权限拒绝,而不是目录尚不存在。B 的 uid 也只能此刻之后读取。
	if _, err := env.runner.Send(context.Background(), SendInput{
		SessionID: sessionB, Content: "准备会话 B", Model: "mock-1",
	}); err != nil {
		t.Fatalf("Send B 首轮: %v", err)
	}
	if events := collectRun(t, env.runs, sessionB).wait(t); len(events) == 0 || events[len(events)-1].Type != "agent_done" {
		t.Fatalf("B 首轮终态应为 agent_done: %v", eventTypes(events))
	}
	identityB := identityOfSession(t, env.store, sessionB)
	recordB := filepath.Join(env.store.Dir(sessionB), "pi-agent", "record.jsonl")
	startB := startRecordOf(t, recordB)
	if uid, _ := startB["uid"].(float64); uint32(uid) != identityB.PiUID {
		t.Fatalf("会话 B 的 Pi uid = %v, want %d", startB["uid"], identityB.PiUID)
	}
	if gid, _ := startB["gid"].(float64); uint32(gid) != identityB.GID {
		t.Fatalf("会话 B 的 Pi gid = %v, want %d", startB["gid"], identityB.GID)
	}
	if ok, _ := probeRecordOf(t, recordB)["ok"].(bool); !ok {
		t.Fatal("正向对照失败:B 应能列出自己的 pi-agent/")
	}

	// 会话 A 正常跑完一轮,并尝试以 A 的身份列出 B 的私有目录。
	if _, err := env.runner.Send(context.Background(), SendInput{
		SessionID: sessionA, Content: "你好", Model: "mock-1",
	}); err != nil {
		t.Fatalf("Send A: %v", err)
	}
	events := collectRun(t, env.runs, sessionA).wait(t)
	if len(events) == 0 || events[len(events)-1].Type != "agent_done" {
		t.Fatalf("A 终态应为 agent_done: %v", eventTypes(events))
	}
	identityA := identityOfSession(t, env.store, sessionA)
	if identityA.PiUID == identityB.PiUID {
		t.Fatalf("两个会话的 uid 不应相同: %d", identityA.PiUID)
	}
	recordA := filepath.Join(env.store.Dir(sessionA), "pi-agent", "record.jsonl")
	startA := startRecordOf(t, recordA)
	if uid, _ := startA["uid"].(float64); uint32(uid) != identityA.PiUID {
		t.Fatalf("Pi 进程 uid = %v, want %d", startA["uid"], identityA.PiUID)
	}
	if gid, _ := startA["gid"].(float64); uint32(gid) != identityA.GID {
		t.Fatalf("Pi 进程 gid = %v, want %d", startA["gid"], identityA.GID)
	}
	probeA := probeRecordOf(t, recordA)
	if ok, _ := probeA["ok"].(bool); ok {
		t.Fatalf("A 不应能列出 B 的 pi-agent/: %+v", probeA)
	}
	if denied, _ := probeA["permission_denied"].(bool); !denied {
		t.Fatalf("A 列 B 目录应因权限不足失败: %+v", probeA)
	}

	// HOME 指向 Pi 自己的 pi-agent/(会话目录归 root,Pi 不可写)。
	agentDir := filepath.Join(env.store.Dir(sessionA), "pi-agent")
	foundHome := false
	for _, value := range startA["env"].([]any) {
		if home, ok := value.(string); ok && strings.HasPrefix(home, "HOME=") {
			foundHome = true
			if home != "HOME="+agentDir {
				t.Fatalf("HOME = %q, want %q", home, "HOME="+agentDir)
			}
		}
	}
	if !foundHome {
		t.Fatal("启动环境缺少 HOME")
	}

	// 目录布局:pi/、pi-agent/、workspace/ 归会话身份,meta.json 归 root。
	for name, wantPerm := range map[string]os.FileMode{"pi": 0o700, "pi-agent": 0o700, "workspace": 0o770} {
		uid, gid, perm := statOwner(t, filepath.Join(env.store.Dir(sessionA), name))
		if uid != identityA.PiUID || gid != identityA.GID || perm != wantPerm {
			t.Fatalf("%s 应为 %d:%d %o,得到 %d:%d %o", name, identityA.PiUID, identityA.GID, wantPerm, uid, gid, perm)
		}
	}
	// runner 写出的文件归会话身份;令牌 0600;meta.json 仍归 root。
	for _, name := range []string{"token", "models.json", "settings.json", "xlyra.json", "system-prompt.md", "record.jsonl"} {
		uid, gid, perm := statOwner(t, filepath.Join(agentDir, name))
		if uid != identityA.PiUID || gid != identityA.GID {
			t.Fatalf("pi-agent/%s 应归 %d:%d,得到 %d:%d", name, identityA.PiUID, identityA.GID, uid, gid)
		}
		if name == "token" && perm != 0o600 {
			t.Fatalf("token 权限 = %o, want 600", perm)
		}
	}
	uid, _, perm := statOwner(t, filepath.Join(env.store.Dir(sessionA), "meta.json"))
	if uid != 0 || perm != 0o600 {
		t.Fatalf("meta.json 应为 root:root 0600,得到 uid=%d %o", uid, perm)
	}
	// 会话目录与 pi/ 里的会话文件:目录归 root,内容归 Pi。
	uid, _, _ = statOwner(t, env.store.Dir(sessionA))
	if uid != 0 {
		t.Fatalf("会话目录应归 root,得到 uid=%d", uid)
	}
	piFiles, err := os.ReadDir(filepath.Join(env.store.Dir(sessionA), "pi"))
	if err != nil || len(piFiles) == 0 {
		t.Fatalf("Pi 会话文件应已写入: %v", err)
	}
	for _, f := range piFiles {
		fuid, _, _ := statOwner(t, filepath.Join(env.store.Dir(sessionA), "pi", f.Name()))
		if fuid != identityA.PiUID {
			t.Fatalf("pi/%s 应归 PiUID=%d,得到 %d", f.Name(), identityA.PiUID, fuid)
		}
	}
	// A 与 B 的正常 run 均已结束,两个身份下都应零残留。
	assertNoUidProcesses(t, identityA.PiUID)
	assertNoUidProcesses(t, identityA.ExecUID)
	assertNoUidProcesses(t, identityB.PiUID)
	assertNoUidProcesses(t, identityB.ExecUID)

	// B 再跑一轮慢速运行并 Stop,身份复用且中止后同样零残留。
	if _, err := env.runner.Send(context.Background(), SendInput{
		SessionID: sessionB, Content: "慢运行", Model: "mock-1",
	}); err != nil {
		t.Fatalf("Send B 第二轮: %v", err)
	}
	collected := collectRun(t, env.runs, sessionB)
	waitForEvent(t, collected, "text_delta")
	if err := env.runner.Stop(context.Background(), sessionB); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if events := collected.wait(t); events[len(events)-1].Type != "agent_cancelled" {
		t.Fatalf("Stop 后终态 = %s, want agent_cancelled", events[len(events)-1].Type)
	}
	assertNoUidProcesses(t, identityA.PiUID)
	assertNoUidProcesses(t, identityA.ExecUID)
	assertNoUidProcesses(t, identityB.PiUID)
	assertNoUidProcesses(t, identityB.ExecUID)
}
