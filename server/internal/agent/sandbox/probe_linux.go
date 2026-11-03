//go:build linux

package sandbox

// 生产探针由 child 在沙箱内 exec:<exe> __xlyra-sandbox probe。目标全部由
// XLYRA_PROBE_* 环境变量给出,结果为一行 JSON map[string]string;值是 ok、
// errno 名,或 uid/nnp/fork 的十进制值。探针不读取或回显文件内容,写入目标
// 一律追加固定 .xlyra-probe 后缀,便于调用方识别并删除。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func probeHelper() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "sandbox probe: 不接受额外参数")
		os.Exit(2)
	}
	res := runProductionProbe()
	b, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox probe:", err)
		os.Exit(2)
	}
	fmt.Println(string(b))
	os.Exit(0)
}

func runProductionProbe() map[string]string {
	res := map[string]string{
		"uid":      strconv.Itoa(os.Getuid()),
		"ptrace":   probePtrace(),
		"setuid0":  probeErrno(unix.Setuid(0)),
		"mknod":    probeErrno(unix.Mknod("mknod.xlyra-probe", unix.S_IFCHR|0o600, int(unix.Mkdev(1, 3)))),
		"dev_null": probeOpen("/dev/null", unix.O_RDWR),
		"dev_mem":  probeOpen("/dev/mem", unix.O_RDONLY),
	}
	if nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil {
		res["nnp"] = probeErrno(err)
	} else {
		res["nnp"] = strconv.Itoa(nnp)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	res["socket_inet"] = probeErrno(err)
	if err == nil {
		_ = unix.Close(fd)
	}
	for _, p := range probePaths("XLYRA_PROBE_READ") {
		res["read:"+p] = probeOpen(p, unix.O_RDONLY)
	}
	for _, p := range probePaths("XLYRA_PROBE_WRITE") {
		res["write:"+p] = probeWrite(p + ".xlyra-probe")
	}
	if target := os.Getenv("XLYRA_PROBE_TCP"); target != "" {
		res["tcp"] = probeDial("tcp", target)
	}
	if target := os.Getenv("XLYRA_PROBE_UDP"); target != "" {
		res["udp"] = probeUDP(target)
	}
	if target := os.Getenv("XLYRA_PROBE_UNIX"); target != "" {
		res["unix"] = probeDial("unix", target)
	}
	if target := os.Getenv("XLYRA_PROBE_ENVIRON_PID"); target != "" {
		if target == "self" {
			target = strconv.Itoa(os.Getpid())
		}
		_, err := os.ReadFile(filepath.Join("/proc", target, "environ"))
		res["environ"] = probeErrno(err)
	}
	if n, _ := strconv.Atoi(os.Getenv("XLYRA_PROBE_FORK")); n > 0 {
		res["fork"] = strconv.Itoa(probeFork(n))
	}
	return res
}

func probePaths(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func probeOpen(path string, flags int) string {
	fd, err := unix.Open(path, flags, 0)
	if err == nil {
		_ = unix.Close(fd)
	}
	return probeErrno(err)
}

func probeWrite(path string) string {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL, 0o600)
	if err != nil {
		return probeErrno(err)
	}
	_, writeErr := unix.Write(fd, []byte("x"))
	closeErr := unix.Close(fd)
	if writeErr != nil {
		return probeErrno(writeErr)
	}
	return probeErrno(closeErr)
}

func probeDial(network, target string) string {
	conn, err := net.DialTimeout(network, target, 2*time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	return probeErrno(err)
}

func probeUDP(target string) string {
	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return err.Error()
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return probeErrno(err)
	}
	_, writeErr := conn.Write([]byte("x"))
	closeErr := conn.Close()
	if writeErr != nil {
		return probeErrno(writeErr)
	}
	return probeErrno(closeErr)
}

func probePtrace() string {
	_, _, errno := unix.Syscall(unix.SYS_PTRACE, uintptr(unix.PTRACE_TRACEME), 0, 0)
	return probeErrno(errno)
}

func probeFork(n int) int {
	succeeded := 0
	for i := 0; i < n; i++ {
		cmd := exec.Command("/bin/sleep", "60")
		cmd.Env = []string{}
		if err := cmd.Start(); err != nil {
			break
		}
		succeeded++
	}
	return succeeded
}

func probeErrno(err error) string {
	if err == nil {
		return "ok"
	}
	var errno unix.Errno
	if errors.As(err, &errno) {
		if errno == 0 {
			return "ok"
		}
		if name := unix.ErrnoName(errno); name != "" {
			return name
		}
	}
	return err.Error()
}
