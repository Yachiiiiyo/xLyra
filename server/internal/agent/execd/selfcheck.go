package execd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"xlyra/server/internal/agent/isolation"
	"xlyra/server/internal/agent/sandbox"
)

// 检查项名称,测试与启动日志按名称定位。
const (
	checkSetup      = "自检环境"
	checkCapability = "沙箱能力"
	checkIdentity   = "命令身份"
	checkWorkspace  = "工作区可读写"
	checkSecrets    = "敏感路径不可读"
	checkOutside    = "工作区外不可写"
	checkNetwork    = "无网络"
	checkSyscall    = "系统调用封堵"
	checkEnviron    = "进程环境隔离"
	checkSock       = "不可连执行器套接字"
	checkRlimits    = "资源限制"
	checkZero       = "零残留"
	checkE2E        = "执行器端到端"
)

// CheckInput 是执行器启动自检的输入。
type CheckInput struct {
	DataDir     string
	SecretPaths []string
	Exe         string
	Isolation   isolation.Config
}

// runSpecHook 仅供集成测试注入 Control,模拟沙箱失效;生产必须为 nil。
var runSpecHook func(*sandbox.Spec)

type selfEnv struct {
	dataDir string
	root    string
	ws      string
	tmp     string
	outside string
	id      isolation.Identity
	exe     string
	exec    *Executor
	sock    string
}

type probeResult struct {
	values map[string]string
	code   int
	err    error
}

type probeRunner struct {
	env  *selfEnv
	base sandbox.Spec
}

// SelfCheck 用真实 Executor/RunCommand 路径做 fail-closed 自检;负向项都带
// 正向对照,避免把“环境本来就不可用”误判成沙箱有效。Detail 只含路径和预期/实际。
func SelfCheck(ctx context.Context, in CheckInput) isolation.Report {
	if msg := selfPrecheck(in); msg != "" {
		return selfSetupFailure(msg)
	}
	if in.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return selfSetupFailure("获取可执行文件失败:" + err.Error())
		}
		in.Exe = exe
	}
	env, err := buildSelfEnv(in)
	if err != nil {
		return selfSetupFailure("搭建自检环境失败:" + err.Error())
	}
	defer env.destroy()
	if err := ctx.Err(); err != nil {
		return selfSetupFailure("自检已取消:" + err.Error())
	}
	return runSelfChecks(ctx, env, in)
}

func selfPrecheck(in CheckInput) string {
	if os.Geteuid() != 0 {
		return fmt.Sprintf("需要 root 运行(当前 euid=%d)", os.Geteuid())
	}
	if runtime.GOOS != "linux" {
		return "仅支持 Linux,当前系统 " + runtime.GOOS
	}
	if in.DataDir == "" {
		return "未提供数据目录"
	}
	return ""
}

func selfSetupFailure(detail string) isolation.Report {
	return isolation.Report{OK: false, Checks: []isolation.Check{{Name: checkSetup, Passed: false, Detail: detail}}}
}

func buildSelfEnv(in CheckInput) (*selfEnv, error) {
	if err := isolation.PrepareDataDir(in.DataDir); err != nil {
		return nil, err
	}
	// 与 isolation.SelfCheck 相同使用保留槽位 MaxSlots-1;该推导未导出,
	// 按其文档公式 PiUID=UIDBase+2k、ExecUID=PiUID+1、GID=PiUID 复算。
	ic := in.Isolation.Normalize()
	pi := uint32(ic.UIDBase + 2*(ic.MaxSlots-1))
	id := isolation.Identity{Slot: ic.MaxSlots - 1, PiUID: pi, ExecUID: pi + 1, GID: pi}
	root := filepath.Join(in.DataDir, "sessions", ".execd-check-"+selfRandHex())
	if err := isolation.PrepareSession(root, id); err != nil {
		return nil, err
	}
	env := &selfEnv{dataDir: in.DataDir, root: root, ws: filepath.Join(root, "workspace"), id: id, exe: in.Exe}
	env.tmp = filepath.Join(root, "exec-tmp")
	env.outside = filepath.Join(in.DataDir, ".execd-outside-"+selfRandHex())
	if err := os.Mkdir(env.outside, 0o777); err != nil {
		env.destroy()
		return nil, err
	}
	if err := os.Chmod(env.outside, 0o777); err != nil {
		env.destroy()
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(env.ws, "seed"), []byte("probe"), 0o644); err != nil {
		env.destroy()
		return nil, err
	}
	ex, err := New(Config{Exe: in.Exe, SessionDir: root, PiUID: int(id.PiUID), ExecUID: int(id.ExecUID), GID: int(id.GID), Workspace: env.ws})
	if err != nil {
		env.destroy()
		return nil, err
	}
	env.exec = ex
	sock, err := ex.Start()
	if err != nil {
		env.destroy()
		return nil, err
	}
	env.sock = sock
	return env, nil
}

