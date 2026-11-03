package isolation

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// probeTimeout 是单个探针子进程的墙钟上限。
const probeTimeout = 5 * time.Second

// probeResult 是一次探针的结果;code<0 表示没能启动或被信号杀死。
type probeResult struct {
	code int
	out  string // stdout,仅用于内部判断,绝不进日志
	err  string // stderr 摘要,只含路径与错误信息
}

// runProbe 以给定凭据执行 /bin/sh -c script:环境清空(因此脚本里一律用
// 绝对路径命令)、cwd=/、单次 5 秒超时;结束后对整个进程组补发 SIGKILL,
// 保证探针子进程零残留。cred 为 nil 时以当前进程身份(root)执行,用作
// 正向对照。
func runProbe(parent context.Context, cred *syscall.Credential, script string) probeResult {
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = []string{}
	cmd.Dir = "/"
	attr := &syscall.SysProcAttr{Setpgid: true}
	if cred != nil {
		attr.Credential = cred
	}
	cmd.SysProcAttr = attr
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// 超时被杀后,进程组里可能还有 sh 派生的子进程
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	code := 0
	if err != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if code < 0 {
				code = -1
			}
		}
	}
	return probeResult{code: code, out: stdout.String(), err: brief(stderr.String())}
}

func errSuffix(r probeResult) string {
	if r.err == "" {
		return ""
	}
	return ":" + r.err
}

// brief 压缩探针 stderr 供 Detail 使用(不含任何文件内容)。
func brief(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// q 用单引号包裹路径(路径均由本包或调用方生成,不含引号)。
func q(p string) string { return "'" + p + "'" }
