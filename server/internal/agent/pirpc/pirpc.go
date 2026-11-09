//go:build !windows

// Package pirpc 以子进程方式驱动 Pi 的 RPC 模式:stdin/stdout 承载 LF 分隔的 JSONL,
// stderr 只作诊断。关键协议事实(见 Pi docs/rpc.md 与 docs/pi-agent-integration/07):
//   - 单行可达数 MB,必须按字节读到 LF,不能用默认 bufio.Scanner;
//   - type=="response" 且 id 匹配的是命令应答,其余(含 agent_settled 等会话事件)走事件通道;
//   - prompt 的应答只表示受理,abort 是取消命令,agent_settled 才代表运行收口。
//
// 本包依赖 Setpgid 与进程组信号,只在 Unix 上可用。
package pirpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultStderrLimit = 64 << 10
	defaultEventBuffer = 256
	defaultCloseGrace  = 5 * time.Second
	// stderr 单行写日志的上限,避免一条超长诊断刷屏。
	stderrLogLineMax = 4 << 10
	// bufio 缓冲大小只影响吞吐;ReadBytes 的单行长度不受它限制。
	stdioBufferSize = 64 << 10
	// Abort 等待 agent_settled 的轮询间隔。
	abortPollInterval = 20 * time.Millisecond
)

// ErrProcessExited 在进程退出后调用 Call/Send,或等待应答期间进程退出时返回。
var ErrProcessExited = errors.New("pirpc: 进程已退出")

// Config 描述如何拉起一个 Pi RPC 子进程。
type Config struct {
	// Command 是可执行文件(node 或 pi);Args 含入口脚本与 --mode rpc 等参数。
	Command string
	Args    []string
	// Dir 是子进程工作目录,空串表示继承父进程当前目录。
	Dir string
	// Env 是子进程的完整环境变量,由调用方构造;本包不混入 os.Environ。
	Env []string
	// Logger 用于 stderr 诊断日志(Debug 级),nil 时用 slog.Default()。
	Logger *slog.Logger
	// StderrLimit 是 ExitInfo.Stderr 保留的尾部字节数,<=0 时取 64KB。
	StderrLimit int
	// EventBuffer 是事件通道容量,<=0 时取 256;通道满时读循环阻塞(背压),不丢事件。
	EventBuffer int
	// CloseGrace 是 Close 关闭 stdin 后等待进程自行退出的时长,<=0 时取 5s。
	CloseGrace time.Duration
	// Credential 非 nil 时以指定 uid/gid 启动子进程(uid 隔离);nil 时行为
	//完全不变(与宿主同身份)。
	Credential *syscall.Credential
}

// ExitInfo 汇总子进程的退出结果与 stdout/stderr 统计。
type ExitInfo struct {
	Code     int    // 退出码;被信号杀死时为 -1
	Signal   string // 被信号杀死时的信号名(如 "killed"),正常退出为空
	Err      error  // 底层 wait 错误:非零退出码或被信号杀死时非 nil
	Stderr   string // stderr 尾部(至多 StderrLimit 字节)
	BadLines int    // stdout 中无法解析为 JSON 的行数(跳过不中断)
	MaxLine  int    // stdout 观察到的最大单行字节数(含换行符)
}

type exitState struct {
	code   int
	signal string
	err    error
}

type callResult struct {
	data json.RawMessage
	err  error
}

// Process 表示一个 Pi RPC 子进程。所有方法并发安全;Wait 保证在 Events 通道关闭后返回。
type Process struct {
	cmd    *exec.Cmd
	stdin  *os.File // 子进程 stdin 的父进程写端
	events chan json.RawMessage
	logger *slog.Logger

	writeMu     sync.Mutex // 保护 stdin 写入与 stdinClosed
	stdinClosed bool

	mu      sync.Mutex // 保护 exited/pending/nextID/exit
	exited  bool
	pending map[string]chan callResult
	nextID  int
	exit    exitState

	// 已见 agent_settled 事件数,Abort 据此判断运行是否收口。
	settled atomic.Int64

	// badLines/maxLine 只由读循环写,读Done 关闭后才能读。
	badLines int
	maxLine  int

	procDone   chan struct{} // cmd.Wait 返回且未决 Call 已全部失败后关闭
	readDone   chan struct{} // stdout EOF 且 Events 通道已关闭后关闭
	stderrDone chan struct{} // stderr EOF 后关闭

	stderrMu   sync.Mutex // 保护 stderrBuf
	stderrBuf  []byte
	stderrMax  int
	closeGrace time.Duration

	killOnce  sync.Once
	closeOnce sync.Once
	waitOnce  sync.Once
	waitInfo  ExitInfo
}

