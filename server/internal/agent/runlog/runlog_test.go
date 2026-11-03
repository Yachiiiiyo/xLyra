package runlog

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/agent/translate"
)

// fakeClock 是可手动推进的注入时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// mustReceive 带超时读一条事件,超时即失败。
func mustReceive(t *testing.T, ch <-chan Event, what string) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("%s: 通道意外关闭", what)
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: 5s 内未收到事件", what)
		return Event{}
	}
}

// mustClose 断言通道在读尽存量事件后关闭。
func mustClose(t *testing.T, ch <-chan Event, what string) {
	t.Helper()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: 5s 内通道未关闭", what)
		}
	}
}

func typedEvent(typ string) translate.Event {
	return translate.Event{Type: typ, Data: map[string]any{"kind": typ}}
}

// TestBasicPublishSubscribe:Publish 顺序与 Ordinal 连续;Finish 后通道关闭;
// Finish 后 Publish 返回 false。
func TestBasicPublishSubscribe(t *testing.T) {
	registry := NewRegistry()
	run, err := registry.Begin("s1", "r1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if run.ID() != "r1" {
		t.Fatalf("ID() = %q, want r1", run.ID())
	}
	if !registry.Running("s1") {
		t.Fatal("Begin 后 Running 应为 true")
	}
	for i := 0; i < 3; i++ {
		if !run.Publish(typedEvent(fmt.Sprintf("e%d", i+1))) {
			t.Fatalf("Publish #%d 返回 false", i+1)
		}
	}
	events := run.Subscribe(context.Background())
	for i := 0; i < 3; i++ {
		ev := mustReceive(t, events, "重放事件")
		if want := int64(i + 1); ev.Ordinal != want {
			t.Fatalf("Ordinal = %d, want %d", ev.Ordinal, want)
		}
		if want := fmt.Sprintf("e%d", i+1); ev.Type != want {
			t.Fatalf("Type = %q, want %q", ev.Type, want)
		}
	}
	run.Finish()
	mustClose(t, events, "Finish 后订阅通道")
	if run.Publish(typedEvent("late")) {
		t.Fatal("Finish 后 Publish 应返回 false")
	}
	if registry.Running("s1") {
		t.Fatal("Finish 后 Running 应为 false")
	}
}

// TestLateSubscribe:run 中途订阅先收重放再收实时;结束后(保留期内)订阅收全部后关闭。
func TestLateSubscribe(t *testing.T) {
	registry := NewRegistry()
	run, _ := registry.Begin("s1", "r1")
	run.Publish(typedEvent("a"))
	run.Publish(typedEvent("b"))

	mid := run.Subscribe(context.Background())
	run.Publish(typedEvent("c"))
	for i, want := range []string{"a", "b", "c"} {
		ev := mustReceive(t, mid, "中途订阅")
		if ev.Ordinal != int64(i+1) || ev.Type != want {
			t.Fatalf("中途订阅第 %d 条 = (%d,%s), want (%d,%s)", i+1, ev.Ordinal, ev.Type, i+1, want)
		}
	}
	run.Finish()
	mustClose(t, mid, "中途订阅通道")

	if registry.Current("s1") != run {
		t.Fatal("保留期内 Current 应返回该 run")
	}
	late := run.Subscribe(context.Background())
	for i, want := range []string{"a", "b", "c"} {
		ev := mustReceive(t, late, "结束后订阅")
		if ev.Ordinal != int64(i+1) || ev.Type != want {
			t.Fatalf("结束后订阅第 %d 条 = (%d,%s), want (%d,%s)", i+1, ev.Ordinal, ev.Type, i+1, want)
		}
	}
	mustClose(t, late, "结束后订阅通道")
}

