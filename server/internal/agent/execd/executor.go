package execd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"xlyra/server/internal/agent/sandbox"
)

const (
	defaultTimeout     = 120 * time.Second
	defaultMaxTimeout  = 600 * time.Second
	defaultMaxOutput   = 16 << 20
	cancelGrace        = 5 * time.Second
	closeDrainTimeout  = 30 * time.Second
	defaultCommandPath = "/usr/local/bin:/usr/bin:/bin"
)

// Config 是一个会话执行器的配置。身份与目录通常来自 isolation.PrepareSession。
type Config struct {
	Exe            string
	SessionDir     string
	SockDir        string
	PiUID          int
	ExecUID        int
	GID            int
	Workspace      string
	Tmp            string
	CtlSocketPath  string
	CtlNonce       string
	CLIBinDir      string
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
	MaxOutputBytes int64
	Limits         sandbox.Limits
	Audit          AuditSink
	Logger         *slog.Logger
}

// Executor 每会话一个;同一时刻只执行一条命令,避免按 uid 清理时误杀并行命令。
type Executor struct {
	cfg   Config
	log   *slog.Logger
	shell string
	sem   chan struct{}
	dying chan struct{}

	dieOnce sync.Once
	mu      sync.Mutex
	runID   string
	sock    string
	ln      net.Listener
	started bool
	closed  bool

	timerMu     sync.Mutex
	activeTimer *activeExecTimer
}

type activeExecTimer struct {
	mu        sync.Mutex
	conn      net.Conn
	writeMu   *sync.Mutex
	timer     *time.Timer
	paused    bool
	pauseRefs int
	startedAt time.Time
	remaining time.Duration
	onTimeout func()
	closed    bool
}

func New(cfg Config) (*Executor, error) {
	var err error
	if cfg.Exe == "" {
		cfg.Exe, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("execd: 获取可执行文件: %w", err)
		}
	}
	if cfg.SessionDir == "" || !filepath.IsAbs(cfg.SessionDir) {
		return nil, errors.New("execd: SessionDir 必须是非空绝对路径")
	}
	cfg.SessionDir = filepath.Clean(cfg.SessionDir)
	if cfg.SockDir == "" {
		cfg.SockDir = filepath.Join(cfg.SessionDir, "exec")
	}
	cfg.SockDir = filepath.Clean(cfg.SockDir)
	rel, err := filepath.Rel(cfg.SessionDir, cfg.SockDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, errors.New("execd: SockDir 必须在 SessionDir 之内")
	}
	if cfg.Tmp == "" {
		cfg.Tmp = filepath.Join(cfg.SessionDir, "exec-tmp")
	}
	if !filepath.IsAbs(cfg.Tmp) {
		return nil, errors.New("execd: Tmp 必须是绝对路径")
	}
	cfg.Tmp = filepath.Clean(cfg.Tmp)
	if cfg.Workspace == "" || !filepath.IsAbs(cfg.Workspace) {
		return nil, errors.New("execd: Workspace 必须是非空绝对路径")
	}
	cfg.Workspace = filepath.Clean(cfg.Workspace)
	if cfg.PiUID <= 0 || cfg.ExecUID <= 0 || cfg.GID <= 0 || cfg.PiUID == cfg.ExecUID {
		return nil, errors.New("execd: PiUID/ExecUID/GID 必须为正数且 PiUID 与 ExecUID 不同")
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = defaultTimeout
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = defaultMaxTimeout
	}
	if cfg.DefaultTimeout > cfg.MaxTimeout {
		cfg.DefaultTimeout = cfg.MaxTimeout
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutput
	}
	if cfg.Limits == (sandbox.Limits{}) {
		cfg.Limits = sandbox.Limits{
			NProc: 256, NoFile: 1024, FSize: 512 << 20, Core: 0,
			CPU: int64(cfg.MaxTimeout / time.Second),
		}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
		if _, err := os.Stat(shell); err != nil {
			return nil, errors.New("execd: 缺少 /bin/bash 与 /bin/sh")
		}
	}
	return &Executor{cfg: cfg, log: cfg.Logger, shell: shell, sem: make(chan struct{}, 1), dying: make(chan struct{})}, nil
}

