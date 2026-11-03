//go:build linux

package sandbox

// 沙箱容器集成测试:需要 Linux + root + SANDBOX_INTEGRATION=1,其余环境跳过
// (设计上由 CI 的 Linux 容器执行,不在开发机运行)。
//
// 测试用被测二进制(测试二进制自己)作为被执行程序:argv=[os.Args[0]] 加
// 探针环境变量,探针在 TestMain 里分发(testProbeHook),把探测结果以 JSON
// 打到 stdout。每个"应失败"的项都有 Control 正向对照:同一探针以
// SkipLandlock/SkipSeccomp 运行时对应项成功,证明是沙箱在起作用,不是环境
// 本来就拒绝。uid 取 30000+ 段,避开 xLyra 默认的 20000 段。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("SANDBOX_INTEGRATION") != "1" || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("需要 Linux + root + SANDBOX_INTEGRATION=1")
	}
}

// makeTraversable 把 dir 及其到系统临时目录之间的祖先放宽到 0711,让目标
// uid 的子进程能穿越到达工作区与测试二进制(与 runner 集成测试同思路)。
func makeTraversable(t *testing.T, dir string) {
	t.Helper()
	stop := filepath.Clean(os.TempDir())
	for cur := filepath.Clean(dir); cur != stop && cur != string(filepath.Separator) && cur != "."; cur = filepath.Dir(cur) {
		if err := os.Chmod(cur, 0o711); err != nil {
			t.Fatal(err)
		}
	}
}

// makeOwnedDir 创建属于 uid:gid 的目录并保证祖先可穿越。
func makeOwnedDir(t *testing.T, uid, gid int) string {
	t.Helper()
	dir := t.TempDir()
	makeTraversable(t, dir)
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	return dir
}

// errnoStr 把探测错误压成可断言的短名:"ok" 或 errno 名。
func errnoStr(err error) string {
	if err == nil {
		return "ok"
	}
	var e unix.Errno
	if errors.As(err, &e) {
		if e == 0 {
			return "ok"
		}
		if name := unix.ErrnoName(e); name != "" {
			return name
		}
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// 探针模式:沙箱内的探测逻辑
// ---------------------------------------------------------------------------

func init() { testProbeHook = probeMain }

// probeMain 是探针入口:测试二进制被沙箱 exec 后经 TestMain 到这里,
// 不运行任何测试。mode 由 SANDBOX_TEST_PROBE 指定。
func probeMain(mode string) int {
	switch mode {
	case "probe":
		writeProbeJSON(runFullProbe())
		return 0
	case "sleep":
		time.Sleep(300 * time.Second)
		return 0
	case "exit":
		return 0
	case "orphan":
		// 后台 setsid 孙进程(同样以探针身份睡眠),父进程立即退出:
		// run 结束后的按 uid 清理必须把它杀掉。
		spawnProbeChild("sleep", true)
		return 0
	case "forkbomb":
		res := map[string]any{}
		n := 0
		var lastErr string
		for i := 0; i < 1000; i++ {
			cmd := spawnProbeChild("sleep", false)
			if err := cmd.Start(); err != nil {
				lastErr = errnoStr(err)
				break
			}
			n++
		}
		res["spawned"] = n
		res["failed"] = lastErr
		writeProbeJSON(res)
		return 0
	case "fsize":
		// 写超过 RLIMIT_FSIZE 的文件:捕获 SIGXFSZ 后 write 返回 EFBIG
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGXFSZ)
		f, err := os.OpenFile(os.Getenv("SANDBOX_TEST_FSIZE_PATH"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		res := map[string]any{"open": errnoStr(err)}
		if err == nil {
			buf := make([]byte, 1<<16)
			for i := 0; i < 64; i++ {
				if _, err := f.Write(buf); err != nil {
					res["write"] = errnoStr(err)
					break
				}
			}
			f.Close()
		}
		writeProbeJSON(res)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "probe: 未知模式 %q\n", mode)
		return 2
	}
}

func writeProbeJSON(v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(2)
	}
	fmt.Println(string(b))
}

// spawnProbeChild 以当前环境再执行测试二进制的指定探针模式;setsid 时孙
// 进程脱离进程组(setsid 孙进程场景)。
func spawnProbeChild(mode string, setsid bool) *exec.Cmd {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "SANDBOX_TEST_PROBE=") {
			env = append(env, e)
		}
	}
	env = append(env, "SANDBOX_TEST_PROBE="+mode)
	cmd := exec.Command(os.Args[0])
	cmd.Env = env
	if setsid {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	return cmd
}

