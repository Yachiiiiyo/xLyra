package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// NetPolicy 是沙箱的网络策略。空串表示"不设网络规则",只允许与 Control
// 正向对照搭配(否则拒绝),用于证明"被拒是因为沙箱在起作用"。
type NetPolicy string

const (
	// NetNone 拒绝全部 TCP bind/connect(Landlock 网络规则,需 ABI≥4),
	// 并用 seccomp 禁止创建 AF_UNIX 之外的套接字,堵住 Landlock 管不到的
	// UDP/RAW/ICMP(命令模式)。
	NetNone NetPolicy = "none"
	// NetConnectPorts 仅允许 TCP connect 到 ConnectPorts,bind 全拒。
	// 此时 seccomp 不得封 socket(),否则 connect 在 Landlock 端口规则
	// 生效前就被打成 EPERM(PoC 已证)。
	NetConnectPorts NetPolicy = "connect-ports"
)

// Spec 是一次沙箱执行的完整描述,经 base64(JSON) 传给 helper。
// 不含任何密钥:argv 对同机其他 uid 可见。
type Spec struct {
	UID, GID     int      // 目标身份;UID==0 一律拒绝
	Cwd          string   // chdir 目标;空则用 Workspace
	Workspace    string   // 读写(必填,绝对路径)
	Tmp          string   // 读写,同时作为 HOME/TMPDIR(命令模式);Pi 模式可空
	ReadOnly     []string // 额外只读路径(可执行);不存在的路径在应用时忽略
	ReadWrite    []string // 额外读写目录(必须是目录;不存在报错)
	DevFiles     []string // 默认 /dev/null(读写)、/dev/zero、/dev/urandom、/dev/random(只读);只按文件放行,不放整个 /dev;非 nil 时整体替换,自定义条目一律可写
	SystemRO     []string // 系统只读白名单;nil 时按模式取默认(见 defaultPiSystemRO/defaultCommandSystemRO),非 nil 时完全替换
	Net          NetPolicy
	ConnectPorts []int    // Net==NetConnectPorts 时允许 TCP connect 的端口
	Umask        int      // -1 表示不改;注意零值 0 会执行 umask(0)
	Env          []string // 完整环境 KEY=VAL;不继承调用方环境
	Limits       Limits
	Timeout      int // 仅 run 模式使用:秒,0 不限
	Control      Control
}

// Limits 是传给 setrlimit 的资源上限(cur=max),<0 表示不设;
// Core 的零值 0 即"禁止 core dump",是预期默认。
type Limits struct {
	NProc  int64
	CPU    int64
	NoFile int64
	FSize  int64
	AS     int64 // Node 进程(Pi)不应设置
	Core   int64
}

// Control 是仅供测试与自检使用的正向对照开关,生产调用方不得设置;任一项
// 为真时 helper 会在 stderr 打印一行 sandbox: CONTROL MODE。
type Control struct {
	SkipLandlock bool
	SkipSeccomp  bool
	SkipNNP      bool
}

// Capability 是沙箱能力的探测结果;OK=false 时 Reason 给出中文原因。
// Detect 的实现在 Linux 专属文件里,非 Linux 平台恒不可用。
type Capability struct {
	OK          bool
	LandlockABI int
	Reason      string
}

func (c Control) any() bool { return c.SkipLandlock || c.SkipSeccomp || c.SkipNNP }

// defaultPiSystemRO 是 Pi 模式(SystemRO 为 nil)的系统只读白名单:
// 整个 /etc(Node 运行需要)加基础系统目录(07-poc-results §十三.3)。
var defaultPiSystemRO = []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc"}

// defaultCommandSystemRO 是命令模式的系统只读白名单:不放开整个 /etc,
// 只放行运行普通命令所需的杂项文件;不存在的路径应用时忽略。
var defaultCommandSystemRO = []string{
	"/usr", "/lib", "/lib64", "/bin", "/sbin",
	"/etc/ssl", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d",
	"/etc/alpine-release", "/etc/os-release", "/etc/passwd", "/etc/group",
	"/etc/nsswitch.conf", "/etc/localtime", "/etc/profile", "/etc/profile.d",
	"/etc/bash.bashrc", "/etc/terminfo", "/usr/share",
}