// Start 创建执行器目录与套接字。目录在 listen 前保持 root:root 0700,
// 套接字先收紧到 PiUID 0600 后才把目录交给 PiUID,因此不存在“先放开再收紧”窗口。
func (e *Executor) Start() (string, error) {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return "", errors.New("execd: 已启动")
	}
	if e.closed {
		e.mu.Unlock()
		return "", errors.New("execd: 已关闭")
	}
	e.started = true
	e.mu.Unlock()
	if err := os.MkdirAll(e.cfg.SessionDir, 0o711); err != nil {
		return "", err
	}
	if err := makeOwnedDir(e.cfg.Tmp, e.cfg.ExecUID, e.cfg.GID); err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.cfg.SockDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(e.cfg.SockDir, 0o700); err != nil {
		return "", err
	}
	sock := filepath.Join(e.cfg.SockDir, "exec.sock")
	// sockaddr_un.sun_path 只有 108 字节,超长时 bind 只会报 invalid argument
	if len(sock) > 104 {
		return "", fmt.Errorf("execd: 套接字路径过长(%d > 104 字节):%s,请缩短 Agent 数据目录路径", len(sock), sock)
	}
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	cleanup := func(err error) (string, error) {
		_ = ln.Close()
		_ = os.Remove(sock)
		return "", err
	}
	if err := os.Chown(sock, e.cfg.PiUID, e.cfg.GID); err != nil {
		return cleanup(err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return cleanup(err)
	}
	if err := os.Chown(e.cfg.SockDir, e.cfg.PiUID, e.cfg.GID); err != nil {
		return cleanup(err)
	}
	if err := os.Chmod(e.cfg.SockDir, 0o700); err != nil {
		return cleanup(err)
	}
	e.mu.Lock()
	e.sock = sock
	e.ln = ln
	e.mu.Unlock()
	go e.acceptLoop(ln)
	return sock, nil
}

func makeOwnedDir(path string, uid, gid int) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func (e *Executor) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go e.handleConn(conn)
	}
}

// SetRun 设置后续审计记录的 runID,可在命令运行期间安全调用。
func (e *Executor) SetRun(runID string) {
	e.mu.Lock()
	e.runID = runID
	e.mu.Unlock()
}

func (e *Executor) currentRunID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runID
}

func (e *Executor) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	var removeErr error
	ln, sock := e.ln, e.sock
	e.mu.Unlock()
	e.dieOnce.Do(func() { close(e.dying) })
	if ln != nil {
		_ = ln.Close()
	}
	if sock != "" {
		removeErr = os.Remove(sock)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
	}
	// 等当前命令收到 dying、终止并释放串行槽;超时后仍做一次按 uid 兜底清理。
	timer := time.NewTimer(closeDrainTimeout)
	select {
	case e.sem <- struct{}{}:
		<-e.sem
	case <-timer.C:
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	_, _, _ = sandbox.KillUID(e.cfg.ExecUID)
	return removeErr
}

func (e *Executor) handleConn(conn net.Conn) {
	defer conn.Close()
	started := time.Now()
	line, err := readRequestLine(conn)
	if err != nil {
		e.finishRejected(conn, request{}, started, err)
		return
	}
	req, err := parseRequest(line)
	if err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	cwd, err := e.resolveCwd(req.Cwd)
	if err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	disconnected := watchDisconnect(conn)
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-disconnected:
		e.finishRejected(conn, req, started, errors.New("等待执行时客户端断开"))
		return
	case <-e.dying:
		e.finishRejected(conn, req, started, errors.New("执行器已关闭"))
		return
	}
	select {
	case <-disconnected:
		e.finishRejected(conn, req, started, errors.New("等待执行时客户端断开"))
		return
	case <-e.dying:
		e.finishRejected(conn, req, started, errors.New("执行器已关闭"))
		return
	default:
	}
	e.runCommand(conn, req, cwd, disconnected, started)
}

