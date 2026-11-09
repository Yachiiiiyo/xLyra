package execd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/agent/sandbox"
)

func TestMain(m *testing.M) {
	if sandbox.MaybeRunHelper() {
		os.Exit(2) // helper 内部已退出,这里不可达
	}
	if os.Getenv("EXECD_TEST_CLIENT") != "" {
		os.Exit(clientProbeMain())
	}
	os.Exit(m.Run())
}

// clientProbeMain 由 Linux 集成测试以指定 Credential 再执行测试二进制,
// 避免在 Go 测试进程里全局 setuid。
func clientProbeMain() int {
	req := request{Cmd: os.Getenv("EXECD_TEST_CLIENT_CMD"), Cwd: os.Getenv("EXECD_TEST_CLIENT_CWD")}
	stdout, stderr, exit, err := runSocketRequest(os.Getenv("EXECD_TEST_CLIENT_SOCK"), req)
	result := struct {
		Stdout string  `json:"stdout"`
		Stderr string  `json:"stderr"`
		Exit   exitMsg `json:"exit"`
		Error  string  `json:"error"`
	}{Stdout: string(stdout), Stderr: string(stderr), Exit: exit}
	if err != nil {
		result.Error = err.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return 2
	}
	if result.Error != "" {
		return 1
	}
	return 0
}