// Validate 校验 Spec 的静态合法性(fail closed):非法 Spec 在构造命令或
// 进入 helper 时都会被拒绝,而不是静默放宽。
func (s Spec) Validate() error {
	if s.UID == 0 {
		return errors.New("sandbox: 拒绝 UID 0")
	}
	if s.UID < 0 || s.GID < 0 || s.UID > math.MaxUint32 || s.GID > math.MaxUint32 {
		return fmt.Errorf("sandbox: 非法身份 uid=%d gid=%d", s.UID, s.GID)
	}
	if s.Workspace == "" {
		return errors.New("sandbox: Workspace 为空")
	}
	for _, p := range append([]string{s.Workspace, s.Cwd, s.Tmp}, s.ReadOnly...) {
		if p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("sandbox: 路径必须是绝对路径:%s", p)
		}
	}
	for _, p := range s.ReadWrite {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("sandbox: ReadWrite 路径必须是绝对路径:%s", p)
		}
	}
	for _, p := range append([]string{}, s.DevFiles...) {
		if p != "" && !strings.HasPrefix(p, "/") {
			return fmt.Errorf("sandbox: DevFiles 路径必须是绝对路径:%s", p)
		}
	}
	for _, p := range s.SystemRO {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("sandbox: SystemRO 路径必须是绝对路径:%s", p)
		}
	}
	switch s.Net {
	case NetNone, NetConnectPorts:
	case "":
		if !s.Control.any() {
			return errors.New("sandbox: 必须显式设置网络策略(NetNone 或 NetConnectPorts)")
		}
	default:
		return fmt.Errorf("sandbox: 未知网络策略 %q", s.Net)
	}
	// 网络策略由 Landlock 独自承担,跳过 Landlock 等于静默放开网络,PoC 拒绝
	if s.Net != "" && s.Control.SkipLandlock {
		return fmt.Errorf("sandbox: 网络策略 %q 与 SkipLandlock 互斥(会静默放开网络)", s.Net)
	}
	for _, port := range s.ConnectPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("sandbox: 非法端口 %d", port)
		}
	}
	if len(s.ConnectPorts) > 0 && s.Net != NetConnectPorts {
		return fmt.Errorf("sandbox: ConnectPorts 只在 %q 下有意义(与 %q 互斥)", NetConnectPorts, s.Net)
	}
	for _, e := range s.Env {
		if strings.ContainsRune(e, 0) {
			return errors.New("sandbox: 环境变量包含 NUL")
		}
		k, _, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return fmt.Errorf("sandbox: 非法环境变量条目 %q(需要 KEY=VAL)", e)
		}
	}
	if s.Umask < -1 || s.Umask > 0o777 {
		return fmt.Errorf("sandbox: 非法 umask %d", s.Umask)
	}
	if s.Timeout < 0 {
		return fmt.Errorf("sandbox: 非法 Timeout %d", s.Timeout)
	}
	// 降权到目标 uid 后没有 CAP_SYS_ADMIN,装 seccomp 过滤器必须先 no_new_privs
	if s.Control.SkipNNP && !s.Control.SkipSeccomp {
		return errors.New("sandbox: SkipNNP 必须与 SkipSeccomp 同用(降权后装过滤器需要 no_new_privs)")
	}
	return nil
}

// checkChildSpec 是 child 模式的执行前校验:静态校验之外,还要求带网络
// 策略的 Spec 在 Landlock ABI<4 时拒绝运行,而不是静默放行网络。
// abi<0 表示 Landlock 不可用。单元测试直接注入 abi 验证该分支。
func checkChildSpec(spec Spec, abi int) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if spec.Control.SkipLandlock {
		return nil
	}
	if abi < 1 {
		return errors.New("sandbox: Landlock 不可用,拒绝运行")
	}
	if spec.Net != "" && abi < 4 {
		return fmt.Errorf("sandbox: 网络策略 %q 需要 Landlock ABI≥4(当前 %d),拒绝运行而不是静默放行网络", spec.Net, abi)
	}
	return nil
}

// resolveSystemRO 返回实际生效的系统只读白名单:child 无法从 Spec 推断
// 调用方模式,直接进入 child 的 Spec 为空时取更严的命令模式默认;
// ChildCommand/RunCommand 在编码前已按各自模式填好。
func resolveSystemRO(spec Spec) []string {
	if spec.SystemRO != nil {
		return spec.SystemRO
	}
	return defaultCommandSystemRO
}

// encodeSpec 把 Spec 编成 helper argv 里的 base64(JSON)。
func encodeSpec(s Spec) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("sandbox: 编码 Spec: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// decodeSpec 是 encodeSpec 的逆运算。
func decodeSpec(v string) (Spec, error) {
	var s Spec
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return s, fmt.Errorf("sandbox: 解码 Spec: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("sandbox: 解析 Spec: %w", err)
	}
	return s, nil
}
