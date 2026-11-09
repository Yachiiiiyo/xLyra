package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SelfCheckInput 是启动自检的输入。
type SelfCheckInput struct {
	// DataDir 是 Agent 数据目录(其下含 sessions/),自检在其下临时搭建探测环境。
	DataDir string
	// SecretPaths 是 xLyra 的敏感路径(密钥文件、数据库目录等);不存在的跳过。
	SecretPaths []string
	// ReadableProbe 是对所有用户应可读的文件(如技能目录下的 SKILL.md);
	// 为空则跳过该正向项。
	ReadableProbe string
	// SelfPID 是 xLyra 自身 pid,用于 /proc/<pid>/environ 探测。
	SelfPID int
}

// Check 是单项探测结果;Detail 只含路径与预期/实际,不含文件内容。
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// Report 是自检报告;OK 仅当全部通过(fail-closed)。
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// 检查项名称(测试与日志按名字定位)。
const (
	checkPre     = "前置条件"
	checkSetup   = "自检环境"
	checkOwnDirs = "自有目录可读写"
	checkPublic  = "公共文件可读"
	checkSecrets = "敏感路径不可读"
	checkCross   = "跨会话隔离"
	checkDataRO  = "数据目录不可写"
	checkMeta    = "meta.json 防篡改"
	checkEnviron = "进程环境隔离"
	checkExec    = "ExecUID 权限边界"
)

// 探测标记;内容本身绝不写进 Detail。
const (
	probeMarker  = "xlyra-iso-probe"
	canaryMarker = "xlyra-iso-canary"
	metaMarker   = "xlyra-iso-meta"
)

// SelfCheck 在实际数据目录上做 fail-closed 自检:用保留槽位的两个身份 A、B
// 临时搭一套与真实布局一致的会话目录,以 /bin/sh -c 子进程逐项验证
// "该成功的成功、该失败的失败";任一项与预期不符即 OK=false,由调用方
// 禁用 Agent 的敏感能力。每项探测都有正向对照,避免"环境坏了所以读不到"
// 被误判为隔离成功。
func SelfCheck(ctx context.Context, c Config, in SelfCheckInput) Report {
	c = c.Normalize()
	if msg := precheck(in); msg != "" {
		return Report{OK: false, Checks: []Check{{Name: checkPre, Passed: false, Detail: msg}}}
	}
	// 数据目录与 sessions 根:缺则建(0711);已存在则保持原样,如实探测现状
	sessions := filepath.Join(in.DataDir, "sessions")
	for _, d := range []string{in.DataDir, sessions} {
		if _, err := os.Lstat(d); errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(d, 0o711); err != nil {
				return setupFailure("无法创建 " + d + ":" + err.Error())
			}
			_ = os.Chmod(d, sessionDirPerm)
		}
	}
	env, err := buildCheckEnv(in.DataDir, c)
	if err != nil {
		return setupFailure("搭建自检环境失败:" + err.Error())
	}
	defer env.destroy()
	return runChecks(ctx, env, in)
}

func precheck(in SelfCheckInput) string {
	if os.Geteuid() != 0 {
		return fmt.Sprintf("需要 root 运行(当前 euid=%d)", os.Geteuid())
	}
	if runtime.GOOS != "linux" {
		return "仅支持 Linux,当前系统 " + runtime.GOOS
	}
	if in.DataDir == "" {
		return "未提供数据目录"
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		return "缺少 /bin/sh:" + err.Error()
	}
	return ""
}

func setupFailure(detail string) Report {
	return Report{OK: false, Checks: []Check{{Name: checkSetup, Passed: false, Detail: detail}}}
}

// checkEnv 是自检的临时环境:<DataDir>/.isolation-check-<rand> 扮演会话根,
// 内含身份 A、B 的两套会话目录与 canary 文件。
type checkEnv struct {
	dataDir string
	root    string
	aDir    string
	bDir    string
	a, b    Identity
	aPI     string
	aAgent  string
	aWS     string
	bPI     string
	bAgent  string
	bWS     string
	// meta 是 A 会话里 root 持有的 meta.json canary。
	meta string
	// parentCanary 是数据目录上一级的 0600 canary(防 SecretPaths 漏配)。
	parentCanary string
}

