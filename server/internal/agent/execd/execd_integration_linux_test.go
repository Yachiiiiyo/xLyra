//go:build linux

package execd

// 执行器容器集成测试:只在 Linux + root + EXECD_INTEGRATION=1 时运行。
// 测试二进制同时充当 sandbox helper、生产探针和指定 uid 的小客户端。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/sandbox"
)

func requireExecdIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("EXECD_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + EXECD_INTEGRATION=1")
	}
}

func makeExecdTraversable(t *testing.T, dir string) {
	t.Helper()
	stop := filepath.Clean(os.TempDir())
	for cur := filepath.Clean(dir); cur != stop && cur != string(filepath.Separator) && cur != "."; cur = filepath.Dir(cur) {
		if err := os.Chmod(cur, 0o711); err != nil {
			t.Fatal(err)
		}
	}
}

type testExecutor struct {
	ex   *Executor
	sock string
	ws   string
	id   isolation.Identity
}

func newTestExecutor(t *testing.T, slot int, audit AuditSink) *testExecutor {
	t.Helper()
	root := t.TempDir()
	makeExecdTraversable(t, root)
	makeExecdTraversable(t, filepath.Dir(os.Args[0]))
	ic := isolation.Config{UIDBase: 32000, MaxSlots: 200}
	id, err := isolation.IdentityFor(ic, slot)
	if err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(root, "sess")
	if err := isolation.PrepareSession(sess, id); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(sess, "workspace")
	ex, err := New(Config{Exe: os.Args[0], SessionDir: sess, PiUID: int(id.PiUID), ExecUID: int(id.ExecUID), GID: int(id.GID), Workspace: ws, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	sock, err := ex.Start()
	if err != nil {
		t.Fatal(err)
	}
	env := &testExecutor{ex: ex, sock: sock, ws: ws, id: id}
	t.Cleanup(func() {
		_ = ex.Close()
		_, _, _ = sandbox.KillUID(int(id.ExecUID))
		_, _, _ = sandbox.KillUID(int(id.PiUID))
	})
	return env
}

func waitExecdZero(t *testing.T, uid uint32) {
	t.Helper()
	if !waitUIDZero(uid, 8*time.Second) {
		t.Fatalf("uid %d 仍有存活进程", uid)
	}
}

type clientResult struct {
	Stdout string  `json:"stdout"`
	Stderr string  `json:"stderr"`
	Exit   exitMsg `json:"exit"`
	Error  string  `json:"error"`
}

func runCredClient(t *testing.T, env *testExecutor, uid uint32, cmd, cwd string) clientResult {
	t.Helper()
	child := exec.Command(os.Args[0])
	child.Env = []string{
		"EXECD_TEST_CLIENT=1", "EXECD_TEST_CLIENT_SOCK=" + env.sock,
		"EXECD_TEST_CLIENT_CMD=" + cmd, "EXECD_TEST_CLIENT_CWD=" + cwd,
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: env.id.GID, Groups: []uint32{}}}
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	runErr := child.Run()
	var result clientResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		t.Fatalf("客户端输出无法解析:%v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if result.Error == "" && runErr != nil {
		t.Fatalf("客户端异常:%v, stderr=%q", runErr, stderr.String())
	}
	return result
}

func TestExecProtocolAndSocketPermissions(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 1, nil)

	pi := runCredClient(t, env, env.id.PiUID, "echo hi; id -u", "")
	if pi.Exit.Code != 0 || pi.Exit.Error != "" || !strings.Contains(pi.Stdout, "hi\n") || !strings.Contains(pi.Stdout, strconv.Itoa(int(env.id.ExecUID))) {
		t.Fatalf("PiUID 端到端结果不正确: %+v", pi)
	}
	denied := runCredClient(t, env, env.id.ExecUID, "echo should-not-run", "")
	if denied.Error == "" {
		t.Fatalf("ExecUID 不应能连接执行器套接字: %+v", denied)
	}

	file := filepath.Join(env.ws, "group-file")
	_, _, exit, err := runSocketRequest(env.sock, request{Cmd: "printf data > group-file"})
	if err != nil || exit.Code != 0 || exit.Error != "" {
		t.Fatalf("写工作区失败: exit=%+v err=%v", exit, err)
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != env.id.ExecUID || st.Gid != env.id.GID || fi.Mode().Perm() != 0o664 {
		t.Fatalf("工作区文件身份/权限 = %d:%d %o", st.Uid, st.Gid, fi.Mode().Perm())
	}
	cat := exec.Command("/bin/cat", file)
	cat.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: env.id.PiUID, Gid: env.id.GID, Groups: []uint32{}}}
	if output, err := cat.CombinedOutput(); err != nil || string(output) != "data" {
		t.Fatalf("PiUID 应能读命令写出的文件:%v, %q", err, output)
	}

	_, _, exit, err = runSocketRequest(env.sock, request{Cmd: "true", Cwd: filepath.Join(env.ws, "..", "..")})
	if err != nil || exit.Error != errCwdOutside.Error() {
		t.Fatalf("cwd 逃逸应被拒绝: exit=%+v err=%v", exit, err)
	}
	_, _, exit, err = runSocketRequest(env.sock, request{Cmd: "true", Cwd: filepath.Join(env.ws, "missing")})
	if err != nil || exit.Error != errCwdMissing.Error() {
		t.Fatalf("不存在 cwd 应被拒绝: exit=%+v err=%v", exit, err)
	}
	waitExecdZero(t, env.id.ExecUID)
}

