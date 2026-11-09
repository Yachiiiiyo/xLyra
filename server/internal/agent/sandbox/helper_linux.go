package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// helperCommand 是主程序重入 helper 模式的 argv 标记;spec 以 base64(JSON)
// 内嵌,之后 "--" 分隔目标 argv。
const helperCommand = "__xlyra-sandbox"

// MaybeRunHelper 必须在 main/TestMain 的第一件事调用:argv 匹配
// `<exe> __xlyra-sandbox <mode> --spec … -- argv…` 时执行 helper 并 os.Exit
// (true 不会真的返回给调用方),不匹配返回 false 走正常启动。必须在任何
// 配置、数据库初始化之前调用,保证 helper 路径零副作用。
func MaybeRunHelper() bool {
	if len(os.Args) >= 3 && os.Args[1] == helperCommand && os.Args[2] == "probe" {
		probeHelper()
		return true // probeHelper 内部已 os.Exit,这里不可达
	}
	if len(os.Args) < 4 || os.Args[1] != helperCommand {
		return false
	}
	spec64, argv, err := parseHelperArgs(os.Args[3:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
		os.Exit(2)
	}
	switch os.Args[2] {
	case "child":
		childHelper(spec64, argv)
	case "run":
		runHelper(spec64, argv)
	default:
		fmt.Fprintf(os.Stderr, "sandbox: 未知 helper 模式 %q\n", os.Args[2])
	}
	os.Exit(2)
	return true
}

// parseHelperArgs 解析 `--spec <base64> -- <argv…>`。
func parseHelperArgs(args []string) (string, []string, error) {
	if len(args) < 4 || args[0] != "--spec" || args[2] != "--" {
		return "", nil, errors.New("helper 参数应为 --spec <base64> -- argv…")
	}
	return args[1], args[3:], nil
}

func childExit(err error) {
	fmt.Fprintf(os.Stderr, "sandbox child: %v\n", err)
	os.Exit(2)
}

// childHelper 在目标 uid 下自限权限后 exec 目标程序。顺序固定:
// LockOSThread → no_new_privs → Landlock → seccomp(TSYNC)→ rlimit →
// umask/chdir → exec;任何一步失败退出码 2,绝不降级继续。
func childHelper(spec64 string, argv []string) {
	runtime.LockOSThread()
	spec, err := decodeSpec(spec64)
	if err != nil {
		childExit(err)
	}
	if len(argv) == 0 {
		childExit(errors.New("目标 argv 为空"))
	}
	if spec.Control.any() {
		fmt.Fprintf(os.Stderr, "sandbox: CONTROL MODE SkipLandlock=%v SkipSeccomp=%v SkipNNP=%v(仅限测试自检)\n",
			spec.Control.SkipLandlock, spec.Control.SkipSeccomp, spec.Control.SkipNNP)
	}
	abi := -1
	if !spec.Control.SkipLandlock {
		if abi, err = landlockABIFn(); err != nil {
			childExit(fmt.Errorf("Landlock ABI 查询: %w", err))
		}
	}
	if err := checkChildSpec(spec, abi); err != nil {
		childExit(err)
	}
	if !spec.Control.SkipNNP {
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			childExit(fmt.Errorf("no_new_privs: %w", err))
		}
	}
	if !spec.Control.SkipLandlock {
		if err := applyLandlock(spec, abi); err != nil {
			childExit(err)
		}
	}
	if !spec.Control.SkipSeccomp {
		filters, err := buildSeccompFilter(runtime.GOARCH, spec.Net == NetNone)
		if err != nil {
			childExit(err)
		}
		if err := installSeccomp(filters); err != nil {
			childExit(err)
		}
	}
	if err := applyRlimits(spec.Limits); err != nil {
		childExit(err)
	}
	if spec.Umask >= 0 {
		unix.Umask(spec.Umask)
	}
	dir := spec.Cwd
	if dir == "" {
		dir = spec.Workspace
	}
	if err := unix.Chdir(dir); err != nil {
		childExit(fmt.Errorf("chdir %s: %w", dir, err))
	}
	// 环境只用 Spec.Env,不继承;argv[0] 按 PATH 解析(SystemRO 已放行)。
	argv0 := argv[0]
	if !filepath.IsAbs(argv0) {
		if p, err := exec.LookPath(argv0); err == nil {
			argv0 = p
		}
	}
	if err := syscall.Exec(argv0, argv, spec.Env); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox child: exec %s: %v\n", argv0, err)
		os.Exit(127)
	}
}

