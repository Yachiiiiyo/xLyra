package sandbox

import (
	"fmt"
	"reflect"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// 编译期对照:BPF/seccomp 常量与 x/sys 定义一致(不等时数组常量下标越界、
// 编译失败;仅 linux 构建可见)。系统调用号表另由 verifyFilterArchTable 在
// 安装前运行期核对。
var _ = [1]struct{}{}[uint(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS)-bpfLDWABS]
var _ = [1]struct{}{}[uint(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K)-bpfJmpJEQK]
var _ = [1]struct{}{}[uint(unix.BPF_RET|unix.BPF_K)-bpfRetK]
var _ = [1]struct{}{}[uint(unix.SECCOMP_RET_ALLOW)-seccompRetAllow]
var _ = [1]struct{}{}[uint(unix.SECCOMP_RET_ERRNO)-seccompRetErrno]
var _ = [1]struct{}{}[uint(unix.EPERM)-errnoEPERM]
var _ = [1]struct{}{}[uint(unix.AF_UNIX)-afUnix]

// verifyFilterArchTable 在安装前核对当前架构的过滤器参数与 x/sys 常量一致,
// 防止手抄系统调用号漂移。
func verifyFilterArchTable() error {
	a, err := filterArchFor(runtime.GOARCH)
	if err != nil {
		return err
	}
	want := []uint32{
		unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_MOUNT,
		unix.SYS_UMOUNT2,
		unix.SYS_UNSHARE,
		unix.SYS_SETNS,
		unix.SYS_KEYCTL,
		unix.SYS_BPF,
		unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_KEXEC_LOAD,
		unix.SYS_IO_URING_SETUP,
		unix.SYS_IO_URING_ENTER,
		unix.SYS_IO_URING_REGISTER,
	}
	switch runtime.GOARCH {
	case "arm64":
		if a.audit != unix.AUDIT_ARCH_AARCH64 {
			return fmt.Errorf("sandbox: arm64 审计架构常量不符:%#x", a.audit)
		}
	case "amd64":
		if a.audit != unix.AUDIT_ARCH_X86_64 {
			return fmt.Errorf("sandbox: amd64 审计架构常量不符:%#x", a.audit)
		}
	}
	if a.socketNr != uint32(unix.SYS_SOCKET) {
		return fmt.Errorf("sandbox: %s 的 socket 系统调用号不符:%d", runtime.GOARCH, a.socketNr)
	}
	if !reflect.DeepEqual(a.denied, want) {
		return fmt.Errorf("sandbox: %s 的禁用系统调用表与 x/sys 常量不一致", runtime.GOARCH)
	}
	return nil
}

// installSeccomp 用 seccomp(2) 的 TSYNC 把过滤器装到整个线程组(Go 运行时
// 的每个线程都受限,之后的 exec 不会解除);旧内核不支持 TSYNC 时退回
// prctl(仅当前线程,LockOSThread + 随后 exec 使其成为幸存线程)。
func installSeccomp(filters []sockFilter) error {
	if err := verifyFilterArchTable(); err != nil {
		return err
	}
	prog := make([]unix.SockFilter, len(filters))
	for i, f := range filters {
		prog[i] = unix.SockFilter{Code: f.Code, Jt: f.Jt, Jf: f.Jf, K: f.K}
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	_, _, errno := unix.Syscall(
		unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		uintptr(unsafe.Pointer(&fprog)),
	)
	runtime.KeepAlive(&fprog)
	runtime.KeepAlive(&prog[0])
	if errno == 0 {
		return nil
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, uintptr(unix.SECCOMP_MODE_FILTER), uintptr(unsafe.Pointer(&fprog)), 0, 0); err != nil {
		return fmt.Errorf("seccomp 安装失败(系统调用 errno %v,prctl %w)", errno, err)
	}
	return nil
}