// sysProcAttr 组装子进程属性:独立进程组(Kill 时能连带 Pi 派生且未脱离
// 进程组的子进程);Credential 非 nil 时叠加 uid/gid 隔离。
func sysProcAttr(credential *syscall.Credential) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if credential != nil {
		attr.Credential = credential
	}
	return attr
}

// Start 拉起子进程并开始读写循环。ctx 取消等价于 Kill。
func Start(ctx context.Context, cfg Config) (*Process, error) {
	if cfg.Command == "" {
		return nil, errors.New("pirpc: Config.Command 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("pirpc: 启动前上下文已取消: %w", err)
	}
	stderrLimit := cfg.StderrLimit
	if stderrLimit <= 0 {
		stderrLimit = defaultStderrLimit
	}
	eventBuffer := cfg.EventBuffer
	if eventBuffer <= 0 {
		eventBuffer = defaultEventBuffer
	}
	closeGrace := cfg.CloseGrace
	if closeGrace <= 0 {
		closeGrace = defaultCloseGrace
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env
	cmd.SysProcAttr = sysProcAttr(cfg.Credential)

	// 手工管理管道而不是 StdoutPipe 等:exec.Cmd.Wait 会关闭 *Pipe 的读端,
	// 可能截断内核缓冲区里尚未读完的数据;手工管道由本包控制关闭时机。
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("pirpc: 创建 stdin 管道失败: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, fmt.Errorf("pirpc: 创建 stdout 管道失败: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("pirpc: 创建 stderr 管道失败: %w", err)
	}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	p := &Process{
		cmd:        cmd,
		stdin:      stdinW,
		events:     make(chan json.RawMessage, eventBuffer),
		logger:     logger,
		pending:    make(map[string]chan callResult),
		stderrMax:  stderrLimit,
		closeGrace: closeGrace,
		procDone:   make(chan struct{}),
		readDone:   make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, fmt.Errorf("pirpc: 启动 %s 失败: %w", cfg.Command, err)
	}
	// 子进程已持有各自的端点,父进程侧多余副本立即关闭:
	// 写端关闭后,子进程(组)退出时读端即 EOF。
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	go p.readStdout(stdoutR)
	go p.readStderr(stderrR)
	go p.waitProc()
	go func() {
		select {
		case <-ctx.Done():
			p.Kill()
		case <-p.procDone:
		}
	}()
	return p, nil
}

// Events 返回事件通道(原始 JSON 行,不含换行)。通道在进程退出且 stdout 读完时关闭;
// 通道满时读循环阻塞,把背压传导到 Pi 的 stdout,不丢事件。
func (p *Process) Events() <-chan json.RawMessage {
	return p.events
}

// Call 发送命令并等待同 id 的 response。命令里的 id 由本方法分配(覆盖传入值),
// 返回应答的原始 JSON(success 与否由调用方解析)。ctx 取消或进程退出时返回错误。
func (p *Process) Call(ctx context.Context, cmd map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	if p.exited {
		p.mu.Unlock()
		return nil, ErrProcessExited
	}
	p.nextID++
	id := fmt.Sprintf("req_%d", p.nextID)
	ch := make(chan callResult, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload := make(map[string]any, len(cmd)+1)
	maps.Copy(payload, cmd)
	payload["id"] = id
	encoded, err := json.Marshal(payload)
	if err != nil {
		p.takePending(id)
		return nil, fmt.Errorf("pirpc: 序列化命令 %v: %w", cmd["type"], err)
	}
	if err := p.write(encoded); err != nil {
		p.takePending(id)
		return nil, err
	}

	select {
	case res := <-ch:
		return res.data, res.err
	case <-ctx.Done():
		// 已投递但无人接收的结果直接丢弃。
		p.takePending(id)
		return nil, fmt.Errorf("pirpc: 等待 %v 应答: %w", cmd["type"], ctx.Err())
	}
}

// Send 只写 stdin 不等应答,用于 extension_ui_response 等无需关联命令的场景。
// id(若有)由调用方提供,本方法不覆盖。
func (p *Process) Send(cmd map[string]any) error {
	if p.hasExited() {
		return ErrProcessExited
	}
	encoded, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("pirpc: 序列化 %v: %w", cmd["type"], err)
	}
	return p.write(encoded)
}

// Abort 发送 abort 命令取消当前运行:最多等 grace 让 Pi 以 agent_settled 收口或自行退出,
// 超时或 ctx 取消则 Kill 整个进程组。进程已退出或没有进行中的运行时返回 nil;
// 强制终止时返回非 nil error。
func (p *Process) Abort(ctx context.Context, grace time.Duration) error {
	if p.hasExited() {
		return nil
	}
	deadline := time.Now().Add(grace)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	abortCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	before := p.settled.Load()
	resp, callErr := p.Call(abortCtx, map[string]any{"type": "abort"})
	switch {
	case callErr == nil:
	case errors.Is(callErr, ErrProcessExited):
		return nil
	case errors.Is(callErr, context.DeadlineExceeded), errors.Is(callErr, context.Canceled):
		// abort 未被应答或外部取消,落到下面按 deadline 判断是否强制清理
	default:
		return callErr
	}
	if callErr == nil {
		var accepted struct {
			Success bool `json:"success"`
		}
		_ = json.Unmarshal(resp, &accepted)
		if !accepted.Success {
			// 没有进行中的运行,不会有新的 agent_settled,无需等待。
			return nil
		}
	}
	for !p.hasExited() && p.settled.Load() <= before && time.Now().Before(deadline) {
		sleep := abortPollInterval
		if remain := time.Until(deadline); remain < sleep {
			sleep = remain
		}
		select {
		case <-ctx.Done():
			p.Kill()
			return fmt.Errorf("pirpc: abort 被取消,已强制终止进程组: %w", ctx.Err())
		case <-time.After(sleep):
		}
	}
	if p.hasExited() || p.settled.Load() > before {
		return nil
	}
	p.Kill()
	return fmt.Errorf("pirpc: abort 后 %v 内未收到 agent_settled,已强制终止进程组", grace)
}

// Kill 对整个进程组发送 SIGKILL,幂等,已退出时为空操作。
func (p *Process) Kill() error {
	p.killOnce.Do(func() {
		proc := p.cmd.Process
		if proc == nil {
			return
		}
		if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = proc.Kill() // 进程组信号失败时兜底杀组长
		}
	})
	return nil
}

// Close 请求有序退出:关 stdin,超过 CloseGrace 仍未退出则 Kill,最后等 Wait 收口。幂等。
// 注意:若调用方不再消费 Events,而读循环仍阻塞在通道发送上,Close 会一直等下去。
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		p.closeStdin()
		go func() {
			select {
			case <-time.After(p.closeGrace):
				p.Kill()
			case <-p.procDone:
			}
		}()
	})
	return p.Wait().Err
}

