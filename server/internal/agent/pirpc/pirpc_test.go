//go:build !windows

package pirpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess 是「测试二进制自举为假 Pi」的入口:测试重新执行自身
// (os.Args[0] -test.run=TestHelperProcess),用 PIRPC_HELPER 环境变量选择模式。
// 正常 `go test` 运行时环境变量为空,直接返回。
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("PIRPC_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		helperEcho()
	case "replay":
		helperReplay()
	case "bigline":
		helperBigline()
	case "badlines":
		helperBadlines()
	case "hang":
		helperHang()
	case "exit":
		helperExit()
	case "stderr":
		helperStderr()
	case "slow-consumer":
		helperSlowConsumer()
	case "abort-ok":
		helperAbortOK()
	case "child":
		helperChild()
	default:
		fmt.Fprintf(os.Stderr, "pirpc helper: 未知模式 %q\n", mode)
		os.Exit(2)
	}
	os.Exit(0)
}

// helperReadCommands 逐行读 stdin 命令并回调,EOF 时返回。
func helperReadCommands(respond func(cmd map[string]any)) {
	reader := bufio.NewReaderSize(os.Stdin, stdioBufferSize)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var cmd map[string]any
			if json.Unmarshal(bytes.TrimRight(line, "\r\n"), &cmd) == nil {
				respond(cmd)
			}
		}
		if err != nil {
			return
		}
	}
}

