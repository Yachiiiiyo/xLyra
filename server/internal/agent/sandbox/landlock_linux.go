package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// 编译期对照:本地访问位常量必须与 x/sys 的定义一致(不等时数组常量下标
// 越界,编译失败),防止手抄漂移。
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE)-accessFSExecute]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE)-accessFSWriteFile]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)-accessFSReadFile]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_READ_DIR)-accessFSReadDir]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_REMOVE_DIR)-accessFSRemoveDir]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_REMOVE_FILE)-accessFSRemoveFil]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_CHAR)-accessFSMakeChar]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_DIR)-accessFSMakeDir]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_REG)-accessFSMakeReg]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SOCK)-accessFSMakeSock]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_FIFO)-accessFSMakeFIFO]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK)-accessFSMakeBlock]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM)-accessFSMakeSym]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_REFER)-accessFSRefer]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE)-accessFSTruncate]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_FS_IOCTL_DEV)-accessFSIoctlDev]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_NET_BIND_TCP)-accessNetBindTCP]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_ACCESS_NET_CONNECT_TCP)-accessNetConnectTCP]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET)-scopeAbstractUnixSocket]
var _ = [1]struct{}{}[uint64(unix.LANDLOCK_SCOPE_SIGNAL)-scopeSignal]
var _ = [1]struct{}{}[uint(unix.LANDLOCK_RULE_PATH_BENEATH)-landlockRulePathBeneath]

// landlockRulePathBeneath 与 x/sys 的 LANDLOCK_RULE_PATH_BENEATH 同值,
// landlockRuleNetPort = LANDLOCK_RULE_NET_PORT(内核 uapi,后者 x/sys v0.48 未定义)。
const (
	landlockRulePathBeneath = 1
	landlockRuleNetPort     = 2
)

// landlock_create_ruleset 按已用字段传 size:8(仅 fs)、16(fs+net)、
// 24(fs+net+scope),多传未知字段在旧内核上会 EINVAL。
const (
	landlockAttrSizeFS    = 8
	landlockAttrSizeNet   = 16
	landlockAttrSizeScope = 24
)

// landlockNetPortAttr 与内核 landlock_net_port_attr 一致(packed 16 字节,
// 端口为主机字节序;PoC 在内核 7.2 实测)。x/sys v0.48 未定义。
type landlockNetPortAttr struct {
	AllowedAccess uint64
	Port          uint64
}

// landlockABIFn 可在测试里注入,模拟旧内核的 ABI 版本。
var landlockABIFn = landlockABI

// landlockABI 只做查询:VERSION 标志不创建规则集、不限制本进程。
func landlockABI() (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

func createRuleset(attr *unix.LandlockRulesetAttr, size uintptr) (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(attr)), size, 0)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

// addPathRule 向 ruleset 加一条 path_beneath 规则。内核按 packed 12 字节读
// landlock_path_beneath_attr(Go 结构有填充,但小端布局下前 12 字节恰好是
// 有效字段);add_rule 的 flags 必须为 0,传结构体长度会 EINVAL(PoC 踩坑)。
func addPathRule(ruleset int, path string, access uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)
	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(ruleset),
		uintptr(landlockRulePathBeneath),
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	)
	runtime.KeepAlive(&attr)
	if errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", path, errno)
	}
	return nil
}

func addNetPortRule(ruleset int, port int, access uint64) error {
	attr := landlockNetPortAttr{AllowedAccess: access, Port: uint64(port)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(ruleset),
		uintptr(landlockRuleNetPort),
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	)
	runtime.KeepAlive(&attr)
	if errno != 0 {
		return fmt.Errorf("landlock net port %d: %w", port, errno)
	}
	return nil
}

// addROPath 放行一条只读路径,不存在的忽略;目录与普通文件给不同的访问位
// (目录位给文件会 EINVAL)。
func addROPath(ruleset int, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("查看 %s: %w", path, err)
	}
	access := roFileAccess()
	if fi.IsDir() {
		access = roDirAccess()
	}
	return addPathRule(ruleset, path, access)
}

// addRWDir 放行一条读写目录,必须已存在且是目录(Landlock 的 rw 目录位传给
// 文件也会 EINVAL,这里先给出明确的错误)。
func addRWDir(ruleset int, path string, access uint64) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("查看 %s: %w", path, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("sandbox: %s 不是目录", path)
	}
	return addPathRule(ruleset, path, access)
}