func watchDisconnect(conn net.Conn) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		var one [1]byte
		_, _ = conn.Read(one[:]) // EOF 或对端错误都表示客户端已离开
	}()
	return ch
}

func (e *Executor) finishRejected(conn net.Conn, req request, started time.Time, err error) {
	exit := errorExit(err)
	e.record(req, req.Cwd, started, exit, false, 0)
	_ = writeProtocolLine(conn, nil, exit)
}

func (e *Executor) resolveCwd(cwd string) (string, error) {
	if cwd == "" {
		cwd = e.cfg.Workspace
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(e.cfg.Workspace, cwd)
	}
	cwd = filepath.Clean(cwd)
	wsReal, err := filepath.EvalSymlinks(e.cfg.Workspace)
	if err != nil {
		return "", errCwdMissing
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", errCwdMissing
	}
	if !pathInside(wsReal, real) {
		return "", errCwdOutside
	}
	return real, nil
}

func pathInside(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func (e *Executor) commandTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return e.cfg.DefaultTimeout
	}
	t := time.Duration(seconds) * time.Second
	if t > e.cfg.MaxTimeout {
		return e.cfg.MaxTimeout
	}
	return t
}

type runOutcome struct {
	mu          sync.Mutex
	overflow    bool
	cancelled   bool
	terminate   sync.Once
	streamsDone chan struct{}
	finished    chan struct{}
}

func (o *runOutcome) set(overflow, cancelled bool) {
	o.mu.Lock()
	if overflow {
		o.overflow = true
	}
	if cancelled {
		o.cancelled = true
	}
	o.mu.Unlock()
}

func (o *runOutcome) get() (bool, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow, o.cancelled
}

// PauseTimer 暂停当前正在执行命令的超时计时，并向 xlyra.ts 发送暂停帧（主要用于审批弹窗等待期间）。
func (e *Executor) PauseTimer() {
	if e == nil {
		return
	}
	e.timerMu.Lock()
	at := e.activeTimer
	e.timerMu.Unlock()
	if at == nil {
		return
	}
	at.pause()
}

// ResumeTimer 恢复先前暂停的命令超时计时，并通知 xlyra.ts 继续计时。
func (e *Executor) ResumeTimer() {
	if e == nil {
		return
	}
	e.timerMu.Lock()
	at := e.activeTimer
	e.timerMu.Unlock()
	if at == nil {
		return
	}
	at.resume()
}

func (at *activeExecTimer) pause() {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.closed {
		return
	}
	at.pauseRefs++
	if at.paused {
		return
	}
	at.paused = true
	if at.timer != nil {
		elapsed := time.Since(at.startedAt)
		if at.remaining > elapsed {
			at.remaining -= elapsed
		} else {
			at.remaining = time.Second
		}
		at.timer.Stop()
	}
	_ = writeProtocolLine(at.conn, at.writeMu, timerMsg{T: "timer", Action: "pause"})
}

func (at *activeExecTimer) resume() {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.closed || !at.paused {
		return
	}
	if at.pauseRefs > 1 {
		at.pauseRefs--
		return
	}
	at.pauseRefs = 0
	at.paused = false
	if at.onTimeout != nil {
		rem := at.remaining
		if rem <= 0 {
			rem = time.Second
		}
		at.startedAt = time.Now()
		at.timer = time.AfterFunc(rem, at.onTimeout)
	}
	_ = writeProtocolLine(at.conn, at.writeMu, timerMsg{T: "timer", Action: "resume"})
}

func (at *activeExecTimer) stop() {
	at.mu.Lock()
	defer at.mu.Unlock()
	if at.closed {
		return
	}
	at.closed = true
	if at.timer != nil {
		at.timer.Stop()
	}
}

