package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ChildCommand 构造 Pi 自己的启动命令(不经 run 模式):直接以目标凭据进入
// child 模式,由调用方设置 stdin/stdout/stderr(及需要时的 Dir)并 Start;
// Credential(主组、无补充组)、Setpgid、Pdeathsig 已设好。SystemRO 为 nil
// 时填 Pi 模式默认(整个 /etc)。
func ChildCommand(exe string, spec Spec, argv []string) (*exec.Cmd, error) {
	s, spec64, err := prepareSpec(spec, defaultPiSystemRO)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("sandbox: argv 为空")
	}
	args := append([]string{exe, helperCommand, "child", "--spec", spec64, "--"}, argv...)
	return &exec.Cmd{
		Path: exe,
		Args: args,
		Env:  s.Env,
		SysProcAttr: &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(s.UID), Gid: uint32(s.GID), Groups: []uint32{}},
			Setpgid:    true,
			Pdeathsig:  syscall.SIGKILL,
		},
	}, nil
}

// RunCommand 构造命令沙箱的启动命令:以 root 运行 run 模式,凭据、进程组、
// 超时与按 uid 清理由 helper 自己处理;调用方设置 stdin/stdout/stderr 并
// Start/Wait。SystemRO 为 nil 时填命令模式默认(不放开整个 /etc)。
func RunCommand(exe string, spec Spec, argv []string) (*exec.Cmd, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("sandbox: run 模式需要 root")
	}
	_, spec64, err := prepareSpec(spec, defaultCommandSystemRO)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("sandbox: argv 为空")
	}
	args := append([]string{exe, helperCommand, "run", "--spec", spec64, "--"}, argv...)
	return &exec.Cmd{Path: exe, Args: args}, nil
}

// prepareSpec 校验并按模式补默认 SystemRO,再编码。不改动调用方的 Spec。
func prepareSpec(spec Spec, defRO []string) (Spec, string, error) {
	if err := spec.Validate(); err != nil {
		return spec, "", err
	}
	if spec.SystemRO == nil {
		spec.SystemRO = defRO
	}
	spec64, err := encodeSpec(spec)
	if err != nil {
		return spec, "", fmt.Errorf("sandbox: %w", err)
	}
	return spec, spec64, nil
}