// Wait 阻塞到进程退出且 stdout/stderr 读完(Events 通道已关闭)后返回退出信息,可重复调用。
func (p *Process) Wait() ExitInfo {
	p.waitOnce.Do(func() {
		<-p.procDone
		<-p.readDone
		<-p.stderrDone
		p.waitInfo = ExitInfo{
			Code:     p.exit.code,
			Signal:   p.exit.signal,
			Err:      p.exit.err,
			Stderr:   p.stderrTail(),
			BadLines: p.badLines,
			MaxLine:  p.maxLine,
		}
	})
	return p.waitInfo
}

// PID 返回子进程 PID,未启动时为 0。
func (p *Process) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *Process) waitProc() {
	waitErr := p.cmd.Wait()
	p.closeStdin() // 进程已死,释放父进程持有的写端

	code, signal := 0, ""
	if st := p.cmd.ProcessState; st != nil {
		code = st.ExitCode()
		if status, ok := st.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signal = status.Signal().String()
		}
	}

	p.mu.Lock()
	p.exited = true
	for id, ch := range p.pending {
		select {
		case ch <- callResult{err: ErrProcessExited}:
		default: // 结果已被投递,丢弃
		}
		delete(p.pending, id)
	}
	p.exit = exitState{code: code, signal: signal, err: waitErr}
	p.mu.Unlock()
	close(p.procDone)
}