// addDevFile 放行一个设备/普通文件,不存在的忽略;必须是文件,目录说明
// 调用方传错了(该用 ReadOnly/ReadWrite)。
func addDevFile(ruleset int, path string, access uint64) error {
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("查看 %s: %w", path, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("sandbox: DevFiles 条目 %s 是目录", path)
	}
	return addPathRule(ruleset, path, access)
}

// helperDir 返回 helper 可执行文件所在目录(系统目录除外)。child 在
// Landlock 之后才 exec 目标程序,系统目录已由 SystemRO 覆盖;但测试经常
// 以测试二进制作为被执行程序,保留这条只读放行(PoC 同款)。
func helperDir() []string {
	exe, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return nil
	}
	dir := filepath.Dir(exe)
	switch dir {
	case "/usr", "/usr/bin", "/bin", "/sbin", "/lib", "/usr/lib", "/":
		return nil
	}
	return []string{dir}
}

// devRule 是一条设备文件放行规则。
type devRule struct {
	path string
	rw   bool
}

// devPlan 返回设备文件放行表:默认 /dev/null 可写、其余只读;自定义
// DevFiles 一律可写(写入 zero/random 等字符设备无害)。
func devPlan(spec Spec) []devRule {
	if spec.DevFiles != nil {
		out := make([]devRule, 0, len(spec.DevFiles))
		for _, p := range spec.DevFiles {
			if p != "" {
				out = append(out, devRule{p, true})
			}
		}
		return out
	}
	return []devRule{
		{"/dev/null", true},
		{"/dev/zero", false},
		{"/dev/urandom", false},
		{"/dev/random", false},
	}
}

// applyLandlock 在当前线程安装 Landlock 限制(调用方已保证 !SkipLandlock,
// 且 checkChildSpec 已拒绝 ABI 不足的网络策略)。scope(ABI≥6)尽力而为:
// 创建失败时降级去掉 scope 并在 stderr 写一行,但网络规则不降级。
func applyLandlock(spec Spec, abi int) error {
	fsBits := fsAccessForABI(abi)
	attr := unix.LandlockRulesetAttr{Access_fs: fsBits}
	size := uintptr(landlockAttrSizeFS)
	if spec.Net != "" {
		attr.Access_net = accessNetBindTCP | accessNetConnectTCP
		size = landlockAttrSizeNet
	}
	wantScope := abi >= 6
	if wantScope {
		attr.Scoped = scopeAbstractUnixSocket | scopeSignal
		size = landlockAttrSizeScope
	}
	ruleset, err := createRuleset(&attr, size)
	if err != nil && wantScope {
		fmt.Fprintf(os.Stderr, "sandbox: Landlock scope 不可用(%v),降级运行\n", err)
		attr.Scoped = 0
		size = landlockAttrSizeNet
		if spec.Net == "" {
			size = landlockAttrSizeFS
		}
		ruleset, err = createRuleset(&attr, size)
	}
	if err != nil {
		return fmt.Errorf("landlock_create_ruleset: %w", err)
	}
	defer unix.Close(ruleset)

	rw := fsBits
	for _, p := range append(resolveSystemRO(spec), spec.ReadOnly...) {
		if err := addROPath(ruleset, p); err != nil {
			return err
		}
	}
	for _, p := range helperDir() {
		if err := addROPath(ruleset, p); err != nil {
			return err
		}
	}
	for _, p := range spec.ReadWrite {
		if p == "" {
			continue
		}
		if err := addRWDir(ruleset, p, rw); err != nil {
			return err
		}
	}
	if err := addRWDir(ruleset, spec.Workspace, rw); err != nil {
		return err
	}
	if spec.Tmp != "" && spec.Tmp != spec.Workspace {
		if err := addRWDir(ruleset, spec.Tmp, rw); err != nil {
			return err
		}
	}
	for _, d := range devPlan(spec) {
		access := devReadAccess(abi)
		if d.rw {
			access = devWriteAccess(abi)
		}
		if err := addDevFile(ruleset, d.path, access); err != nil {
			return err
		}
	}
	if spec.Net == NetConnectPorts {
		for _, port := range spec.ConnectPorts {
			if err := addNetPortRule(ruleset, port, accessNetConnectTCP); err != nil {
				return err
			}
		}
	}
	// TSYNC(内核 6.15+)把限制扩到整个线程组;旧内核不认该 flag 时退回
	// 单线程安装(LockOSThread + 随后 exec 保证幸存线程受限)。
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), uintptr(unix.LANDLOCK_RESTRICT_SELF_TSYNC), 0)
	if errno == unix.EINVAL || errno == unix.ENOTSUP || errno == unix.EOPNOTSUPP {
		_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0)
	}
	if errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}