func buildCheckEnv(dataDir string, c Config) (*checkEnv, error) {
	e := &checkEnv{
		dataDir: dataDir,
		a:       identityForSlot(c, c.MaxSlots-1), // 保留槽位 A
		b:       identityForSlot(c, c.MaxSlots),   // 保留槽位 B
	}
	e.root = filepath.Join(dataDir, ".isolation-check-"+randHex(6))
	if err := os.Mkdir(e.root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(e.root, sessionDirPerm); err != nil {
		return nil, err
	}
	e.aDir, e.bDir = filepath.Join(e.root, "a"), filepath.Join(e.root, "b")
	if err := PrepareSession(e.aDir, e.a); err != nil {
		return nil, err
	}
	if err := PrepareSession(e.bDir, e.b); err != nil {
		return nil, err
	}
	e.aPI, e.aAgent, e.aWS = filepath.Join(e.aDir, "pi"), filepath.Join(e.aDir, "pi-agent"), filepath.Join(e.aDir, "workspace")
	e.bPI, e.bAgent, e.bWS = filepath.Join(e.bDir, "pi"), filepath.Join(e.bDir, "pi-agent"), filepath.Join(e.bDir, "workspace")
	// A 会话的 meta.json canary:root 持有 0600,与 session.Store 的写法一致
	e.meta = filepath.Join(e.aDir, "meta.json")
	if err := writeRootFile(e.meta, metaMarker); err != nil {
		return nil, err
	}
	// canary 文件:各自身份持有 0600
	canaries := []struct {
		path string
		id   Identity
	}{
		{filepath.Join(e.aPI, "canary"), e.a},
		{filepath.Join(e.aAgent, "canary"), e.a},
		{filepath.Join(e.bPI, "canary"), e.b},
		{filepath.Join(e.bAgent, "canary"), e.b},
		{filepath.Join(e.bWS, "canary"), e.b},
	}
	for _, cn := range canaries {
		if err := writeRootFile(cn.path, canaryMarker); err != nil {
			return nil, err
		}
		if err := ChownFile(cn.path, cn.id); err != nil {
			return nil, err
		}
	}
	parent := filepath.Dir(filepath.Clean(dataDir))
	e.parentCanary = filepath.Join(parent, ".xlyra-isolation-"+randHex(4))
	if err := writeRootFile(e.parentCanary, canaryMarker); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *checkEnv) destroy() {
	_ = os.RemoveAll(e.root)
	_ = os.Remove(e.parentCanary)
}

// runChecks 执行全部探测项并汇总报告。
func runChecks(ctx context.Context, e *checkEnv, in SelfCheckInput) Report {
	var checks []Check
	aCred, bCred := e.a.PiCredential(), e.b.PiCredential()
	run := func(cred *syscall.Credential, script string) probeResult {
		return runProbe(ctx, cred, script)
	}
	add := func(name string, problems []string) {
		checks = append(checks, Check{Name: name, Passed: len(problems) == 0, Detail: strings.Join(problems, "; ")})
	}

	// 1. 正向:A 在自己的 workspace/、pi/、pi-agent/ 里创建、读、改名、删除。
	{
		var problems []string
		for _, d := range []string{e.aWS, e.aPI, e.aAgent} {
			f := filepath.Join(d, ".probe")
			script := fmt.Sprintf("echo %s > %s && /bin/grep -q %s %s && /bin/mv %s %s && /bin/rm %s",
				probeMarker, q(f), probeMarker, q(f), q(f), q(f+"2"), q(f+"2"))
			if r := run(aCred, script); r.code != 0 {
				problems = append(problems, fmt.Sprintf("%s: 预期可读写但失败(exit=%d%s)", d, r.code, errSuffix(r)))
			}
		}
		add(checkOwnDirs, problems)
	}

	// 2. 正向:A 读对所有用户可读的文件(如技能 SKILL.md)。
	if in.ReadableProbe != "" {
		var problems []string
		if r := run(aCred, "/bin/cat "+q(in.ReadableProbe)+" > /dev/null"); r.code != 0 {
			problems = append(problems, fmt.Sprintf("%s: 预期可读但失败(exit=%d%s)", in.ReadableProbe, r.code, errSuffix(r)))
		}
		add(checkPublic, problems)
	}

	// 3. 负向:敏感路径(文件读内容、目录列目)必须读不到;
	//    外加数据目录上一级的 0600 canary,防 SecretPaths 漏配。
	{
		var problems []string
		skipped := 0
		for _, p := range in.SecretPaths {
			fi, err := os.Stat(p) // 跟随符号链接,按最终目标类型选探测方式
			if errors.Is(err, fs.ErrNotExist) {
				skipped++
				continue
			}
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: 无法检查(%v)", p, err))
				continue
			}
			op := "/bin/cat"
			if fi.IsDir() {
				op = "/bin/ls"
			}
			// 正向对照:root 能读,证明目标存在且探测方式有效
			if r := run(nil, op+" "+q(p)); r.code != 0 {
				problems = append(problems, fmt.Sprintf("%s: 对照失败,root 也读不到(exit=%d%s)", p, r.code, errSuffix(r)))
				continue
			}
			if r := run(aCred, op+" "+q(p)); r.code == 0 {
				problems = append(problems, fmt.Sprintf("%s: 预期读不到但成功", p))
			}
		}
		if r := run(nil, "/bin/cat "+q(e.parentCanary)); r.code != 0 || !strings.Contains(r.out, canaryMarker) {
			problems = append(problems, "数据目录上一级: 对照失败(root 未读到 canary)")
		} else if r := run(aCred, "/bin/cat "+q(e.parentCanary)); r.code == 0 {
			problems = append(problems, "数据目录上一级: 预期读不到但成功")
		}
		detail := strings.Join(problems, "; ")
		if detail == "" && skipped > 0 {
			detail = fmt.Sprintf("跳过 %d 个不存在的路径", skipped)
		}
		checks = append(checks, Check{Name: checkSecrets, Passed: len(problems) == 0, Detail: detail})
	}

	// 4. 负向:读不到 B 会话任何目录里的 canary,也写不进 B 的工作区。
	{
		var problems []string
		for _, d := range []string{e.bWS, e.bPI, e.bAgent} {
			canary := filepath.Join(d, "canary")
			// 正向对照:B 自己能读到 canary
			if r := run(bCred, "/bin/grep -q "+canaryMarker+" "+q(canary)); r.code != 0 {
				problems = append(problems, fmt.Sprintf("%s: 对照失败,属主自己读不到 canary(exit=%d%s)", d, r.code, errSuffix(r)))
				continue
			}
			if r := run(aCred, "/bin/cat "+q(canary)); r.code == 0 {
				problems = append(problems, fmt.Sprintf("%s: 预期读不到但成功", d))
			}
		}
		pwn := filepath.Join(e.bWS, ".pwn")
		if r := run(aCred, "echo x > "+q(pwn)); r.code == 0 || pathExists(pwn) {
			problems = append(problems, "B 工作区: 预期写入失败但成功")
			_ = os.Remove(pwn)
		}
		add(checkCross, problems)
	}

	// 5. 负向:写不进数据目录根与 sessions 根。
	{
		var problems []string
		sessions := filepath.Join(e.dataDir, "sessions")
		// 正向对照:数据目录与 sessions 根必须可穿越,否则生产会话也无法访问
		if r := run(aCred, "test -x "+q(e.dataDir)+" -a -x "+q(sessions)); r.code != 0 {
			problems = append(problems, "对照失败:会话身份无法穿越数据目录/sessions 根(权限过紧)")
		}
		for _, d := range []string{e.dataDir, sessions} {
			pwn := filepath.Join(d, ".isolation-pwn-"+randHex(4))
			if r := run(aCred, "echo x > "+q(pwn)); r.code == 0 || pathExists(pwn) {
				problems = append(problems, fmt.Sprintf("%s: 预期写入失败但成功", d))
				_ = os.Remove(pwn)
			}
		}
		add(checkDataRO, problems)
	}

	// 6. 负向:root 持有的 meta.json 读、改、删、改名全部必须失败
	//    (会话目录属 root,目录写权限决定能否删除或替换文件)。
	{
		var problems []string
		ops := []struct {
			name   string
			script string
		}{
			{"读取", "/bin/cat " + q(e.meta)},
			{"修改", "echo x >> " + q(e.meta)},
			{"删除", "/bin/rm " + q(e.meta)},
			{"改名", "/bin/mv " + q(e.meta) + " " + q(e.meta+".renamed")},
		}
		intact := func() bool {
			data, err := os.ReadFile(e.meta)
			return err == nil && string(data) == metaMarker
		}
		for _, op := range ops {
			r := run(aCred, op.script)
			if r.code == 0 || !intact() || pathExists(e.meta+".renamed") {
				problems = append(problems, fmt.Sprintf("meta.json %s: 预期失败但成功(exit=%d)", op.name, r.code))
			}
		}
		add(checkMeta, problems)
	}

	// 7. 负向:读不到 xLyra 进程的环境变量。
	{
		var problems []string
		if in.SelfPID <= 0 {
			problems = append(problems, "未提供 SelfPID")
		} else {
			envFile := filepath.Join("/proc", strconv.Itoa(in.SelfPID), "environ")
			// 对照:进程确实存在(读不到是因为权限,而不是 pid 无效)。不能用"root 读得到"
			// 当对照:容器默认没有 CAP_SYS_PTRACE,root 也读不了不属于自己进程树的 environ。
			if !pathExists(filepath.Join("/proc", strconv.Itoa(in.SelfPID))) {
				problems = append(problems, fmt.Sprintf("对照失败,/proc/%d 不存在", in.SelfPID))
			} else if r := run(aCred, "/bin/cat "+q(envFile)); r.code == 0 {
				problems = append(problems, "预期读不到但成功: "+envFile)
			}
			// 正向对照:能读自己的 environ,证明 /proc 机制可用
			if r := run(aCred, "/bin/cat /proc/self/environ"); r.code != 0 {
				problems = append(problems, "对照失败,连自己的 environ 都读不到")
			}
		}
		add(checkEnviron, problems)
	}

	// 8. ExecUID 探针(P5b 的前提,现在就验证):经组权限读写工作区、
	//    新文件属组继承 GID(setgid),但读不到 Pi 的私有目录与 Pi 进程环境。
	{
		var problems []string
		execCred := e.a.ExecCredential()
		f := filepath.Join(e.aWS, ".exec-probe")
		r := run(execCred, "echo "+probeMarker+" > "+q(f)+" && /bin/grep -q "+probeMarker+" "+q(f))
		if r.code != 0 {
			problems = append(problems, fmt.Sprintf("工作区: 预期经组权限可读写但失败(exit=%d%s)", r.code, errSuffix(r)))
		} else if fi, err := os.Lstat(f); err != nil {
			problems = append(problems, "工作区: 无法检查新文件属组")
		} else if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Gid != e.a.GID {
			problems = append(problems, "工作区: 新文件未继承会话组(setgid 未生效)")
		}
		for _, d := range []string{e.aAgent, e.aPI} {
			if r := run(execCred, "/bin/ls "+q(d)); r.code == 0 {
				problems = append(problems, fmt.Sprintf("%s: ExecUID 预期读不到但成功", d))
			}
			if r := run(execCred, "/bin/cat "+q(filepath.Join(d, "canary"))); r.code == 0 {
				problems = append(problems, fmt.Sprintf("%s/canary: ExecUID 预期读不到但成功", d))
			}
		}
		problems = append(problems, probePiEnviron(ctx, e)...)
		add(checkExec, problems)
	}

	ok := true
	for _, c := range checks {
		if !c.Passed {
			ok = false
		}
	}
	return Report{OK: ok, Checks: checks}
}

