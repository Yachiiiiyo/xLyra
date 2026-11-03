//go:build linux

package sandbox

import (
	"reflect"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// TestFilterArchTableMatchesXSys:linux 构建下,手抄的系统调用号表必须与
// x/sys 当前编译目标的常量完全一致(生产安装路径 verifyFilterArchTable
// 也会做同一检查)。
func TestFilterArchTableMatchesXSys(t *testing.T) {
	if err := verifyFilterArchTable(); err != nil {
		t.Fatal(err)
	}
	a := filterArchs[runtime.GOARCH]
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
	if !reflect.DeepEqual(a.denied, want) {
		t.Fatalf("禁用表与 x/sys 常量不一致:%v", a.denied)
	}
	switch runtime.GOARCH {
	case "arm64":
		if a.audit != unix.AUDIT_ARCH_AARCH64 || a.socketNr != unix.SYS_SOCKET {
			t.Fatalf("arm64 参数不符:%+v", a)
		}
	case "amd64":
		if a.audit != unix.AUDIT_ARCH_X86_64 || a.socketNr != unix.SYS_SOCKET {
			t.Fatalf("amd64 参数不符:%+v", a)
		}
	}
}
