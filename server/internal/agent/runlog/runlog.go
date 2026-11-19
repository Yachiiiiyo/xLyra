// Package runlog 为每个 Agent run 维护一份内存事件缓冲,并把缓冲/实时事件
// 序列化为 /agent 页消费的 SSE 流(event: rollout + {type,payload,ordinal} 信封)。
// 前端不带 after 游标、重连时由服务端从 run 开头重放(前端自裁剪重复),因此
// Subscribe 一律从第 0 条重放;Pi 会话文件与 transcript 才是持久数据,本包不落盘。
package runlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"xlyra/server/internal/agent/translate"
)

const (
	defaultRetention = 60 * time.Second
	defaultMaxEvents = 50000
	defaultKeepalive = 15 * time.Second
	// 订阅输出通道与截断后直发通道的容量:只影响消费端突发下的缓冲,
	// 不改变语义(慢订阅者靠游标读共享切片追平,不占队列)。
	outBuffer  = 64
	liveBuffer = 512
)

// ErrRunActive 表示同一会话已有未结束的 run,不能 Begin 新的。
var ErrRunActive = errors.New("runlog: 会话已有进行中的 run")

// Event 是带序号的转译事件,Ordinal 从 1 起单调递增(截断后仍继续计数)。
type Event struct {
	Ordinal int64
	translate.Event
}

// Observer 在事件成功追加后被调用。调用发生在持有 Run.mu 期间,
// 实现必须不阻塞(不得等待 IO、锁或其它 goroutine),否则会卡住事件泵。
type Observer func(sessionID, runID string, startedAt time.Time, ev translate.Event)

// Registry 按会话管理 run。过期清理采用惰性策略:只在 Begin/Current 访问时
// 顺带清扫超过保留期的已结束 run,不起后台 goroutine。
type Registry struct {
	mu        sync.Mutex
	runs      map[string]*Run
	retention time.Duration
	maxEvents int
	clock     func() time.Time
	obs       Observer
}

// Option 配置 Registry。
type Option func(*Registry)

// WithRetention 设置 run 结束后保留供迟到订阅者重放的时长(默认 60s)。
func WithRetention(d time.Duration) Option { return func(r *Registry) { r.retention = d } }

// WithMaxEvents 设置单 run 缓冲上限(默认 50000),超过后不再缓存只广播。
func WithMaxEvents(n int) Option { return func(r *Registry) { r.maxEvents = n } }

// WithClock 注入时钟,便于测试控制保留期。
func WithClock(clock func() time.Time) Option { return func(r *Registry) { r.clock = clock } }

// WithObserver 设置可选观察者。没有观察者时事件广播与现在完全一致。
// 观察者在持有 Run.mu 时被调用,必须不阻塞。
func WithObserver(obs Observer) Option { return func(r *Registry) { r.obs = obs } }

// NewRegistry 建立空的 run 注册表。
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{
		runs:      map[string]*Run{},
		retention: defaultRetention,
		maxEvents: defaultMaxEvents,
		clock:     time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.retention <= 0 {
		r.retention = defaultRetention
	}
	if r.maxEvents <= 0 {
		r.maxEvents = defaultMaxEvents
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	return r
}

// Begin 为会话开始一个 run。上一 run 已结束(无论是否仍在保留期)则替换;
// 仍有未结束 run 时返回 ErrRunActive。
func (r *Registry) Begin(sessionID, runID string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	if cur := r.runs[sessionID]; cur != nil && !cur.isFinished() {
		return nil, ErrRunActive
	}
	now := r.clock()
	run := &Run{
		id:        runID,
		sessionID: sessionID,
		startedAt: now,
		maxEvents: r.maxEvents,
		clock:     r.clock,
		obs:       r.obs,
		buf:       make([]Event, 0, 64),
		subs:      map[*subscriber]struct{}{},
		next:      1, // Ordinal 从 1 起
		done:      make(chan struct{}),
	}
	r.runs[sessionID] = run
	return run, nil
}

// Current 返回会话当前 run(进行中,或已结束但仍在保留期内),没有则 nil。
func (r *Registry) Current(sessionID string) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	return r.runs[sessionID]
}

// Running 报告会话是否有未结束的 run(供会话列表的 running 标记)。
func (r *Registry) Running(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[sessionID]
	return run != nil && !run.isFinished()
}

// sweepLocked 删除已结束且超过保留期的 run。锁序恒为 Registry.mu → Run.mu,
// 与 Run 自身方法(只拿 Run.mu)不会反向死锁。
func (r *Registry) sweepLocked() {
	now := r.clock()
	for sessionID, run := range r.runs {
		if run.expired(now, r.retention) {
			delete(r.runs, sessionID)
		}
	}
}

// subscriber 是一个在线订阅者的唤醒通道。notify 只作信号(容量 1,可合并);
// live 仅在缓冲截断后使用:事件进不了共享切片,只能直发给在线订阅者。
type subscriber struct {
	notify chan struct{}
	live   chan Event
}

// Run 是一次运行的事件缓冲。所有方法并发安全;buf 只追加不改写,
// 订阅者按各自游标读取共享切片,慢订阅者不丢事件也不拖慢 Publish。
type Run struct {
	id        string
	sessionID string
	startedAt time.Time
	maxEvents int
	clock     func() time.Time
	obs       Observer

	mu         sync.Mutex
	buf        []Event // 重放缓冲,达 maxEvents 后冻结
	next       int64   // 下一个事件的 Ordinal(截断后仍递增)
	subs       map[*subscriber]struct{}
	truncated  bool
	finished   bool
	finishedAt time.Time
	done       chan struct{} // Finish 时关闭
}