// TestConcurrentSubscribers:并发订阅(含故意慢的)最终序列一致且完整。
func TestConcurrentSubscribers(t *testing.T) {
	const total = 200
	registry := NewRegistry()
	run, _ := registry.Begin("s1", "r1")

	readAll := func(name string, slow bool, got *[]Event, done chan struct{}) {
		defer close(done)
		ch := run.Subscribe(context.Background())
		for ev := range ch {
			*got = append(*got, ev)
			if slow {
				time.Sleep(time.Millisecond) // 故意慢:靠游标追平,不丢事件
			}
		}
	}

	fast1, fast2, slow := []Event{}, []Event{}, []Event{}
	done1, done2, done3 := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go readAll("fast1", false, &fast1, done1)
	go readAll("fast2", false, &fast2, done2)
	go readAll("slow", true, &slow, done3)

	for i := 0; i < total; i++ {
		if !run.Publish(typedEvent(fmt.Sprintf("e%d", i))) {
			t.Fatalf("Publish #%d 返回 false", i)
		}
	}
	run.Finish()
	<-done1
	<-done2
	<-done3

	for name, got := range map[string][]Event{"fast1": fast1, "fast2": fast2, "slow": slow} {
		if len(got) != total {
			t.Fatalf("%s 收到 %d 条, want %d", name, len(got), total)
		}
		for i, ev := range got {
			if ev.Ordinal != int64(i+1) {
				t.Fatalf("%s 第 %d 条 Ordinal = %d", name, i+1, ev.Ordinal)
			}
			if want := fmt.Sprintf("e%d", i); ev.Type != want {
				t.Fatalf("%s 第 %d 条 Type = %q, want %q", name, i+1, ev.Type, want)
			}
		}
	}
}

// TestRegistryLifecycle:ErrRunActive;保留期内替换;过期后 Current 为 nil。
func TestRegistryLifecycle(t *testing.T) {
	clock := newFakeClock()
	registry := NewRegistry(WithClock(clock.Now), WithRetention(time.Minute))

	if run := registry.Current("s1"); run != nil {
		t.Fatal("无 run 时 Current 应为 nil")
	}
	if registry.Running("s1") {
		t.Fatal("无 run 时 Running 应为 false")
	}
	first, err := registry.Begin("s1", "r1")
	if err != nil {
		t.Fatalf("Begin r1: %v", err)
	}
	if _, err := registry.Begin("s1", "r2"); !errors.Is(err, ErrRunActive) {
		t.Fatalf("重复 Begin 错误 = %v, want ErrRunActive", err)
	}

	first.Finish()
	if registry.Running("s1") {
		t.Fatal("Finish 后 Running 应为 false")
	}
	if run := registry.Current("s1"); run != first {
		t.Fatal("保留期内 Current 应返回已结束的 run")
	}

	second, err := registry.Begin("s1", "r2") // 已结束则替换,不受 ErrRunActive 影响
	if err != nil {
		t.Fatalf("Begin r2: %v", err)
	}
	second.Finish()

	clock.Advance(time.Minute) // 恰好等于保留期还未过期
	if run := registry.Current("s1"); run == nil {
		t.Fatal("保留期内 Current 不应为 nil")
	}
	clock.Advance(time.Second)
	if run := registry.Current("s1"); run != nil {
		t.Fatalf("超过保留期后 Current = %v, want nil", run)
	}
	if registry.Running("s1") {
		t.Fatal("过期后 Running 应为 false")
	}
}

// TestTruncation:超上限后 Truncated 为 true,缓冲冻结,在线订阅者仍收到全部事件。
func TestTruncation(t *testing.T) {
	const max = 5
	registry := NewRegistry(WithMaxEvents(max))
	run, _ := registry.Begin("s1", "r1")
	events := run.Subscribe(context.Background())

	for i := 0; i < max; i++ {
		run.Publish(typedEvent(fmt.Sprintf("e%d", i+1)))
	}
	if run.Truncated() {
		t.Fatal("恰好达上限时 Truncated 应为 false")
	}
	for i := 0; i < 3; i++ {
		run.Publish(typedEvent(fmt.Sprintf("e%d", max+i+1)))
	}
	if !run.Truncated() {
		t.Fatal("超上限后 Truncated 应为 true")
	}
	run.Finish()

	const want = max + 3
	for i := 0; i < want; i++ {
		ev := mustReceive(t, events, "在线订阅")
		if ev.Ordinal != int64(i+1) {
			t.Fatalf("第 %d 条 Ordinal = %d", i+1, ev.Ordinal)
		}
		if wantType := fmt.Sprintf("e%d", i+1); ev.Type != wantType {
			t.Fatalf("第 %d 条 Type = %q, want %q", i+1, ev.Type, wantType)
		}
	}
	mustClose(t, events, "在线订阅通道")

	late := run.Subscribe(context.Background()) // 迟到订阅只能重放被冻结的缓冲
	for i := 0; i < max; i++ {
		ev := mustReceive(t, late, "迟到订阅")
		if wantType := fmt.Sprintf("e%d", i+1); ev.Type != wantType {
			t.Fatalf("迟到订阅第 %d 条 Type = %q, want %q", i+1, ev.Type, wantType)
		}
	}
	mustClose(t, late, "迟到订阅通道")
}