func (e *Executor) runCommand(conn net.Conn, req request, cwd string, disconnected <-chan struct{}, started time.Time) {
	timeout := e.commandTimeout(req.Timeout)
	pathEnv := defaultCommandPath
	if dir := strings.TrimSpace(e.cfg.CLIBinDir); dir != "" {
		pathEnv = dir + ":" + pathEnv
	}
	env := []string{
		"PATH=" + pathEnv, "HOME=" + e.cfg.Tmp, "TMPDIR=" + e.cfg.Tmp,
		"LANG=C.UTF-8", "TERM=dumb",
	}
	var readOnly, readWrite []string
	if dir := strings.TrimSpace(e.cfg.CLIBinDir); dir != "" {
		readOnly = append(readOnly, dir)
	}
	if sock := strings.TrimSpace(e.cfg.CtlSocketPath); sock != "" {
		readWrite = append(readWrite, filepath.Dir(sock))
		env = append(env, "XLYRA_CTL_SOCK="+sock)
		if e.cfg.CtlNonce != "" {
			env = append(env, "XLYRA_CTL_NONCE="+e.cfg.CtlNonce)
		}
	}
	// 当启用控制面时，超时改由 Go 侧可暂停定时器托管，避免人工审批期间子进程被 sandbox 强杀。
	sandboxTimeout := int(timeout / time.Second)
	useManagedTimer := strings.TrimSpace(e.cfg.CtlSocketPath) != ""
	if useManagedTimer {
		sandboxTimeout = 0
	}
	spec := sandbox.Spec{
		UID: e.cfg.ExecUID, GID: e.cfg.GID,
		Workspace: e.cfg.Workspace, Tmp: e.cfg.Tmp, Cwd: cwd,
		ReadOnly: readOnly, ReadWrite: readWrite,
		// umask 为 0:共享工作区里不同会话身份创建的文件要互相可写。
		Net: sandbox.NetNone, Umask: 0, Limits: e.cfg.Limits,
		Timeout: sandboxTimeout,
		Env:     env,
	}
	cmd, err := sandbox.RunCommand(e.cfg.Exe, spec, []string{e.shell, "-c", req.Cmd})
	if err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	if err := cmd.Start(); err != nil {
		e.finishRejected(conn, req, started, err)
		return
	}
	outcome := &runOutcome{streamsDone: make(chan struct{}), finished: make(chan struct{})}
	terminate := func() {
		outcome.terminate.Do(func() { go e.terminateCommand(cmd, stdout, stderr, outcome.streamsDone) })
	}
	overflow := make(chan struct{})
	managedTimedOut := make(chan struct{})
	var managedTimeoutOnce sync.Once
	var overflowOnce sync.Once
	counter := &outputCounter{max: e.cfg.MaxOutputBytes}
	var writeMu sync.Mutex

	at := &activeExecTimer{
		conn:      conn,
		writeMu:   &writeMu,
		startedAt: time.Now(),
		remaining: timeout,
	}
	if useManagedTimer && timeout > 0 {
		at.onTimeout = func() {
			managedTimeoutOnce.Do(func() { close(managedTimedOut) })
		}
		at.timer = time.AfterFunc(timeout, at.onTimeout)
	}
	e.timerMu.Lock()
	e.activeTimer = at
	e.timerMu.Unlock()
	defer func() {
		at.stop()
		e.timerMu.Lock()
		if e.activeTimer == at {
			e.activeTimer = nil
		}
		e.timerMu.Unlock()
	}()

	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		e.streamOutput(conn, &writeMu, stdout, "out", counter, func() { overflowOnce.Do(func() { close(overflow) }) })
	}()
	go func() {
		defer readers.Done()
		e.streamOutput(conn, &writeMu, stderr, "err", counter, func() { overflowOnce.Do(func() { close(overflow) }) })
	}()
	go func() {
		readers.Wait()
		close(outcome.streamsDone)
	}()
	go func() {
		select {
		case <-disconnected:
			outcome.set(false, true)
			terminate()
		case <-overflow:
			outcome.set(true, false)
			terminate()
		case <-managedTimedOut:
			terminate()
		case <-e.dying:
			outcome.set(false, true)
			terminate()
		case <-outcome.finished:
		}
	}()
	// helper 在退出前已按 uid 清理,所有继承输出写端的后代也随之消失;
	// 先等读端 EOF 再 Wait,避免 Wait 提前关闭管道导致末尾输出丢失。
	<-outcome.streamsDone
	waitErr := cmd.Wait()
	close(outcome.finished)
	code, signal := waitInfo(waitErr)
	timedOutByManaged := false
	select {
	case <-managedTimedOut:
		timedOutByManaged = true
		code = 124
	default:
	}
	reaped, residual := e.cleanupExecUID()
	overflowed, cancelled := outcome.get()
	exit := exitMsg{T: "exit", Code: code, Signal: signal, TimedOut: code == 124 || timedOutByManaged, Reaped: reaped}
	var errs []string
	if overflowed {
		errs = append(errs, errOutputLimit.Error())
	}
	if residual {
		errs = append(errs, errResidual.Error())
		e.log.Error("execd: 清理后仍有残留进程", "session", filepath.Base(e.cfg.SessionDir), "exec_uid", e.cfg.ExecUID)
	}
	exit.Error = strings.Join(errs, "; ")
	// 先落审计再回结果:客户端拿到结果时审计记录必须已经存在
	e.record(req, cwd, started, exit, cancelled, counter.value())
	_ = writeProtocolLine(conn, &writeMu, exit)
}