// runFullProbe 是"全套探测":值均为 errno 名或数字,由测试端断言。
func runFullProbe() map[string]any {
	res := map[string]any{
		"uid": os.Getuid(),
		"gid": os.Getgid(),
	}
	if nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err == nil {
		res["nnp"] = nnp
	} else {
		res["nnp"] = errnoStr(err)
	}
	// 文件系统(Landlock)
	if p := os.Getenv("SANDBOX_TEST_SECRET"); p != "" {
		fd, err := unix.Open(p, unix.O_RDONLY, 0)
		res["secret"] = errnoStr(err)
		if err == nil {
			unix.Close(fd)
		}
	}
	if p := os.Getenv("SANDBOX_TEST_OUTSIDE"); p != "" {
		fd, err := unix.Open(p, unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL, 0o666)
		res["write_out"] = errnoStr(err)
		if err == nil {
			unix.Close(fd)
		}
	}
	if dir := os.Getenv("SANDBOX_TEST_WS"); dir != "" {
		fd, err := unix.Open(filepath.Join(dir, "seed.txt"), unix.O_RDONLY, 0)
		res["ws_read"] = errnoStr(err)
		if err == nil {
			unix.Close(fd)
		}
		fd, err = unix.Open(filepath.Join(dir, "probe-"+os.Getenv("SANDBOX_TEST_TAG")+".txt"), unix.O_CREAT|unix.O_WRONLY, 0o666)
		res["ws_write"] = errnoStr(err)
		if err == nil {
			unix.Close(fd)
		}
	}
	if dir := os.Getenv("SANDBOX_TEST_TMP"); dir != "" {
		fd, err := unix.Open(filepath.Join(dir, "probe-"+os.Getenv("SANDBOX_TEST_TAG")+".txt"), unix.O_CREAT|unix.O_WRONLY, 0o666)
		res["tmp_write"] = errnoStr(err)
		if err == nil {
			unix.Close(fd)
		}
		// mknod 需要能力(降权后没有),期望 EPERM——这属于身份层,不是沙箱层
		res["mknod"] = errnoStr(unix.Mknod(filepath.Join(dir, "node-"+os.Getenv("SANDBOX_TEST_TAG")), unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3))))
	}
	// 套接字(seccomp 封非 AF_UNIX;Landlock 管 TCP)
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	res["sock_dgram"] = errnoStr(err)
	if err == nil {
		unix.Close(fd)
	}
	fd, err = unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	res["sock_stream"] = errnoStr(err)
	if err == nil {
		unix.Close(fd)
	}
	fd, err = unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	res["sock_unix"] = errnoStr(err)
	if err == nil {
		unix.Close(fd)
	}
	// TCP connect/bind(端口探测;未配置端口则跳过)
	if p := os.Getenv("SANDBOX_TEST_PORT_ALLOWED"); p != "" {
		res["tcp_connect_allowed"] = probeTCPConnect(p)
	}
	if p := os.Getenv("SANDBOX_TEST_PORT_DENIED"); p != "" {
		res["tcp_connect_denied"] = probeTCPConnect(p)
		res["tcp_bind"] = probeTCPBind(p)
	}
	// ptrace:TRACEME(同 uid 对照下可成功,证明封堵来自 seccomp)
	_, _, errno := unix.Syscall(unix.SYS_PTRACE, uintptr(unix.PTRACE_TRACEME), 0, 0)
	res["ptrace"] = errnoStr(errno)
	// /proc 不在放行集:self/父进程的 environ 都读不到;对照用 self-environ
	// (同 uid 一定可读,父进程的受宿主 yama 配置影响,只记录不断言)。
	if _, err := os.ReadFile("/proc/self/environ"); err != nil {
		res["proc_environ"] = errnoStr(err)
	} else {
		res["proc_environ"] = "ok"
	}
	if _, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getppid())); err != nil {
		res["proc_ppid_environ"] = errnoStr(err)
	} else {
		res["proc_ppid_environ"] = "ok"
	}
	// 身份层
	res["setuid0"] = errnoStr(unix.Setuid(0))
	// 设备文件:只按文件放行
	fd, err = unix.Open("/dev/null", unix.O_RDWR, 0)
	res["dev_null"] = errnoStr(err)
	if err == nil {
		unix.Close(fd)
	}
	fd, err = unix.Open("/dev/mem", unix.O_RDONLY, 0)
	res["dev_mem"] = errnoStr(err)
	if err == nil {
		unix.Close(fd)
	}
	// 资源限制自报
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_CORE, &lim); err == nil {
		res["core_limit"] = lim.Cur
	}
	return res
}