func helperWriteJSON(v map[string]any) {
	encoded, _ := json.Marshal(v)
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

// echo:对每条带 id 的命令回 success 应答。
func helperEcho() {
	helperReadCommands(func(cmd map[string]any) {
		id, _ := cmd["id"].(string)
		if id == "" {
			return
		}
		helperWriteJSON(map[string]any{"type": "response", "id": id, "success": true, "data": map[string]any{}})
	})
}

// replay:从 PIRPC_FIXTURE 指向的 jsonl 文件逐行输出事件,全部输出后退出。
func helperReplay() {
	data, err := os.ReadFile(os.Getenv("PIRPC_FIXTURE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// bigline:输出一行 3MB 的合法 JSON 事件。
func helperBigline() {
	helperWriteJSON(map[string]any{"type": "big_pad", "pad": strings.Repeat("A", 3*1024*1024)})
}

// badlines:穿插非法 JSON 行、空行、CRLF 行。
func helperBadlines() {
	_, _ = os.Stdout.WriteString("{\"type\":\"agent_start\"}\n")
	_, _ = os.Stdout.WriteString("this is not json\n")
	_, _ = os.Stdout.WriteString("\n")
	_, _ = os.Stdout.WriteString("{\"type\":\"turn_start\"}\r\n")
	_, _ = os.Stdout.WriteString("{\"type\":\"broken\":\n")
	_, _ = os.Stdout.WriteString("{\"type\":\"agent_settled\"}\n")
}

// hang:正常应答所有命令,唯独收到 abort 后既不应答也不退出。
func helperHang() {
	helperReadCommands(func(cmd map[string]any) {
		if cmd["type"] == "abort" {
			return
		}
		id, _ := cmd["id"].(string)
		if id == "" {
			return
		}
		helperWriteJSON(map[string]any{"type": "response", "id": id, "success": true})
	})
}

// exit:收到第一条命令后立即以非零码退出,不应答。
func helperExit() {
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadBytes('\n')
	fmt.Fprintln(os.Stderr, "helper stderr line 1")
	fmt.Fprintln(os.Stderr, "helper stderr line 2")
	os.Exit(3)
}

// stderr:把 PIRPC_STDERR_BODY 原样写入 stderr,然后等 stdin EOF 退出。
func helperStderr() {
	_, _ = os.Stderr.WriteString(os.Getenv("PIRPC_STDERR_BODY"))
	helperReadCommands(func(map[string]any) {})
}

// slow-consumer:一次性快速输出 2000 个事件后退出(测试端 range Events 等通道关闭,不能再等 stdin)。
func helperSlowConsumer() {
	var buf bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&buf, "{\"type\":\"tick\",\"seq\":%d}\n", i)
	}
	if _, err := os.Stdout.Write(buf.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// abort-ok:应答 abort 并随后发出 agent_settled 事件,进程继续存活。
func helperAbortOK() {
	helperReadCommands(func(cmd map[string]any) {
		id, _ := cmd["id"].(string)
		if id == "" {
			return
		}
		helperWriteJSON(map[string]any{"type": "response", "id": id, "success": true})
		if cmd["type"] == "abort" {
			_, _ = os.Stdout.WriteString("{\"type\":\"agent_settled\"}\n")
		}
	})
}

// child:fork 一个 sleep 子进程(留在同一进程组)并公布其 PID,然后等被杀。
func helperChild() {
	kid := exec.Command("sleep", "30")
	kid.Stdout = os.Stdout
	kid.Stderr = os.Stderr
	if err := kid.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	helperWriteJSON(map[string]any{"type": "child_pid", "pid": kid.Process.Pid})
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func helperConfig(t *testing.T, mode string) Config {
	t.Helper()
	return Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
		Dir:     t.TempDir(),
		// pirpc 不继承 os.Environ,环境由调用方(这里是测试)完整构造。
		Env:    append(os.Environ(), "PIRPC_HELPER="+mode),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestCallRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "echo"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	resp, err := p.Call(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var got struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("应答不是合法 JSON: %v", err)
	}
	if got.Type != "response" || got.ID != "req_1" || !got.Success {
		t.Fatalf("应答不符: %s", resp)
	}
}

func TestConcurrentCalls(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "echo"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := p.Call(ctx, map[string]any{"type": "get_state", "n": i})
			if err != nil {
				t.Errorf("Call %d: %v", i, err)
				return
			}
			var got struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(resp, &got); err != nil {
				t.Errorf("Call %d 应答非法: %v", i, err)
				return
			}
			if !strings.HasPrefix(got.ID, "req_") {
				t.Errorf("Call %d 应答 id = %q", i, got.ID)
			}
		}(i)
	}
	wg.Wait()
}

func TestReplayFixtureEvents(t *testing.T) {
	fixture, err := filepath.Abs("../testdata/pi-events/basic.jsonl")
	if err != nil {
		t.Fatalf("解析夹具路径: %v", err)
	}
	cfg := helperConfig(t, "replay")
	cfg.Env = append(cfg.Env, "PIRPC_FIXTURE="+fixture)
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	want := readFixtureLines(t, fixture)
	var got []string
	for ev := range p.Events() {
		got = append(got, string(ev))
	}
	if len(got) != len(want) {
		t.Fatalf("事件数 = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 行事件不符:\n got: %.200s\nwant: %.200s", i, got[i], want[i])
		}
	}

	info := p.Wait()
	if info.Code != 0 || info.Err != nil {
		t.Fatalf("退出信息 = %+v", info)
	}
	if info.BadLines != 0 {
		t.Errorf("BadLines = %d, want 0", info.BadLines)
	}
	if info.MaxLine == 0 {
		t.Errorf("MaxLine 未统计")
	}
	// Wait 返回时事件通道必须已关闭。
	if ev, ok := <-p.Events(); ok {
		t.Errorf("Wait 后通道应已关闭,却收到 %s", ev)
	}
}

func readFixtureLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读夹具: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("夹具 %s 含非法 JSON 行: %.120s", path, line)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestBigLineNotTruncated(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "bigline"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	var got struct {
		Type string `json:"type"`
		Pad  string `json:"pad"`
	}
	select {
	case ev := <-p.Events():
		if err := json.Unmarshal(ev, &got); err != nil {
			t.Fatalf("3MB 行不是合法 JSON: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("未收到大行事件")
	}
	if len(got.Pad) != 3*1024*1024 {
		t.Errorf("pad 长度 = %d, want %d", len(got.Pad), 3*1024*1024)
	}
	if info := p.Wait(); info.MaxLine < 3*1024*1024 {
		t.Errorf("MaxLine = %d, want >= %d", info.MaxLine, 3*1024*1024)
	}
}

func TestBadLinesSkippedAndCounted(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "badlines"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	var types []string
	for ev := range p.Events() {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(ev, &head); err != nil {
			t.Fatalf("事件非法: %v", err)
		}
		types = append(types, head.Type)
	}
	want := []string{"agent_start", "turn_start", "agent_settled"}
	if len(types) != len(want) {
		t.Fatalf("事件 = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("事件 = %v, want %v", types, want)
		}
	}
	if info := p.Wait(); info.BadLines != 2 {
		t.Errorf("BadLines = %d, want 2", info.BadLines)
	}
}

func TestAbortHangKillsProcessGroup(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "hang"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()
	if _, err := p.Call(ctx, map[string]any{"type": "get_state"}); err != nil {
		t.Fatalf("预热 Call: %v", err)
	}

	const grace = 400 * time.Millisecond
	start := time.Now()
	err = p.Abort(ctx, grace)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("hang 模式 Abort 应超时强制终止")
	}
	if elapsed < grace-50*time.Millisecond {
		t.Errorf("Abort 用时 %v,不应早于 grace %v", elapsed, grace)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Abort 用时 %v,远超 grace %v", elapsed, grace)
	}

	info := p.Wait()
	if info.Signal == "" || info.Code != -1 {
		t.Errorf("期望被信号杀死,退出信息 = %+v", info)
	}
	// 进程退出后,未决/后续 Call 立即以 ErrProcessExited 返回。
	if _, err := p.Call(ctx, map[string]any{"type": "get_state"}); !errors.Is(err, ErrProcessExited) {
		t.Errorf("退出后 Call 错误 = %v, want ErrProcessExited", err)
	}
}

func TestAbortGracefulSettles(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "abort-ok"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()
	if _, err := p.Call(ctx, map[string]any{"type": "get_state"}); err != nil {
		t.Fatalf("预热 Call: %v", err)
	}

	start := time.Now()
	if err := p.Abort(ctx, 5*time.Second); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Abort 用时 %v,应远小于 grace", elapsed)
	}
	// agent_settled 事件已到,排空后进程应仍存活可用。
	for range 1 {
		<-p.Events()
	}
	if _, err := p.Call(ctx, map[string]any{"type": "get_state"}); err != nil {
		t.Errorf("abort 收口后进程应存活: %v", err)
	}
}

func TestExitFailsPendingCalls(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "exit"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	_, err = p.Call(ctx, map[string]any{"type": "die"})
	if !errors.Is(err, ErrProcessExited) {
		t.Fatalf("未决 Call 错误 = %v, want ErrProcessExited", err)
	}

	info := p.Wait()
	if info.Code != 3 {
		t.Errorf("Code = %d, want 3", info.Code)
	}
	if info.Err == nil {
		t.Error("非零退出码应带 Err")
	}
	if !strings.Contains(info.Stderr, "helper stderr line 2") {
		t.Errorf("Stderr 尾部不含诊断: %q", info.Stderr)
	}
	if info.BadLines != 0 {
		t.Errorf("BadLines = %d, want 0", info.BadLines)
	}
}

func TestStderrTailKeepsLastBytes(t *testing.T) {
	cfg := helperConfig(t, "stderr")
	cfg.StderrLimit = 100
	cfg.Env = append(cfg.Env, "PIRPC_STDERR_BODY="+strings.Repeat("x", 5000)+"TAILEND")
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info := p.Wait()
	if len(info.Stderr) != 100 {
		t.Fatalf("Stderr 长度 = %d, want 100", len(info.Stderr))
	}
	if !strings.HasSuffix(info.Stderr, "TAILEND") {
		t.Fatalf("Stderr 尾部错位: %q", info.Stderr)
	}
}

func TestStderrLogLinesTruncated(t *testing.T) {
	var buf bytes.Buffer
	cfg := helperConfig(t, "stderr")
	cfg.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg.Env = append(cfg.Env, "PIRPC_STDERR_BODY="+strings.Repeat("y", 10000))
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	run, maxRun := 0, 0
	for _, c := range buf.Bytes() {
		if c == 'y' {
			run++
			if run > maxRun {
				maxRun = run
			}
		} else {
			run = 0
		}
	}
	if maxRun == 0 {
		t.Fatal("stderr 未写日志")
	}
	if maxRun > stderrLogLineMax {
		t.Errorf("单行日志长度 %d 超过上限 %d", maxRun, stderrLogLineMax)
	}
}

func TestSlowConsumerNoLossNoDeadlock(t *testing.T) {
	cfg := helperConfig(t, "slow-consumer")
	cfg.EventBuffer = 4 // 通道很小,验证背压不丢事件、不死锁
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	count, lastSeq := 0, -1
	for ev := range p.Events() {
		var tick struct {
			Seq int `json:"seq"`
		}
		if err := json.Unmarshal(ev, &tick); err != nil {
			t.Fatalf("事件非法: %v", err)
		}
		if tick.Seq != lastSeq+1 {
			t.Fatalf("顺序断裂: seq = %d, want %d", tick.Seq, lastSeq+1)
		}
		lastSeq = tick.Seq
		count++
		if count%16 == 0 {
			time.Sleep(2 * time.Millisecond) // 故意慢消费
		}
	}
	if count != 2000 {
		t.Errorf("收到事件 %d 个, want 2000", count)
	}
	if info := p.Wait(); info.Code != 0 {
		t.Errorf("退出信息 = %+v", info)
	}
}

func TestSendDoesNotWaitForResponse(t *testing.T) {
	ctx := context.Background()
	p, err := Start(ctx, helperConfig(t, "echo"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	if err := p.Send(map[string]any{"type": "extension_ui_response", "id": "ui-1", "confirmed": true}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// echo 假 Pi 会回一条 id 不匹配任何未决 Call 的 response,应路由到事件通道。
	time.Sleep(200 * time.Millisecond)
	select {
	case ev := <-p.Events():
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(ev, &head); err != nil {
			t.Fatalf("事件非法: %v", err)
		}
		if head.Type != "response" || head.ID != "ui-1" {
			t.Fatalf("旁路事件不符: %s", ev)
		}
	default:
		t.Fatal("未收到 ui-1 的旁路 response")
	}
	if _, err := p.Call(ctx, map[string]any{"type": "get_state"}); err != nil {
		t.Errorf("Send 之后连接应保持健康: %v", err)
	}
}

func TestCallContextCancel(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "hang"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	// hang 模式对 abort 永不应答,Call 只能靠 ctx 超时返回。
	_, err = p.Call(ctx, map[string]any{"type": "abort"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call 错误 = %v, want context.DeadlineExceeded", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "echo"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("第一次 Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("第二次 Close: %v", err)
	}
	if info := p.Wait(); info.Code != 0 {
		t.Errorf("退出信息 = %+v", info)
	}
}

func TestKillIdempotent(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "echo"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()
	if err := p.Kill(); err != nil {
		t.Fatalf("第一次 Kill: %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("第二次 Kill: %v", err)
	}
	info := p.Wait()
	if info.Signal == "" {
		t.Errorf("期望被信号杀死,退出信息 = %+v", info)
	}
}

func TestStartContextCancelKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, helperConfig(t, "echo"))
	if err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	defer cancel()
	defer p.Close()
	if _, err := p.Call(context.Background(), map[string]any{"type": "get_state"}); err != nil {
		t.Fatalf("预热 Call: %v", err)
	}

	cancel()
	info := p.Wait()
	if info.Signal == "" {
		t.Fatalf("ctx 取消应杀掉进程,退出信息 = %+v", info)
	}
	if err := syscall.Kill(p.PID(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("进程 %d 仍可发信号(未退出): %v", p.PID(), err)
	}
}

func TestKillCleansUpProcessGroup(t *testing.T) {
	p, err := Start(context.Background(), helperConfig(t, "child"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	var ann struct {
		PID int `json:"pid"`
	}
	select {
	case ev := <-p.Events():
		if err := json.Unmarshal(ev, &ann); err != nil {
			t.Fatalf("事件非法: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("未收到 child_pid 事件")
	}
	if ann.PID == 0 {
		t.Fatal("child_pid 事件缺少 pid")
	}

	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if info := p.Wait(); info.Signal == "" {
		t.Fatalf("期望被信号杀死,退出信息 = %+v", info)
	}
	// 同进程组的 sleep 子进程也应消失(可能短暂处于僵尸态,轮询等待)。
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(ann.PID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("子进程 %d 未随进程组终止: %v", ann.PID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSysProcAttrCredential:nil 时不设 Credential(与旧行为一致),非 nil 时
// 原样传递且 Setpgid 始终保留。非 root 环境可跑,不真的切 uid。
func TestSysProcAttrCredential(t *testing.T) {
	attr := sysProcAttr(nil)
	if !attr.Setpgid {
		t.Fatal("Setpgid 应始终开启")
	}
	if attr.Credential != nil {
		t.Fatalf("nil Credential 不应设到 SysProcAttr: %+v", attr.Credential)
	}
	cred := &syscall.Credential{Uid: 20001, Gid: 20001, Groups: []uint32{20001}}
	attr = sysProcAttr(cred)
	if !attr.Setpgid {
		t.Fatal("Setpgid 应始终开启")
	}
	if attr.Credential != cred {
		t.Fatalf("Credential 应原样传递: %+v", attr.Credential)
	}
}

// TestStartWithNilCredential:显式传 nil Credential 启动仍正常(隔离关闭路径)。
func TestStartWithNilCredential(t *testing.T) {
	cfg := helperConfig(t, "echo")
	cfg.Credential = nil
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()
	if _, err := p.Call(context.Background(), map[string]any{"type": "get_state"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
}