func TestExecTimeoutAndDisconnect(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 2, nil)
	_, _, exit, err := runSocketRequest(env.sock, request{Cmd: "sleep 300", Timeout: 2})
	if err != nil || !exit.TimedOut || exit.Code != 124 || exit.Error != "" {
		t.Fatalf("超时结果不正确: exit=%+v err=%v", exit, err)
	}
	waitExecdZero(t, env.id.ExecUID)

	conn, err := net.Dial("unix", env.sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(request{Cmd: "sleep 300", Timeout: 30}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = conn.Close()
	waitExecdZero(t, env.id.ExecUID)
	stdout, _, exit, err := runSocketRequest(env.sock, request{Cmd: "printf next"})
	if err != nil || exit.Code != 0 || string(stdout) != "next" {
		t.Fatalf("断开后下一条命令应正常执行: %q %+v %v", stdout, exit, err)
	}
}

func TestExecBackgroundZeroResidual(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 3, nil)
	for _, cmd := range []string{"/bin/sleep 300 &", "setsid /bin/sleep 300 &", "(/bin/sleep 300 &) &"} {
		_, _, exit, err := runSocketRequest(env.sock, request{Cmd: cmd, Timeout: 10})
		if err != nil || exit.Code != 0 || exit.Error != "" {
			t.Fatalf("后台命令 %q 失败: %+v %v", cmd, exit, err)
		}
		waitExecdZero(t, env.id.ExecUID)
	}
}

func TestExecOutputLimit(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 4, nil)
	_, _, exit, err := runSocketRequest(env.sock, request{Cmd: "head -c 20971520 /dev/zero", Timeout: 30})
	if err != nil || exit.Error != errOutputLimit.Error() {
		t.Fatalf("20 MiB 输出应触发上限: exit=%+v err=%v", exit, err)
	}
	waitExecdZero(t, env.id.ExecUID)
}

func TestExecSerialConnections(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 5, nil)
	type result struct {
		stdout string
		exit   exitMsg
		err    error
		at     time.Time
	}
	run := func(cmd string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			stdout, _, exit, err := runSocketRequest(env.sock, request{Cmd: cmd, Timeout: 20})
			ch <- result{stdout: string(stdout), exit: exit, err: err, at: time.Now()}
		}()
		return ch
	}
	first := run("sleep 2; printf first")
	time.Sleep(200 * time.Millisecond)
	second := run("printf second")
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.stdout != "first" || b.stdout != "second" {
		t.Fatalf("串行命令结果不正确: %+v %+v", a, b)
	}
	if b.at.Before(a.at) {
		t.Fatalf("第二条命令不应先于第一条完成: first=%v second=%v", a.at, b.at)
	}
}