// probePiEnviron 以 A 的 PiUID 启动一个 sleep,验证 ExecUID 读不到它的
// /proc/<pid>/environ;用完即杀,不留残留。
func probePiEnviron(ctx context.Context, e *checkEnv) []string {
	sleep := exec.Command("/bin/sleep", "30")
	sleep.Env = []string{"XLYRA_ISO_PROBE=" + probeMarker}
	sleep.SysProcAttr = &syscall.SysProcAttr{Credential: e.a.PiCredential(), Setpgid: true}
	if err := sleep.Start(); err != nil {
		return []string{"无法启动 Pi 身份探测进程:" + err.Error()}
	}
	defer func() {
		_ = sleep.Process.Kill()
		_ = syscall.Kill(-sleep.Process.Pid, syscall.SIGKILL)
		_, _ = sleep.Process.Wait()
	}()
	procDir := filepath.Join("/proc", strconv.Itoa(sleep.Process.Pid))
	envFile := filepath.Join(procDir, "environ")
	deadline := time.Now().Add(2 * time.Second)
	for !pathExists(procDir) {
		if time.Now().After(deadline) {
			return []string{"Pi 身份探测进程未出现在 /proc"}
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 经 Credential 从 root 降权启动的进程会被内核设为非 dumpable,此时
	// /proc/<pid>/environ 归 root 所有,同 uid 进程也不一定能读(还需
	// CAP_SYS_PTRACE)。只有 proc 条目确属 PiUID 时才把属主读取作为正向对照;
	// 否则跳过该对照,仍强制验证下面 ExecUID 的跨身份读取必定失败。
	if fi, err := os.Stat(envFile); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid == e.a.PiUID {
			if r := runProbe(ctx, e.a.PiCredential(), "/bin/cat "+q(envFile)); r.code != 0 || !strings.Contains(r.out, probeMarker) {
				return []string{"对照失败,属主 PiUID 读不到自己探测进程的 environ"}
			}
		}
	}
	if r := runProbe(ctx, e.a.ExecCredential(), "/bin/cat "+q(envFile)); r.code == 0 {
		return []string{"ExecUID 读到了 Pi 进程的 environ(预期读不到)"}
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
