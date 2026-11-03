package sandbox

import "fmt"

// BPF/seccomp 常量。x/sys 的 BPF_*、SECCOMP_RET_*、AUDIT_ARCH_* 与
// SockFilter 仅在 linux 构建可见,而过滤器构造是纯函数、要在任意开发机上
// 做单元测试,故在此定义数值相同的本地常量(与内核 uapi 一致;linux 构建
// 下由 seccomp_linux.go 的编译期对照与 bpf_linux_test.go 保证不漂移)。
const (
	bpfLDWABS  = 0x20 // BPF_LD|BPF_W|BPF_ABS
	bpfJmpJEQK = 0x15 // BPF_JMP|BPF_JEQ|BPF_K
	bpfRetK    = 0x06 // BPF_RET|BPF_K

	seccompRetAllow = 0x7fff0000
	seccompRetErrno = 0x00050000
	errnoEPERM      = 0x1

	afUnix = 1 // AF_UNIX
)

// sockFilter 与 unix.SockFilter 布局一致,安装时逐字段转换。
type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// filterArch 是一种目标架构的过滤器参数。系统调用号来自内核
// arch/{arm64,amd64}/include/uapi/asm/unistd.h,与 x/sys v0.48 的 SYS_*
// 一致(linux 构建下自检)。arm64 与 amd64 的号不同(如 ptrace 117/101),
// 过滤器先核对审计架构、再比较系统调用号,未知架构一律 EPERM(amd64 上的
// 32 位兼容 ABI 也因此被拒)。
type filterArch struct {
	audit    uint32 // seccomp_data.arch:arm64=0xc00000b7,amd64=0xc000003e
	socketNr uint32 // socket 系统调用号:arm64=198,amd64=41
	denied   []uint32
}

var filterArchs = map[string]filterArch{
	"arm64": {
		audit:    0xc00000b7, // AUDIT_ARCH_AARCH64
		socketNr: 198,
		denied: []uint32{
			117, // ptrace
			270, // process_vm_readv
			271, // process_vm_writev
			40,  // mount
			39,  // umount2
			97,  // unshare
			268, // setns
			219, // keyctl
			280, // bpf
			241, // perf_event_open
			104, // kexec_load
			425, // io_uring_setup(IORING_OP_SOCKET 可绕过 socket() 过滤)
			426, // io_uring_enter
			427, // io_uring_register
		},
	},
	"amd64": {
		audit:    0xc000003e, // AUDIT_ARCH_X86_64
		socketNr: 41,
		denied: []uint32{
			101, // ptrace
			310, // process_vm_readv
			311, // process_vm_writev
			165, // mount
			166, // umount2
			272, // unshare
			308, // setns
			250, // keyctl
			321, // bpf
			298, // perf_event_open
			246, // kexec_load
			425, // io_uring_setup
			426, // io_uring_enter
			427, // io_uring_register
		},
	},
}

func filterArchFor(goarch string) (filterArch, error) {
	a, ok := filterArchs[goarch]
	if !ok {
		return a, fmt.Errorf("sandbox: 不支持的架构 %s(仅 arm64/amd64)", goarch)
	}
	return a, nil
}

// buildSeccompFilter 构造 seccomp 经典 BPF 程序(默认放行 + 黑名单 EPERM):
// 先核对审计架构,再对禁用列表逐个 EPERM;denyNonUnixSocket 时 socket()
// 仅放行 AF_UNIX(堵 UDP/RAW/ICMP)。纯函数,不安装,可跨平台单测;
// seccomp_data 布局:nr@0、arch@4、args[0] 低 32 位@16(小端)。
func buildSeccompFilter(goarch string, denyNonUnixSocket bool) ([]sockFilter, error) {
	a, err := filterArchFor(goarch)
	if err != nil {
		return nil, err
	}
	f := []sockFilter{
		{Code: bpfLDWABS, K: 4},                          // 加载 arch
		{Code: bpfJmpJEQK, Jt: 1, K: a.audit},            // 匹配则跳过下一条
		{Code: bpfRetK, K: seccompRetErrno | errnoEPERM}, // 未知架构一律 EPERM
		{Code: bpfLDWABS, K: 0},                          // 加载 nr
	}
	for _, nr := range a.denied {
		f = append(f,
			sockFilter{Code: bpfJmpJEQK, Jf: 1, K: nr},
			sockFilter{Code: bpfRetK, K: seccompRetErrno | errnoEPERM},
		)
	}
	if denyNonUnixSocket {
		// socket(domain) 的 domain != AF_UNIX 时 EPERM。socketpair 是另一个
		// 系统调用,不受影响。注意:NetConnectPorts 时不得设置本开关,否则
		// connect 在 Landlock 端口规则生效前就被 EPERM。
		f = append(f,
			sockFilter{Code: bpfJmpJEQK, Jf: 3, K: a.socketNr}, // 非 socket → 直达放行
			sockFilter{Code: bpfLDWABS, K: 16},                 // 加载 args[0](domain)
			sockFilter{Code: bpfJmpJEQK, Jt: 1, K: afUnix},     // AF_UNIX → 跳过 EPERM
			sockFilter{Code: bpfRetK, K: seccompRetErrno | errnoEPERM},
		)
	}
	return append(f, sockFilter{Code: bpfRetK, K: seccompRetAllow}), nil
}