func TestExecClose(t *testing.T) {
	requireExecdIntegration(t)
	env := newTestExecutor(t, 6, nil)
	conn, err := net.Dial("unix", env.sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(request{Cmd: "sleep 300"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := env.ex.Close(); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := os.Lstat(env.sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close 后套接字应消失: %v", err)
	}
	waitExecdZero(t, env.id.ExecUID)
}

func TestExecExecutorsIndependent(t *testing.T) {
	requireExecdIntegration(t)
	a := newTestExecutor(t, 7, nil)
	b := newTestExecutor(t, 8, nil)
	conn, err := net.Dial("unix", a.sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(request{Cmd: "sleep 300"}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		stdout string
		exit   exitMsg
		err    error
	}
	done := make(chan result, 1)
	go func() {
		stdout, _, exit, err := runSocketRequest(b.sock, request{Cmd: "sleep 2; printf B", Timeout: 10})
		done <- result{stdout: string(stdout), exit: exit, err: err}
	}()
	time.Sleep(300 * time.Millisecond)
	if err := a.ex.Close(); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	got := <-done
	if got.err != nil || got.exit.Code != 0 || got.stdout != "B" {
		t.Fatalf("另一个 Executor 的命令不应受影响: %+v", got)
	}
	waitExecdZero(t, a.id.ExecUID)
	waitExecdZero(t, b.id.ExecUID)
}

func TestExecAuditFile(t *testing.T) {
	requireExecdIntegration(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewFileAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	env := newTestExecutor(t, 9, sink)
	env.ex.SetRun("run-1")
	_, _, _, _ = runSocketRequest(env.sock, request{Cmd: "printf audited"})
	_, _, _, _ = runSocketRequest(env.sock, request{Cmd: "true", Cwd: filepath.Join(env.ws, "..", "..")})
	_, _, _, _ = runSocketRequest(env.sock, request{Cmd: "sleep 300", Timeout: 2})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("审计记录数 = %d, 内容=%s", len(lines), data)
	}
	var events []AuditEvent
	for _, line := range lines {
		var event AuditEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if events[0].RunID != "run-1" || events[0].Command != "printf audited" || events[0].ExitCode != 0 || events[0].OutputBytes != 7 || events[0].Duration <= 0 {
		t.Fatalf("正常命令审计字段不合理: %+v", events[0])
	}
	if events[1].ExitCode != 1 || events[1].Error != errCwdOutside.Error() {
		t.Fatalf("拒绝命令审计字段不合理: %+v", events[1])
	}
	if !events[2].TimedOut || events[2].ExitCode != 124 {
		t.Fatalf("超时审计字段不合理: %+v", events[2])
	}
}

func selfCheckFixture(t *testing.T, worldReadable bool) (string, CheckInput) {
	t.Helper()
	// 套接字路径受 sun_path 108 字节限制,t.TempDir 带测试名太长,改用短目录
	root, err := os.MkdirTemp("", "xc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	makeExecdTraversable(t, root)
	makeExecdTraversable(t, filepath.Dir(os.Args[0]))
	dataDir := filepath.Join(root, "agent")
	conf := filepath.Join(root, "conf")
	if err := os.MkdirAll(conf, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(conf, "master.key")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if worldReadable {
		if err := os.Chmod(conf, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(secret, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir, CheckInput{
		DataDir: dataDir, SecretPaths: []string{secret, filepath.Join(conf, "missing")},
		Exe: os.Args[0], Isolation: isolation.Config{UIDBase: 33000, MaxSlots: 100},
	}
}

func logExecdReport(t *testing.T, report isolation.Report) {
	t.Helper()
	for _, check := range report.Checks {
		t.Logf("%s passed=%v detail=%s", check.Name, check.Passed, check.Detail)
	}
}

func requireExecdFailed(t *testing.T, report isolation.Report, name string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			if check.Passed {
				t.Fatalf("检查项 %q 预期失败但通过", name)
			}
			return
		}
	}
	t.Fatalf("报告缺少检查项 %q", name)
}

func TestExecSelfCheckOK(t *testing.T) {
	requireExecdIntegration(t)
	dataDir, in := selfCheckFixture(t, false)
	in.DataDir = dataDir
	report := SelfCheck(context.Background(), in)
	logExecdReport(t, report)
	if !report.OK {
		t.Fatal("容器内完整自检应通过")
	}
	if len(report.Checks) != 12 {
		t.Fatalf("自检应有 12 项,得到 %d", len(report.Checks))
	}
}

func TestExecSelfCheckInjectedFailure(t *testing.T) {
	requireExecdIntegration(t)
	dataDir, in := selfCheckFixture(t, true)
	in.DataDir = dataDir
	oldHook := runSpecHook
	runSpecHook = func(spec *sandbox.Spec) {
		spec.Control = sandbox.Control{SkipLandlock: true, SkipSeccomp: true, SkipNNP: true}
		spec.Net = ""
	}
	defer func() { runSpecHook = oldHook }()
	report := SelfCheck(context.Background(), in)
	logExecdReport(t, report)
	if report.OK {
		t.Fatal("模拟沙箱失效后自检不应通过")
	}
	requireExecdFailed(t, report, checkSecrets)
}