func probeTCPConnect(portStr string) string {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "bad-port"
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return errnoStr(err) // seccomp 封 socket 的场景
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	return errnoStr(unix.Connect(fd, sa))
}

func probeTCPBind(portStr string) string {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "bad-port"
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return errnoStr(err)
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	return errnoStr(unix.Bind(fd, sa))
}

// ---------------------------------------------------------------------------
// 测试端辅助
// ---------------------------------------------------------------------------

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	makeTraversable(t, filepath.Dir(exe))
	return exe
}

// runSpec 以 RunCommand 执行一次探针并解析 JSON 结果;mode 写入
// SANDBOX_TEST_PROBE(覆盖已有值,env 里重复键以先出现者为准)。
func runSpec(t *testing.T, spec Spec, mode string, extraEnv ...string) (map[string]any, string, int) {
	t.Helper()
	exe := testExe(t)
	env := make([]string, 0, len(spec.Env)+len(extraEnv)+1)
	for _, e := range spec.Env {
		if !strings.HasPrefix(e, "SANDBOX_TEST_PROBE=") {
			env = append(env, e)
		}
	}
	env = append(env, extraEnv...)
	env = append(env, "SANDBOX_TEST_PROBE="+mode)
	spec.Env = env
	cmd, err := RunCommand(exe, spec, []string{exe})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			t.Fatalf("运行探针失败:%v", runErr)
		}
		code = ee.ExitCode()
	}
	var res map[string]any
	if s := strings.TrimSpace(stdout.String()); s != "" {
		if err := json.Unmarshal([]byte(s), &res); err != nil {
			t.Fatalf("解析探针输出 %q:%v", s, err)
		}
	}
	return res, stderr.String(), code
}

func wantStr(t *testing.T, res map[string]any, key, want string) {
	t.Helper()
	got, _ := res[key].(string)
	if got != want {
		t.Errorf("%s = %q,期望 %q(完整结果:%v)", key, got, want, res)
	}
}

func wantNotOK(t *testing.T, res map[string]any, key string) {
	t.Helper()
	got, _ := res[key].(string)
	if got == "ok" {
		t.Errorf("%s 不应成功(完整结果:%v)", key, res)
	}
}

func wantInt(t *testing.T, res map[string]any, key string, want int) {
	t.Helper()
	got, ok := res[key].(float64)
	if !ok || int(got) != want {
		t.Errorf("%s = %v,期望 %d(完整结果:%v)", key, res[key], want, res)
	}
}

// aliveUID 统计 uid 的存活(非僵尸)进程数。
func aliveUID(uid int) int {
	n := 0
	for _, p := range ProcsOfUID(uid) {
		if p.State != "Z" {
			n++
		}
	}
	return n
}

func waitAlive(t *testing.T, uid, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if aliveUID(uid) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uid %d 存活进程不足:%d < %d", uid, aliveUID(uid), want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitZero(t *testing.T, uid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if aliveUID(uid) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uid %d 仍有 %d 个存活进程", uid, aliveUID(uid))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// cleanupUID 在测试结束时兜底清理该 uid,失败也不阻塞。
func cleanupUID(t *testing.T, uid int) {
	t.Helper()
	t.Cleanup(func() { _, _, _ = KillUID(uid) })
}

