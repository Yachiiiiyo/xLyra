// Package execd 提供每会话一个的沙箱命令执行器:Pi 经 Unix 套接字提交命令,
// xLyra 主进程以 root 通过 sandbox.RunCommand 执行,串行处理并按 ExecUID
// 断言零残留;同时提供 JSONL 审计与 fail-closed 的启动自检。
package execd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxRequestLineBytes = 1 << 20
	maxCommandBytes     = 64 << 10
	maxChunkBytes       = 32 << 10
)

var (
	errLineTooLong = errors.New("请求行过长")
	errCmdTooLong  = errors.New("命令过长")
	errEmptyCmd    = errors.New("命令为空")
	errCwdMissing  = errors.New("工作目录不存在")
	errCwdOutside  = errors.New("工作目录不在会话工作区内")
	errOutputLimit = errors.New("输出超过上限,命令已终止")
	errResidual    = errors.New("清理后仍有残留进程")
)

type request struct {
	Cmd     string `json:"cmd"`
	Cwd     string `json:"cwd"`
	Timeout int    `json:"timeout"`
}

type chunkMsg struct {
	T string `json:"t"`
	D string `json:"d"`
}

type exitMsg struct {
	T        string `json:"t"`
	Code     int    `json:"code"`
	Signal   string `json:"signal"`
	TimedOut bool   `json:"timed_out"`
	Reaped   int    `json:"reaped"`
	Error    string `json:"error"`
}

func readRequestLine(r io.Reader) ([]byte, error) {
	// LimitReader 保证没有换行的请求也不会令缓冲区无限增长。
	line, err := bufio.NewReader(io.LimitReader(r, maxRequestLineBytes+1)).ReadBytes('\n')
	if len(line) > maxRequestLineBytes {
		return nil, errLineTooLong
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(bytes.TrimSpace(line)) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("空请求")
	}
	return bytes.TrimSpace(line), nil
}

func parseRequest(line []byte) (request, error) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return req, fmt.Errorf("请求格式错误: %w", err)
	}
	if req.Cmd == "" {
		return req, errEmptyCmd
	}
	if len(req.Cmd) > maxCommandBytes {
		return req, errCmdTooLong
	}
	return req, nil
}

func errorExit(err error) exitMsg {
	return exitMsg{T: "exit", Code: 1, Error: err.Error()}
}

func waitInfo(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var ee interface {
		ExitCode() int
	}
	if errors.As(err, &ee) {
		if ws, ok := err.(interface{ Sys() any }); ok {
			if status, ok := ws.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				sig := status.Signal()
				return 128 + int(sig), signalName(sig)
			}
		}
		if code := ee.ExitCode(); code >= 0 {
			return code, ""
		}
	}
	return 1, ""
}

func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGHUP: "HUP", syscall.SIGINT: "INT", syscall.SIGQUIT: "QUIT",
		syscall.SIGKILL: "KILL", syscall.SIGSEGV: "SEGV", syscall.SIGPIPE: "PIPE",
		syscall.SIGALRM: "ALRM", syscall.SIGTERM: "TERM",
	}
	if name := names[sig]; name != "" {
		return name
	}
	return strings.TrimPrefix(sig.String(), "SIG")
}

type outputCounter struct {
	mu    sync.Mutex
	total int64
	max   int64
}

func (c *outputCounter) add(n int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(n)
	return c.total, c.total > c.max
}

func (c *outputCounter) value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// runSocketRequest 是自检与集成测试共用的小客户端;调用方身份决定能否穿过
// 套接字目录及 0600 权限。收到 exit 即返回,error 字段保留在 exitMsg 中。
func runSocketRequest(sockPath string, req request) ([]byte, []byte, exitMsg, error) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, nil, exitMsg{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, nil, exitMsg{}, err
	}
	var stdout, stderr bytes.Buffer
	var exit exitMsg
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 2*maxRequestLineBytes)
	seenExit := false
	for scanner.Scan() {
		var head struct {
			T string `json:"t"`
			D string `json:"d"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &head); err != nil {
			return nil, nil, exitMsg{}, err
		}
		switch head.T {
		case "out", "err":
			data, err := base64.StdEncoding.DecodeString(head.D)
			if err != nil {
				return nil, nil, exitMsg{}, err
			}
			if head.T == "out" {
				stdout.Write(data)
			} else {
				stderr.Write(data)
			}
		case "exit":
			if err := json.Unmarshal(scanner.Bytes(), &exit); err != nil {
				return nil, nil, exitMsg{}, err
			}
			seenExit = true
		default:
			return nil, nil, exitMsg{}, fmt.Errorf("未知响应类型 %q", head.T)
		}
		if seenExit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, exitMsg{}, err
	}
	if !seenExit {
		return nil, nil, exitMsg{}, errors.New("连接关闭前未收到 exit")
	}
	return stdout.Bytes(), stderr.Bytes(), exit, nil
}