func (p *Process) readStdout(r *os.File) {
	defer func() {
		close(p.events)
		close(p.readDone)
		_ = r.Close()
	}()
	reader := bufio.NewReaderSize(r, stdioBufferSize)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			p.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (p *Process) handleLine(raw []byte) {
	line := raw
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	if len(line) == 0 {
		return
	}
	if len(raw) > p.maxLine {
		p.maxLine = len(raw)
	}
	if !json.Valid(line) {
		p.badLines++ // 非法 JSON 行计数并跳过,不中断
		return
	}
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	// 已验证合法 JSON;字段形态不符时 head 保持零值,按事件处理。
	_ = json.Unmarshal(line, &head)
	if head.Type == "response" && head.ID != "" {
		if ch := p.takePending(head.ID); ch != nil {
			select {
			case ch <- callResult{data: json.RawMessage(line)}:
			default: // 进程退出路径已投递失败结果,丢弃迟到的应答
			}
			return
		}
	}
	if head.Type == "agent_settled" {
		p.settled.Add(1)
	}
	p.events <- json.RawMessage(line)
}

func (p *Process) readStderr(r *os.File) {
	defer func() {
		close(p.stderrDone)
		_ = r.Close()
	}()
	reader := bufio.NewReaderSize(r, stdioBufferSize)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			p.logStderrLine(line)
			p.appendStderr(line)
		}
		if err != nil {
			return
		}
	}
}

func (p *Process) logStderrLine(raw []byte) {
	msg := raw
	if n := len(msg); n > 0 && msg[n-1] == '\n' {
		msg = msg[:n-1]
	}
	if n := len(msg); n > 0 && msg[n-1] == '\r' {
		msg = msg[:n-1]
	}
	if len(msg) == 0 {
		return
	}
	if len(msg) > stderrLogLineMax {
		msg = append(msg[:stderrLogLineMax:stderrLogLineMax], []byte("…(已截断)")...)
	}
	p.logger.Debug("Pi stderr", "line", string(msg))
}

func (p *Process) appendStderr(line []byte) {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	p.stderrBuf = append(p.stderrBuf, line...)
	// 超过上限两倍才丢弃头部,摊平拷贝开销;stderrTail 再截到精确上限。
	if excess := len(p.stderrBuf) - p.stderrMax; excess > p.stderrMax {
		p.stderrBuf = append(p.stderrBuf[:0], p.stderrBuf[excess:]...)
	}
}

func (p *Process) stderrTail() string {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	if len(p.stderrBuf) > p.stderrMax {
		return string(p.stderrBuf[len(p.stderrBuf)-p.stderrMax:])
	}
	return string(p.stderrBuf)
}

func (p *Process) write(encoded []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdinClosed {
		return fmt.Errorf("pirpc: 写入命令: %w", ErrProcessExited)
	}
	if _, err := p.stdin.Write(append(encoded, '\n')); err != nil {
		if p.hasExited() {
			return fmt.Errorf("pirpc: 写入命令: %w", ErrProcessExited)
		}
		return fmt.Errorf("pirpc: 写入 stdin: %w", err)
	}
	return nil
}

func (p *Process) closeStdin() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if !p.stdinClosed {
		p.stdinClosed = true
		_ = p.stdin.Close()
	}
}

func (p *Process) hasExited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

func (p *Process) takePending(id string) chan callResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := p.pending[id]
	delete(p.pending, id)
	return ch
}