func selfRandHex() string {
	return strconv.FormatInt(time.Now().UnixNano(), 16)
}

func (e *selfEnv) destroy() {
	if e.exec != nil {
		_ = e.exec.Close()
	}
	if e.id.ExecUID != 0 {
		_, _, _ = sandbox.KillUID(int(e.id.ExecUID))
	}
	if e.id.PiUID != 0 {
		_, _, _ = sandbox.KillUID(int(e.id.PiUID))
	}
	if e.root != "" {
		_ = os.RemoveAll(e.root)
	}
	if e.outside != "" {
		_ = os.RemoveAll(e.outside)
	}
}

func (e *selfEnv) runner() *probeRunner {
	return &probeRunner{env: e, base: sandbox.Spec{
		UID: int(e.id.ExecUID), GID: int(e.id.GID), Workspace: e.ws, Tmp: e.tmp,
		Net: sandbox.NetNone, Umask: -1, Timeout: 60, Limits: sandbox.Limits{Core: 0},
		Env: []string{"PATH=" + defaultCommandPath, "HOME=" + e.tmp, "TMPDIR=" + e.tmp, "LANG=C.UTF-8"},
	}}
}

func (r *probeRunner) run(extraEnv []string, mutate func(*sandbox.Spec)) probeResult {
	spec := r.base
	spec.Env = append(append([]string{}, r.base.Env...), extraEnv...)
	if mutate != nil {
		mutate(&spec)
	}
	if runSpecHook != nil {
		runSpecHook(&spec)
	}
	cmd, err := sandbox.RunCommand(r.env.exe, spec, []string{r.env.exe, "__xlyra-sandbox", "probe"})
	if err != nil {
		return probeResult{err: err}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code, _ := waitInfo(runErr)
	res := probeResult{code: code}
	if runErr != nil {
		res.err = fmt.Errorf("退出码 %d", code)
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res.values); err != nil && res.err == nil {
		res.err = errors.New("探针输出格式错误")
	}
	// 自检各探测之间也保持零残留,避免影响后续 NPROC 计数。
	_, _, _ = sandbox.KillUID(int(r.env.id.ExecUID))
	return res
}

func skipLandlock(spec *sandbox.Spec) {
	spec.Control = sandbox.Control{SkipLandlock: true}
	spec.Net = ""
}

func skipLandlockSeccomp(spec *sandbox.Spec) {
	spec.Control = sandbox.Control{SkipLandlock: true, SkipSeccomp: true}
	spec.Net = ""
}

func skipSeccomp(spec *sandbox.Spec) {
	spec.Control = sandbox.Control{SkipSeccomp: true}
}

func runSelfChecks(ctx context.Context, env *selfEnv, in CheckInput) isolation.Report {
	var checks []isolation.Check
	add := func(name string, problems []string) {
		checks = append(checks, isolation.Check{Name: name, Passed: len(problems) == 0, Detail: strings.Join(problems, "; ")})
	}
	runner := env.runner()

	capability := sandbox.Detect()
	if capability.OK {
		add(checkCapability, nil)
	} else {
		add(checkCapability, []string{capability.Reason})
	}

	{
		var problems []string
		res := runner.run(nil, nil)
		if res.err != nil {
			problems = append(problems, "探测失败:"+res.err.Error())
		} else {
			if res.values["uid"] != strconv.Itoa(int(env.id.ExecUID)) {
				problems = append(problems, fmt.Sprintf("uid: 预期 %d,实际 %s", env.id.ExecUID, res.values["uid"]))
			}
			if res.values["nnp"] != "1" {
				problems = append(problems, "nnp: 预期 1,实际 "+res.values["nnp"])
			}
		}
		add(checkIdentity, problems)
	}

	{
		var problems []string
		seed := filepath.Join(env.ws, "seed")
		wsWrite := filepath.Join(env.ws, "write-check")
		tmpWrite := filepath.Join(env.tmp, "write-check")
		res := runner.run([]string{"XLYRA_PROBE_READ=" + seed, "XLYRA_PROBE_WRITE=" + wsWrite + "," + tmpWrite}, nil)
		if res.err != nil {
			problems = append(problems, "探测失败:"+res.err.Error())
		} else {
			for _, key := range []string{"read:" + seed, "write:" + wsWrite, "write:" + tmpWrite} {
				if res.values[key] != "ok" {
					problems = append(problems, key+": 预期 ok,实际 "+res.values[key])
				}
			}
		}
		add(checkWorkspace, problems)
	}

	{
		var problems, notes []string
		var existing []string
		for _, p := range in.SecretPaths {
			if _, err := os.Stat(p); err == nil {
				existing = append(existing, p)
			} else if !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, p+": 无法检查")
			}
		}
		if len(existing) > 0 {
			envVar := "XLYRA_PROBE_READ=" + strings.Join(existing, ",")
			sbx := runner.run([]string{envVar}, nil)
			ctl := runner.run([]string{envVar}, skipLandlock)
			if sbx.err != nil || ctl.err != nil {
				problems = append(problems, "探测或对照运行失败")
			} else {
				for _, p := range existing {
					got, control := sbx.values["read:"+p], ctl.values["read:"+p]
					if control == "ok" {
						if got != "EACCES" {
							problems = append(problems, fmt.Sprintf("%s: 预期 EACCES,实际 %s", p, got))
						}
					} else {
						if got == "ok" {
							problems = append(problems, p+": 预期读不到但成功")
						} else {
							notes = append(notes, fmt.Sprintf("%s: 对照也不可读(%s),仅验证读不到", p, control))
						}
					}
				}
			}
		}
		checks = append(checks, isolation.Check{Name: checkSecrets, Passed: len(problems) == 0, Detail: strings.Join(append(problems, notes...), "; ")})
	}

	{
		var problems []string
		target := filepath.Join(env.outside, "write-check")
		sbx := runner.run([]string{"XLYRA_PROBE_WRITE=" + target}, nil)
		ctlTarget := filepath.Join(env.outside, "write-control")
		ctl := runner.run([]string{"XLYRA_PROBE_WRITE=" + ctlTarget}, skipLandlock)
		if sbx.err != nil || ctl.err != nil {
			problems = append(problems, "探测或对照运行失败")
		} else {
			if got := sbx.values["write:"+target]; got == "ok" {
				problems = append(problems, target+": 预期写入失败但成功")
			}
			if got := ctl.values["write:"+ctlTarget]; got != "ok" {
				problems = append(problems, "对照失败: SkipLandlock 写入返回 "+got)
			}
		}
		_ = os.Remove(ctlTarget + ".xlyra-probe")
		add(checkOutside, problems)
	}

	{
		var problems []string
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			problems = append(problems, "无法创建回环监听")
			add(checkNetwork, problems)
		} else {
			udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:9")
			if err != nil {
				problems = append(problems, "无法构造 UDP 目标")
			}
			probeEnv := []string{"XLYRA_PROBE_TCP=" + ln.Addr().String(), "XLYRA_PROBE_UDP=" + udpAddr.String()}
			sbx := runner.run(probeEnv, nil)
			ctl := runner.run(probeEnv, skipLandlockSeccomp)
			_ = ln.Close()
			if sbx.err != nil || ctl.err != nil {
				problems = append(problems, "探测或对照运行失败")
			} else {
				for _, key := range []string{"tcp", "udp", "socket_inet"} {
					if sbx.values[key] == "ok" {
						problems = append(problems, key+": 预期失败但成功")
					}
					if ctl.values[key] != "ok" {
						problems = append(problems, key+" 对照: 预期 ok,实际 "+ctl.values[key])
					}
				}
			}
			add(checkNetwork, problems)
		}
	}

	{
		var problems []string
		sbx := runner.run(nil, nil)
		ctl := runner.run(nil, skipSeccomp)
		if sbx.err != nil || ctl.err != nil {
			problems = append(problems, "探测或对照运行失败")
		} else {
			for _, key := range []string{"ptrace", "setuid0", "mknod"} {
				if sbx.values[key] == "ok" {
					problems = append(problems, key+": 预期失败但成功")
				}
			}
			if ctl.values["ptrace"] != "ok" {
				problems = append(problems, "ptrace 对照: 预期 ok,实际 "+ctl.values["ptrace"])
			}
		}
		add(checkSyscall, problems)
	}

	{
		var problems []string
		sbx := runner.run([]string{fmt.Sprintf("XLYRA_PROBE_ENVIRON_PID=%d", os.Getpid())}, nil)
		ctl := runner.run([]string{"XLYRA_PROBE_ENVIRON_PID=self"}, skipLandlock)
		if sbx.err != nil || ctl.err != nil {
			problems = append(problems, "探测或对照运行失败")
		} else {
			if sbx.values["environ"] == "ok" {
				problems = append(problems, "xLyra environ: 预期读不到但成功")
			}
			if ctl.values["environ"] != "ok" {
				problems = append(problems, "自有 environ 对照: 预期 ok,实际 "+ctl.values["environ"])
			}
		}
		add(checkEnviron, problems)
	}

	{
		var problems []string
		probeEnv := []string{"XLYRA_PROBE_UNIX=" + env.sock}
		sbx := runner.run(probeEnv, nil)
		if sbx.err != nil {
			problems = append(problems, "探测失败:"+sbx.err.Error())
		} else if sbx.values["unix"] == "ok" {
			problems = append(problems, "exec.sock: ExecUID 预期连不上但成功")
		}
		// 对照是真实 Pi 启动路径:PiUID 经会话目录 rw 规则连接同会话套接字。
		piSpec := sandbox.Spec{
			UID: int(env.id.PiUID), GID: int(env.id.GID), Workspace: env.root,
			Net: sandbox.NetNone, Umask: -1,
			Env: append([]string{"PATH=" + defaultCommandPath}, probeEnv...),
		}
		cmd, err := sandbox.ChildCommand(env.exe, piSpec, []string{env.exe, "__xlyra-sandbox", "probe"})
		if err != nil {
			problems = append(problems, "无法构造 PiUID 对照")
		} else {
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			runErr := cmd.Run()
			var values map[string]string
			if runErr != nil || json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &values) != nil || values["unix"] != "ok" {
				problems = append(problems, "PiUID 对照: 预期连接 exec.sock 成功")
			}
		}
		add(checkSock, problems)
	}

	{
		var problems []string
		// Go 探针自身的运行时线程也计入 NPROC,限额不能太小,否则探针起不来
		forkEnv := []string{"XLYRA_PROBE_FORK=80"}
		limited := runner.run(forkEnv, func(spec *sandbox.Spec) { spec.Limits.NProc = 40 })
		control := runner.run(forkEnv, nil)
		if limited.err != nil || control.err != nil {
			problems = append(problems, "探测或对照运行失败")
		} else {
			n, err1 := strconv.Atoi(limited.values["fork"])
			c, err2 := strconv.Atoi(control.values["fork"])
			if err1 != nil || n >= 80 {
				problems = append(problems, "NProc=40: fork 成功数应小于 80,实际 "+limited.values["fork"])
			}
			if err2 != nil || c != 80 {
				problems = append(problems, "无 NProc 对照: 预期 80,实际 "+control.values["fork"])
			}
		}
		add(checkRlimits, problems)
	}

	{
		var problems []string
		_, _, exit, err := runSocketRequest(env.sock, request{Cmd: "setsid /bin/sleep 60 &", Timeout: 10})
		if err != nil || exit.Code != 0 || exit.Error != "" {
			problems = append(problems, "后台进程探测命令失败")
		} else if !waitUIDZero(env.id.ExecUID, 5*time.Second) {
			problems = append(problems, "命令返回后 ExecUID 仍有存活进程")
		}
		add(checkZero, problems)
	}

	{
		var problems []string
		stdout, _, exit, err := runSocketRequest(env.sock, request{Cmd: "echo hi; id -u", Timeout: 10})
		if err != nil {
			problems = append(problems, "协议请求失败:"+err.Error())
		} else {
			text := string(stdout)
			if !strings.Contains(text, "hi\n") || !strings.Contains(text, strconv.Itoa(int(env.id.ExecUID))) {
				problems = append(problems, "端到端输出缺少 hi 或 ExecUID")
			}
			if exit.Code != 0 || exit.Error != "" {
				problems = append(problems, fmt.Sprintf("端到端退出码: 预期 0,实际 %d(%s)", exit.Code, exit.Error))
			}
		}
		add(checkE2E, problems)
	}

	ok := true
	for _, check := range checks {
		if !check.Passed {
			ok = false
		}
	}
	return isolation.Report{OK: ok, Checks: checks}
}

func waitUIDZero(uid uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		alive := 0
		for _, p := range sandbox.ProcsOfUID(int(uid)) {
			if p.State != "Z" {
				alive++
			}
		}
		if alive == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