// TestSubscribeCancel:ctx 取消后通道关闭、goroutine 退出,无泄漏。
func TestSubscribeCancel(t *testing.T) {
	cases := []struct {
		name    string
		publish int // 取消前发布的事件数(0=订阅者闲置在等待)
	}{
		{name: "闲置等待", publish: 0},
		{name: "积压未读", publish: outBuffer + 100}, // 超出 out 容量,goroutine 阻塞在投递
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := runtime.NumGoroutine()
			registry := NewRegistry()
			run, _ := registry.Begin("s1", "r1")
			ctx, cancel := context.WithCancel(context.Background())
			events := run.Subscribe(ctx)
			for i := 0; i < tc.publish; i++ {
				run.Publish(typedEvent("e"))
			}
			cancel()

			mustClose(t, events, "取消后的订阅通道")
			// goroutine 在 close(out) 前已退出主循环,轮询等计数回落。
			deadline := time.Now().Add(5 * time.Second)
			for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if after := runtime.NumGoroutine(); after > before {
				t.Fatalf("goroutine 数 %d > 基线 %d,疑似泄漏", after, before)
			}
			run.Finish()
		})
	}
}

// TestWriteSSEFormat:逐字符断言 SSE 输出(headers、信封、空载荷 {})。
func TestWriteSSEFormat(t *testing.T) {
	events := make(chan Event, 3)
	events <- Event{Ordinal: 1, Event: translate.Event{Type: "text_delta", Data: map[string]any{"delta": "你好"}}}
	events <- Event{Ordinal: 2, Event: translate.Event{Type: "done", Data: nil}} // nil 载荷应序列化为 {}
	close(events)

	recorder := httptest.NewRecorder()
	if err := WriteSSE(context.Background(), recorder, events, time.Hour); err != nil {
		t.Fatalf("WriteSSE: %v", err)
	}
	want := "event: rollout\ndata: {\"type\":\"text_delta\",\"payload\":{\"delta\":\"你好\"},\"ordinal\":1}\n\n" +
		"event: rollout\ndata: {\"type\":\"done\",\"payload\":{},\"ordinal\":2}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("SSE 输出不符:\ngot  %q\nwant %q", got, want)
	}
	for key, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := recorder.Header().Get(key); got != want {
			t.Fatalf("响应头 %s = %q, want %q", key, got, want)
		}
	}
	if !recorder.Flushed {
		t.Fatal("未调用 Flush")
	}
}

// TestWriteSSEKeepalive:空闲时按 keepalive 间隔输出 ": ping",ctx 取消返回其错误。
func TestWriteSSEKeepalive(t *testing.T) {
	events := make(chan Event) // 不发事件,保持流空闲
	ctx, cancel := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteSSE(ctx, recorder, events, 10*time.Millisecond)
	}()
	time.Sleep(100 * time.Millisecond) // 等 ≥ 数个 keepalive 间隔;期间不碰 recorder,避免数据竞争
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx 取消后错误 = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 WriteSSE 未返回")
	}
	if got := recorder.Body.String(); !strings.Contains(got, ": ping\n\n") {
		t.Fatalf("输出未包含 keepalive 注释行: %q", got)
	}
}

// TestWriteSSEEmptyStream:通道立即关闭时返回 nil 且无事件输出。
func TestWriteSSEEmptyStream(t *testing.T) {
	events := make(chan Event)
	close(events)
	recorder := httptest.NewRecorder()
	if err := WriteSSE(context.Background(), recorder, events, time.Hour); err != nil {
		t.Fatalf("WriteSSE: %v", err)
	}
	if got := recorder.Body.String(); got != "" {
		t.Fatalf("空流应无输出, got %q", got)
	}
}