// ID 返回 run 标识。
func (run *Run) ID() string { return run.id }

// Publish 追加事件并通知订阅者。已 Finish 后返回 false;缓冲达上限后不再
// 追加(置 Truncated)但仍直发给在线订阅者——直发通道满(订阅者停读很久)时
// 丢弃该事件,避免单个卡死的 SSE 消费者阻塞整个 run 的事件泵;前端在流异常
// 结束/终态时会用 transcript 对账,兜住这种极端场景。
func (run *Run) Publish(ev translate.Event) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.finished {
		return false
	}
	entry := Event{Ordinal: run.next, Event: ev}
	run.next++
	if len(run.buf) < run.maxEvents {
		run.buf = append(run.buf, entry)
		// 非阻塞信号:已挂起的信号会唤醒订阅者读到全部新事件,可安全合并。
		for sub := range run.subs {
			select {
			case sub.notify <- struct{}{}:
			default:
			}
		}
		run.observe(ev)
		return true
	}
	run.truncated = true
	// 截断后的直发必须持锁进行:与 Finish(同锁置 finished)互斥,保证订阅者
	// 看到 finished 前所有直发事件都已入队,排空 live 即得完整序列。
	for sub := range run.subs {
		select {
		case sub.live <- entry:
		default:
		}
	}
	run.observe(ev)
	return true
}

// observe 在持有 Run.mu 时调用观察者。panic 不得影响事件泵。
func (run *Run) observe(ev translate.Event) {
	if run.obs == nil {
		return
	}
	defer func() { _ = recover() }()
	run.obs(run.sessionID, run.id, run.startedAt, ev)
}

// Finish 结束 run(幂等)。订阅者读完已缓冲事件后其通道关闭。
func (run *Run) Finish() {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.finished {
		return
	}
	run.finished = true
	run.finishedAt = run.clock()
	close(run.done)
}

// Done 在 run 结束时关闭。
func (run *Run) Done() <-chan struct{} { return run.done }

// Truncated 报告缓冲是否已达上限而被截断。
func (run *Run) Truncated() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.truncated
}

// Subscribe 从第 0 条开始重放已缓冲事件,再跟随实时事件;run 结束且读完后
// 通道关闭。ctx 取消时通道关闭、goroutine 退出。
func (run *Run) Subscribe(ctx context.Context) <-chan Event {
	sub := &subscriber{
		notify: make(chan struct{}, 1),
		live:   make(chan Event, liveBuffer),
	}
	run.mu.Lock()
	run.subs[sub] = struct{}{}
	run.mu.Unlock()

	out := make(chan Event, outBuffer)
	go func() {
		defer func() {
			run.mu.Lock()
			delete(run.subs, sub)
			run.mu.Unlock()
			close(out)
		}()
		var cursor int // buf 中已投递的条数
		for {
			run.mu.Lock()
			n := len(run.buf)
			finished := run.finished
			// buf 只追加:即使随后扩容重分配,旧底层数组内容不变,解锁后读安全。
			var fresh []Event
			if cursor < n {
				fresh = run.buf[cursor:n]
			}
			run.mu.Unlock()
			for _, ev := range fresh {
				if !send(ctx, out, ev) {
					return
				}
			}
			cursor = n
			if finished {
				// Publish 与 Finish 同锁互斥:此刻不会再有新事件入队,排空 live 即收尾。
				for {
					select {
					case ev := <-sub.live:
						if !send(ctx, out, ev) {
							return
						}
						continue
					default:
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-run.done:
				// 转下一轮循环走 finished 收尾
			case <-sub.notify:
			case ev := <-sub.live:
				// 截断后 buf 已冻结,live 即唯一事件源,不会与重放乱序
				if !send(ctx, out, ev) {
					return
				}
			}
		}
	}()
	return out
}

// send 投递单条事件,ctx 取消时返回 false 让调用方退出。
func send(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (run *Run) isFinished() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.finished
}

// expired 需在持有 Registry.mu 时调用(锁序 Registry.mu → Run.mu)。
func (run *Run) expired(now time.Time, retention time.Duration) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.finished && now.Sub(run.finishedAt) > retention
}

// sseEnvelope 是 data 行的 JSON 信封;字段顺序即序列化顺序(type,payload,ordinal)。
type sseEnvelope struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
	Ordinal int64          `json:"ordinal"`
}

// WriteSSE 把订阅输出为 SSE:设置流式响应头,每条事件写
// "event: rollout\ndata: {…}\n\n" 并 Flush;每 keepalive 写一行 ": ping\n\n"
// 保活(<=0 时取 15s)。通道关闭返回 nil,ctx 取消返回其错误。
func WriteSSE(ctx context.Context, w http.ResponseWriter, events <-chan Event, keepalive time.Duration) error {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	flusher, canFlush := w.(http.Flusher)
	flush := func() {
		if canFlush {
			flusher.Flush()
		}
	}
	if keepalive <= 0 {
		keepalive = defaultKeepalive
	}
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			payload := ev.Data
			if payload == nil {
				payload = map[string]any{} // 永不为 null
			}
			encoded, err := json.Marshal(sseEnvelope{Type: ev.Type, Payload: payload, Ordinal: ev.Ordinal})
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: rollout\ndata: %s\n\n", encoded); err != nil {
				return err
			}
			flush()
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return err
			}
			flush()
		}
	}
}