func TestRequestParsingAndLimits(t *testing.T) {
	req, err := parseRequest([]byte(`{"cmd":"echo hi","cwd":"/w","timeout":3}`))
	if err != nil || req.Cmd != "echo hi" || req.Cwd != "/w" || req.Timeout != 3 {
		t.Fatalf("请求解析不正确: %+v, %v", req, err)
	}
	if _, err := parseRequest([]byte(`{"cmd":`)); err == nil {
		t.Fatal("畸形 JSON 应被拒绝")
	}
	if _, err := parseRequest([]byte(`{"cmd":""}`)); !errors.Is(err, errEmptyCmd) {
		t.Fatalf("空命令错误 = %v", err)
	}
	long, err := json.Marshal(request{Cmd: strings.Repeat("x", maxCommandBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRequest(long); !errors.Is(err, errCmdTooLong) || err.Error() != "命令过长" {
		t.Fatalf("超长命令错误 = %v", err)
	}
}

func TestReadRequestLine(t *testing.T) {
	line, err := readRequestLine(strings.NewReader("  {\"cmd\":\"true\"}\nignored"))
	if err != nil || string(line) != `{"cmd":"true"}` {
		t.Fatalf("读取请求行 = %q, %v", line, err)
	}
	_, err = readRequestLine(strings.NewReader(strings.Repeat("x", maxRequestLineBytes+1)))
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("超过 1 MiB 应返回请求行过长,得到 %v", err)
	}
	exact := strings.Repeat("x", maxRequestLineBytes)
	if _, err := readRequestLine(strings.NewReader(exact)); err != nil {
		t.Fatalf("恰好 1 MiB 的行不应被行上限拒绝: %v", err)
	}
}

func TestProtocolJSONWireFormat(t *testing.T) {
	chunk, err := json.Marshal(chunkMsg{T: "out", D: base64.StdEncoding.EncodeToString([]byte("abc"))})
	if err != nil || string(chunk) != `{"t":"out","d":"YWJj"}` {
		t.Fatalf("输出消息格式 = %s, %v", chunk, err)
	}
	exit, err := json.Marshal(exitMsg{T: "exit", Code: 137, Signal: "KILL", TimedOut: false, Reaped: 2, Error: ""})
	if err != nil || string(exit) != `{"t":"exit","code":137,"signal":"KILL","timed_out":false,"reaped":2,"error":""}` {
		t.Fatalf("exit 消息格式 = %s, %v", exit, err)
	}
}

func TestConfigDefaultsAndSockDirValidation(t *testing.T) {
	root := t.TempDir()
	e, err := New(Config{SessionDir: root, Workspace: root, PiUID: 10, ExecUID: 11, GID: 10})
	if err != nil {
		t.Fatal(err)
	}
	if e.cfg.SockDir != filepath.Join(root, "exec") || e.cfg.Tmp != filepath.Join(root, "exec-tmp") {
		t.Fatalf("默认目录不正确: %+v", e.cfg)
	}
	if e.cfg.DefaultTimeout != 120*time.Second || e.cfg.MaxTimeout != 600*time.Second || e.cfg.MaxOutputBytes != 16<<20 {
		t.Fatalf("默认超时/输出上限不正确: %+v", e.cfg)
	}
	wantLimits := sandbox.Limits{NProc: 256, CPU: 600, NoFile: 1024, FSize: 512 << 20, Core: 0}
	if e.cfg.Limits != wantLimits {
		t.Fatalf("默认 Limits = %+v, want %+v", e.cfg.Limits, wantLimits)
	}
	if got := e.commandTimeout(0); got != 120*time.Second {
		t.Fatalf("缺省 timeout = %v", got)
	}
	if got := e.commandTimeout(1000); got != 600*time.Second {
		t.Fatalf("超上限 timeout = %v", got)
	}
	if got := e.commandTimeout(3); got != 3*time.Second {
		t.Fatalf("显式 timeout = %v", got)
	}
	for _, sockDir := range []string{root, filepath.Dir(root), filepath.Join(root, "..", "escape")} {
		_, err := New(Config{SessionDir: root, SockDir: sockDir, Workspace: root, PiUID: 10, ExecUID: 11, GID: 10})
		if err == nil {
			t.Fatalf("SockDir %q 越界应被拒绝", sockDir)
		}
	}
	if _, err := New(Config{SessionDir: "relative", Workspace: root, PiUID: 10, ExecUID: 11, GID: 10}); err == nil {
		t.Fatal("相对 SessionDir 应被拒绝")
	}
}

func TestResolveCwdAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{ws, outside, filepath.Join(ws, "sub")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "sub"), filepath.Join(ws, "inside")); err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{SessionDir: root, Workspace: ws, PiUID: 10, ExecUID: 11, GID: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{"", ".", "sub", "inside", ws} {
		resolved, err := e.resolveCwd(cwd)
		if err != nil || !pathInside(mustEval(t, ws), resolved) {
			t.Fatalf("cwd %q 应合法,得到 %q, %v", cwd, resolved, err)
		}
	}
	if _, err := e.resolveCwd("escape"); !errors.Is(err, errCwdOutside) {
		t.Fatalf("符号链接逃逸错误 = %v", err)
	}
	if _, err := e.resolveCwd(outside); !errors.Is(err, errCwdOutside) {
		t.Fatalf("绝对路径逃逸错误 = %v", err)
	}
	if _, err := e.resolveCwd("missing"); !errors.Is(err, errCwdMissing) {
		t.Fatalf("不存在目录错误 = %v", err)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestOutputCounter(t *testing.T) {
	counter := &outputCounter{max: 10}
	if total, over := counter.add(6); total != 6 || over {
		t.Fatalf("首次计数 = %d, %v", total, over)
	}
	if total, over := counter.add(4); total != 10 || over {
		t.Fatalf("恰好上限 = %d, %v", total, over)
	}
	if total, over := counter.add(1); total != 11 || !over {
		t.Fatalf("超过上限 = %d, %v", total, over)
	}
	if counter.value() != 11 {
		t.Fatal("计数读取不一致")
	}
}

func TestAuditTruncationAndJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewFileAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("c", maxAuditCommandBytes+100)
	sink.Record(AuditEvent{Time: time.Unix(1, 0), RunID: "r", Session: "s", Command: long, Cwd: "/w", ExitCode: 7, OutputBytes: 12})
	sink.Record(AuditEvent{Time: time.Unix(2, 0), Command: "true", TimedOut: true})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("JSONL 行数 = %d", len(lines))
	}
	var first AuditEvent
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Command != strings.Repeat("c", maxAuditCommandBytes)+"…[truncated]" || first.ExitCode != 7 || first.OutputBytes != 12 {
		t.Fatalf("审计字段不正确: %+v", first)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("审计文件权限 = %v, %v", fi.Mode(), err)
	}
}