// applyRlimits 用 setrlimit 设 cur=max;≤0 表示不设(Core 例外:≥0 即设,
// 零值 0 禁止 core)。NPROC 按 uid 计数,配合每会话独立 uid 才有意义。
func applyRlimits(l Limits) error {
	set := func(res int, v int64, name string) error {
		lim := unix.Rlimit{Cur: uint64(v), Max: uint64(v)}
		if err := unix.Setrlimit(res, &lim); err != nil {
			return fmt.Errorf("rlimit %s: %w", name, err)
		}
		return nil
	}
	for _, r := range []struct {
		res  int
		v    int64
		name string
		set  bool
	}{
		{unix.RLIMIT_NPROC, l.NProc, "nproc", l.NProc > 0},
		{unix.RLIMIT_CPU, l.CPU, "cpu", l.CPU > 0},
		{unix.RLIMIT_NOFILE, l.NoFile, "nofile", l.NoFile > 0},
		{unix.RLIMIT_FSIZE, l.FSize, "fsize", l.FSize > 0},
		{unix.RLIMIT_AS, l.AS, "as", l.AS > 0},
		{unix.RLIMIT_CORE, l.Core, "core", l.Core >= 0},
	} {
		if r.set {
			if err := set(r.res, r.v, r.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// runHelper 以 root 运行:作为命令沙箱的监督进程。凭据、进程组、Pdeathsig
// 在这里设置;转发 stdio;超时(SIGKILL 整组 + 按 uid 清理,退出码 124)、
// SIGTERM/SIGINT(同上,退出码 128+n);正常结束后按 uid 清理并断言零残留
// (残留时退出码 125)。本进程是 subreaper 小进程,wait4(-1) 只会收到自己
// 子树里的进程,安全;主进程绝不能这样做(见 KillUID)。
func runHelper(spec64 string, argv []string) {
	spec, err := decodeSpec(spec64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
		os.Exit(2)
	}
	if err := spec.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
		os.Exit(2)
	}
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox: 目标 argv 为空")
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "sandbox: run 模式需要 root")
		os.Exit(2)
	}
	if spec.Control.any() {
		fmt.Fprintf(os.Stderr, "sandbox: CONTROL MODE SkipLandlock=%v SkipSeccomp=%v SkipNNP=%v(仅限测试自检)\n",
			spec.Control.SkipLandlock, spec.Control.SkipSeccomp, spec.Control.SkipNNP)
	}
	_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)

	exe, err := os.Executable()
	if err != nil {
		exe = "/proc/self/exe"
	}
	// exec.Command 自己会把 exe 放进 argv[0],所以这里要去掉 helperArgv 的首元素
	cmd := exec.Command(exe, helperArgv("child", spec64, argv)[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Groups 为空切片:显式 setgroups(0) 丢掉补充组(需要 CAP_SETGID)
		Credential: &syscall.Credential{Uid: uint32(spec.UID), Gid: uint32(spec.GID), Groups: []uint32{}},
		Setpgid:    true,
		Pdeathsig:  syscall.SIGKILL,
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: 启动 child: %v\n", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeout <-chan time.Time
	if spec.Timeout > 0 {
		timeout = time.After(time.Duration(spec.Timeout) * time.Second)
	}

	killAll := func() {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		_ = cmd.Process.Kill()
		reapUIDHelper(spec.UID)
	}
	select {
	case waitErr := <-done:
		code := exitCode(waitErr)
		rr := reapUIDHelper(spec.UID)
		if rr.Remaining > 0 {
			fmt.Fprintf(os.Stderr, "sandbox: uid %d 清理后仍剩 %d 个存活进程\n", spec.UID, rr.Remaining)
			os.Exit(125)
		}
		os.Exit(code)
	case sig := <-sigCh:
		killAll()
		fmt.Fprintf(os.Stderr, "sandbox: 收到 %s,已清理 uid %d\n", sig, spec.UID)
		os.Exit(128 + int(sig.(syscall.Signal)))
	case <-timeout:
		killAll()
		fmt.Fprintf(os.Stderr, "sandbox: 超时,已清理 uid %d\n", spec.UID)
		os.Exit(124)
	}
}

// helperArgv 构造 `<exe> __xlyra-sandbox <mode> --spec … -- argv…`。
func helperArgv(mode, spec64 string, argv []string) []string {
	return append([]string{os.Args[0], helperCommand, mode, "--spec", spec64, "--"}, argv...)
}

// exitCode 从 exec 的 Wait 错误提取退出码(信号终止为 128+n)。
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
		if ee.ExitCode() >= 0 {
			return ee.ExitCode()
		}
	}
	fmt.Fprintf(os.Stderr, "sandbox: wait: %v\n", err)
	return 1
}

// reapStats 是 helper 内清理的结果。
type reapStats struct {
	Killed    int
	Remaining int
}

// reapUIDHelper 只能在 helper 小进程里调用(subreaper):SIGKILL 目标 uid 的
// 存活进程(僵尸不算),wait4(-1) 收尸,轮询直到连续 3 轮无存活,最多约
// 2 秒。导出的 KillUID 是主进程版,不做 wait4。
func reapUIDHelper(uid int) reapStats {
	killed := map[int]bool{}
	stable := 0
	for round := 0; round < 100; round++ {
		alive := 0
		for _, p := range ProcsOfUID(uid) {
			if p.State == "Z" {
				continue
			}
			alive++
			if err := unix.Kill(p.Pid, unix.SIGKILL); err == nil || err == unix.ESRCH {
				killed[p.Pid] = true
			}
		}
		for {
			var ws unix.WaitStatus
			if _, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil); err != nil {
				break
			}
		}
		if alive == 0 {
			stable++
			if stable >= 3 {
				break
			}
		} else {
			stable = 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	remaining := 0
	for _, p := range ProcsOfUID(uid) {
		if p.State != "Z" {
			remaining++
		}
	}
	return reapStats{Killed: len(killed), Remaining: remaining}
}