func (e *Executor) streamOutput(conn net.Conn, writeMu *sync.Mutex, r io.Reader, kind string, counter *outputCounter, over func()) {
	buf := make([]byte, maxChunkBytes)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, exceeded := counter.add(n); exceeded {
				over()
				return
			}
			_ = writeProtocolLine(conn, writeMu, chunkMsg{T: kind, D: base64.StdEncoding.EncodeToString(buf[:n])})
		}
		if err != nil {
			return
		}
	}
}

func writeProtocolLine(conn net.Conn, mu *sync.Mutex, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	_, err = conn.Write(line)
	return err
}

func (e *Executor) terminateCommand(cmd *exec.Cmd, stdout, stderr io.ReadCloser, streamsDone <-chan struct{}) {
	if cmd.Process != nil {
		_ = unix.Kill(cmd.Process.Pid, unix.SIGTERM)
	}
	select {
	case <-streamsDone:
		return
	case <-time.After(cancelGrace):
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_, _, _ = sandbox.KillUID(e.cfg.ExecUID)
	// 异常卡死时最后强制关闭读端,保证执行器不泄漏串行槽。
	select {
	case <-streamsDone:
	case <-time.After(cancelGrace):
		_ = stdout.Close()
		_ = stderr.Close()
	}
}

func (e *Executor) cleanupExecUID() (int, bool) {
	alive := 0
	for _, p := range sandbox.ProcsOfUID(e.cfg.ExecUID) {
		if p.State != "Z" {
			alive++
		}
	}
	if alive == 0 {
		return 0, false
	}
	killed, remaining, err := sandbox.KillUID(e.cfg.ExecUID)
	return killed, err != nil || remaining > 0
}

func (e *Executor) record(req request, cwd string, started time.Time, exit exitMsg, cancelled bool, outputBytes int64) {
	event := AuditEvent{
		Time: time.Now(), RunID: e.currentRunID(), Session: filepath.Base(e.cfg.SessionDir),
		Command: truncateCommand(req.Cmd), Cwd: cwd, ExitCode: exit.Code, Signal: exit.Signal,
		TimedOut: exit.TimedOut, Cancelled: cancelled, Duration: time.Since(started),
		OutputBytes: outputBytes, Reaped: exit.Reaped, Error: exit.Error,
	}
	if e.cfg.Audit != nil {
		e.cfg.Audit.Record(event)
	}
	e.log.Info("execd: 命令结束", "session", event.Session, "run_id", event.RunID,
		"command", event.Command, "cwd", event.Cwd, "exit_code", event.ExitCode,
		"signal", event.Signal, "timed_out", event.TimedOut, "cancelled", event.Cancelled,
		"duration", event.Duration, "output_bytes", event.OutputBytes, "reaped", event.Reaped,
		"error", event.Error)
}