// startSleeper 以 ChildCommand(Pi 启动路径)在 uid 下起一个睡眠探针。
func startSleeper(t *testing.T, uid, gid int) *exec.Cmd {
	t.Helper()
	ws := makeOwnedDir(t, uid, gid)
	cmd, err := ChildCommand(testExe(t), Spec{
		UID: uid, GID: gid, Workspace: ws, Net: NetNone, Umask: -1,
		Env: []string{"PATH=/usr/bin:/bin", "SANDBOX_TEST_PROBE=sleep"},
	}, []string{os.Args[0]})
	if err != nil {
		t.Fatalf("ChildCommand: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动睡眠探针: %v", err)
	}
	waitAlive(t, uid, 1)
	return cmd
}

// ---------------------------------------------------------------------------
// 集成测试
// ---------------------------------------------------------------------------

// TestSandboxDetect 集成环境(容器、root)里沙箱必须可用。
func TestSandboxDetect(t *testing.T) {
	requireIntegration(t)
	cap := Detect()
	if !cap.OK {
		t.Fatalf("沙箱应可用:%+v", cap)
	}
	if cap.LandlockABI < 4 {
		t.Fatalf("Landlock ABI %d < 4", cap.LandlockABI)
	}
}

// TestCommandModeFullSandbox 覆盖命令模式(NetNone)全部防线,并为每个
// "应失败"的项提供 Control 正向对照。
func TestCommandModeFullSandbox(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30100, 30100
	cleanupUID(t, uid)

	secretDir := t.TempDir()
	makeTraversable(t, secretDir)
	secret := filepath.Join(secretDir, "secret.key")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	makeTraversable(t, outsideDir)
	if err := os.Chmod(outsideDir, 0o777); err != nil {
		t.Fatal(err)
	}
	ws := makeOwnedDir(t, uid, gid)
	if err := os.WriteFile(filepath.Join(ws, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := makeOwnedDir(t, uid, gid)

	base := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: tmp, Net: NetNone,
		Umask:  -1,
		Limits: Limits{Core: 0},
		Env: []string{
			"PATH=/usr/bin:/bin", "HOME=" + tmp, "TMPDIR=" + tmp,
			"SANDBOX_TEST_SECRET=" + secret,
			"SANDBOX_TEST_WS=" + ws, "SANDBOX_TEST_TMP=" + tmp,
		},
	}

	// 沙箱全开
	sbx := base
	sbx.Env = append(append([]string{}, base.Env...), "SANDBOX_TEST_TAG=sbx", "SANDBOX_TEST_OUTSIDE="+filepath.Join(outsideDir, "sbx.txt"))
	res, _, code := runSpec(t, sbx, "probe")
	if code != 0 {
		t.Fatalf("探针退出码 %d", code)
	}
	wantInt(t, res, "uid", uid)
	wantInt(t, res, "gid", gid)
	wantInt(t, res, "nnp", 1)
	wantStr(t, res, "secret", "EACCES")
	wantStr(t, res, "write_out", "EACCES")
	wantStr(t, res, "ws_read", "ok")
	wantStr(t, res, "ws_write", "ok")
	wantStr(t, res, "tmp_write", "ok")
	wantStr(t, res, "sock_dgram", "EPERM")
	wantStr(t, res, "sock_stream", "EPERM")
	wantStr(t, res, "sock_unix", "ok")
	wantStr(t, res, "ptrace", "EPERM")
	wantStr(t, res, "setuid0", "EPERM")
	wantStr(t, res, "mknod", "EPERM")
	wantStr(t, res, "dev_null", "ok")
	wantNotOK(t, res, "dev_mem")
	wantNotOK(t, res, "proc_environ")
	wantNotOK(t, res, "proc_ppid_environ")
	wantInt(t, res, "core_limit", 0)

	// Landlock 正向对照:同 uid、无 Landlock(因此也不带网络策略),
	// 文件系统防线的各项都应成功,证明是 Landlock 在起作用。
	ctlLL := base
	ctlLL.Control = Control{SkipLandlock: true}
	ctlLL.Net = ""
	ctlLL.Env = append(append([]string{}, base.Env...), "SANDBOX_TEST_TAG=ctlLL", "SANDBOX_TEST_OUTSIDE="+filepath.Join(outsideDir, "ctlLL.txt"))
	res, _, code = runSpec(t, ctlLL, "probe")
	if code != 0 {
		t.Fatalf("Landlock 对照退出码 %d", code)
	}
	wantStr(t, res, "secret", "ok")
	wantStr(t, res, "write_out", "ok")
	wantStr(t, res, "proc_environ", "ok")
	// 身份层与设备层对照:这些失败与 Landlock 无关,对照下依旧失败
	wantStr(t, res, "setuid0", "EPERM")
	wantStr(t, res, "mknod", "EPERM")
	wantNotOK(t, res, "dev_mem")

	// seccomp 正向对照:Landlock 照常(NetNone 仍拒绝 TCP),
	// 套接字创建与 ptrace 恢复,证明它们此前被 seccomp 拦截。
	ctlSC := base
	ctlSC.Control = Control{SkipSeccomp: true}
	ctlSC.Env = append(append([]string{}, base.Env...), "SANDBOX_TEST_TAG=ctlSC")
	res, _, code = runSpec(t, ctlSC, "probe")
	if code != 0 {
		t.Fatalf("seccomp 对照退出码 %d", code)
	}
	wantStr(t, res, "sock_dgram", "ok")
	wantStr(t, res, "sock_stream", "ok")
	wantStr(t, res, "ptrace", "ok")
	wantStr(t, res, "secret", "EACCES") // Landlock 未跳过
	// 已知事实(记录,不断言):Landlock 不管 UDP,UDP 套接字此刻可发送;
	// TCP connect 仍被 Landlock 拒(端口未放行),见 Pi 模式测试。

	// 全对照(无 NNP):身份层是最后防线
	ctlAll := base
	ctlAll.Control = Control{SkipLandlock: true, SkipSeccomp: true, SkipNNP: true}
	ctlAll.Net = ""
	ctlAll.Env = append(append([]string{}, base.Env...), "SANDBOX_TEST_TAG=ctlAll")
	res, _, code = runSpec(t, ctlAll, "probe")
	if code != 0 {
		t.Fatalf("全对照退出码 %d", code)
	}
	wantInt(t, res, "nnp", 0)
	wantStr(t, res, "setuid0", "EPERM")
	wantStr(t, res, "mknod", "EPERM")

	waitZero(t, uid)
}

// TestPiModeNetwork 覆盖 Pi 模式(NetConnectPorts):允许端口可连、其他
// 端口与 bind 被拒、socket() 不被封;UDP 不受 Landlock 约束为已知事实。
func TestPiModeNetwork(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30101, 30101
	cleanupUID(t, uid)

	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln1.Close()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	allowed := ln1.Addr().(*net.TCPAddr).Port
	denied := ln2.Addr().(*net.TCPAddr).Port

	ws := makeOwnedDir(t, uid, gid)
	spec := Spec{
		UID: uid, GID: gid, Workspace: ws, Net: NetConnectPorts,
		ConnectPorts: []int{allowed}, Umask: -1,
		Env: []string{
			"PATH=/usr/bin:/bin",
			fmt.Sprintf("SANDBOX_TEST_PORT_ALLOWED=%d", allowed),
			fmt.Sprintf("SANDBOX_TEST_PORT_DENIED=%d", denied),
		},
	}
	res, _, code := runSpec(t, spec, "probe")
	if code != 0 {
		t.Fatalf("探针退出码 %d", code)
	}
	wantStr(t, res, "tcp_connect_allowed", "ok")
	wantStr(t, res, "tcp_connect_denied", "EACCES")
	wantStr(t, res, "tcp_bind", "EACCES")
	wantStr(t, res, "sock_stream", "ok") // seccomp 不封 socket,Landlock 端口规则才有机会生效
	wantStr(t, res, "sock_dgram", "ok")  // 已知事实:Landlock 不管 UDP,此处不断言封堵
	wantStr(t, res, "sock_unix", "ok")

	// 对照:去掉 Landlock(网络策略随之撤销)后,被拒端口与 bind 都恢复,
	// 证明端口限制来自 Landlock。
	ctl := spec
	ctl.Control = Control{SkipLandlock: true}
	ctl.Net = ""
	ctl.ConnectPorts = nil // 网络策略撤销时端口列表一并清空(校验拒绝两者不匹配)
	res, _, code = runSpec(t, ctl, "probe")
	if code != 0 {
		t.Fatalf("对照退出码 %d", code)
	}
	wantStr(t, res, "tcp_connect_denied", "ok")
	// 被拒端口上的监听器仍在,bind 没被 Landlock 拒绝时得到 EADDRINUSE
	if got := res["tcp_bind"]; got != "ok" && got != "EADDRINUSE" {
		t.Fatalf("对照 tcp_bind = %q, 期望 ok 或 EADDRINUSE(不再是 EACCES)", got)
	}

	waitZero(t, uid)
}

// TestTimeoutKill 超时:run 杀整组并按 uid 清理,退出码 124、零残留。
func TestTimeoutKill(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30102, 30102
	cleanupUID(t, uid)
	ws := makeOwnedDir(t, uid, gid)
	spec := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: ws, Net: NetNone,
		Timeout: 1, Umask: -1,
		Env: []string{"PATH=/usr/bin:/bin", "SANDBOX_TEST_PROBE=sleep"},
	}
	cmd, err := RunCommand(testExe(t), spec, []string{os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	runErr := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) || ee.ExitCode() != 124 {
		t.Fatalf("期望退出码 124,得到 %v", runErr)
	}
	waitZero(t, uid)
}

// TestOrphanCleanup 清理:setsid 孙进程在 run 结束后被按 uid 清掉(退出码
// 0 表示清理成功);另一个 uid 的进程不受影响。
func TestOrphanCleanup(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30103, 30103
	const otherUID, otherGID = 30104, 30104
	cleanupUID(t, uid)
	cleanupUID(t, otherUID)

	other := startSleeper(t, otherUID, otherGID)
	defer func() {
		_, _, _ = KillUID(otherUID)
		_ = other.Wait()
	}()

	ws := makeOwnedDir(t, uid, gid)
	spec := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: ws, Net: NetNone,
		Umask: -1, Timeout: 30,
		Env: []string{"PATH=/usr/bin:/bin", "SANDBOX_TEST_PROBE=orphan"},
	}
	cmd, err := RunCommand(testExe(t), spec, []string{os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 0 {
			t.Fatalf("orphan 探针应退出 0(清理成功),得到 %v", err)
		}
	}
	// 孙进程已被 run 的按 uid 清理杀掉;另一个 uid 的睡眠进程仍在。
	waitZero(t, uid)
	if aliveUID(otherUID) < 1 {
		t.Fatal("另一个 uid 的进程不应受清理影响")
	}
}

// TestKillUIDAndSweep:KillUID 清掉目标 uid 的全部进程,拒绝 uid 0 与自身;
// SweepUIDs 按 uid 区间清理。
func TestKillUIDAndSweep(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30105, 30105
	const sweepUID, sweepGID = 30106, 30106
	cleanupUID(t, uid)
	cleanupUID(t, sweepUID)

	s1 := startSleeper(t, uid, gid)
	s2 := startSleeper(t, uid, gid)
	waitAlive(t, uid, 2)
	killed, remaining, err := KillUID(uid)
	if err != nil {
		t.Fatalf("KillUID: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("KillUID 后仍有 %d 个存活进程", remaining)
	}
	if killed < 2 {
		t.Fatalf("KillUID 应至少杀掉 2 个进程,得到 %d", killed)
	}
	_ = s1.Wait()
	_ = s2.Wait()
	waitZero(t, uid)

	// 拒绝 uid 0 与自身 euid,且不动手
	if _, _, err := KillUID(0); err == nil {
		t.Fatal("KillUID(0) 应返回错误")
	}
	if _, _, err := KillUID(os.Geteuid()); err == nil {
		t.Fatal("KillUID(自身 euid) 应返回错误")
	}

	// SweepUIDs:另一个 uid 的睡眠进程在区间内被清掉
	s3 := startSleeper(t, sweepUID, sweepGID)
	if killed := SweepUIDs(uid, 10); killed < 1 {
		t.Fatalf("SweepUIDs 应至少杀掉 1 个进程,得到 %d", killed)
	}
	waitZero(t, sweepUID)
	_ = s3.Wait()
}

// TestRlimits:NPROC 限住 fork 炸弹(另一个 uid 不受影响)、FSIZE 触发
// EFBIG、Core 为 0(core 已在命令模式探针里断言)。
func TestRlimits(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30107, 30107
	const otherUID, otherGID = 30108, 30108
	cleanupUID(t, uid)
	cleanupUID(t, otherUID)

	other := startSleeper(t, otherUID, otherGID)
	defer func() {
		_, _, _ = KillUID(otherUID)
		_ = other.Wait()
	}()

	ws := makeOwnedDir(t, uid, gid)
	// fork 炸弹:NPROC=8,探针持续拉起睡眠子进程直到 fork 失败
	bomb := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: ws, Net: NetNone,
		Umask: -1, Timeout: 60,
		Limits: Limits{NProc: 8, Core: 0},
		Env:    []string{"PATH=/usr/bin:/bin"},
	}
	res, _, code := runSpec(t, bomb, "forkbomb")
	if code != 0 {
		t.Fatalf("fork 炸弹探针退出码 %d", code)
	}
	if failed, _ := res["failed"].(string); failed != "EAGAIN" {
		t.Fatalf("fork 应被 EAGAIN 限住,得到 %v", res)
	}
	if spawned := int(res["spawned"].(float64)); spawned > 8 {
		t.Fatalf("NPROC=8 下 spawn 数 %d 不应超过限制", spawned)
	}
	waitZero(t, uid)
	if aliveUID(otherUID) < 1 {
		t.Fatal("另一个 uid 的进程不应受 NPROC 影响")
	}

	// FSIZE:超过限制的写得到 EFBIG(SIGXFSZ 已捕获)
	fsizePath := filepath.Join(ws, "big.bin")
	fsSpec := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: ws, Net: NetNone,
		Umask: -1, Timeout: 60,
		Limits: Limits{FSize: 1 << 16, Core: 0},
		Env:    []string{"PATH=/usr/bin:/bin", "SANDBOX_TEST_FSIZE_PATH=" + fsizePath},
	}
	res, _, code = runSpec(t, fsSpec, "fsize")
	if code != 0 {
		t.Fatalf("fsize 探针退出码 %d", code)
	}
	if w, _ := res["write"].(string); w != "EFBIG" {
		t.Fatalf("超限写应 EFBIG,得到 %v", res)
	}
	waitZero(t, uid)
}

// TestControlModeBanner:Control 任一项开启时,run 与 child 都要在 stderr
// 打印醒目的一行 sandbox: CONTROL MODE。
func TestControlModeBanner(t *testing.T) {
	requireIntegration(t)
	const uid, gid = 30109, 30109
	cleanupUID(t, uid)
	ws := makeOwnedDir(t, uid, gid)
	spec := Spec{
		UID: uid, GID: gid, Workspace: ws, Tmp: ws, Net: NetNone,
		Umask: -1, Control: Control{SkipSeccomp: true},
		Env: []string{"PATH=/usr/bin:/bin"},
	}
	_, stderr, code := runSpec(t, spec, "exit")
	if code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	if !strings.Contains(stderr, "sandbox: CONTROL MODE") {
		t.Fatalf("stderr 应包含 CONTROL MODE 提示,得到:%s", stderr)
	}
	waitZero(t, uid)
}
