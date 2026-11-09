package runner

import (
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
)

func TestGenSampleTrackerMeasuresFromFirstDelta(t *testing.T) {
	base := time.Unix(100, 0)
	now := base
	tracker := genSampleTracker{now: func() time.Time { return now }}

	if _, ok := tracker.observe([]byte(`{"type":"message_start","message":{"role":"assistant","timestamp":12345,"stopReason":"pending"}}`)); ok {
		t.Fatal("message_start 不应产生样本")
	}
	// 首个增量之前的 3 秒是首字等待/网关排队,不能计入生成耗时。
	now = base.Add(3 * time.Second)
	if _, ok := tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"想"}}`)); ok {
		t.Fatal("message_update 不应产生样本")
	}
	now = now.Add(250 * time.Millisecond)
	tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"答"}}`))
	now = now.Add(500 * time.Millisecond)
	sample, ok := tracker.observe([]byte(`{"type":"message_end","message":{"role":"assistant","timestamp":12345,"stopReason":"stop","usage":{"output":42}}}`))
	if !ok {
		t.Fatal("正常 assistant 消息应产生样本")
	}
	if sample.Timestamp != 12345 || sample.OutputTokens != 42 || sample.GenMs != 750 {
		t.Fatalf("样本 = %+v, want timestamp=12345 output=42 gen_ms=750", sample)
	}
}

func TestGenSampleTrackerStartsAtDeltaAfterBlockStart(t *testing.T) {
	base := time.Unix(100, 0)
	now := base
	tracker := genSampleTracker{now: func() time.Time { return now }}

	tracker.observe([]byte(`{"type":"message_start","message":{"role":"assistant","timestamp":12345}}`))
	// 块开始先到,3 秒后才有 token:这 3 秒仍是首字等待,不能计入耗时。
	tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`))
	now = base.Add(3 * time.Second)
	tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"答"}}`))
	now = now.Add(750 * time.Millisecond)
	sample, ok := tracker.observe([]byte(`{"type":"message_end","message":{"role":"assistant","timestamp":12345,"stopReason":"stop","usage":{"output":42}}}`))
	if !ok {
		t.Fatal("有 delta 的正常 assistant 消息应产生样本")
	}
	if sample.GenMs != 750 {
		t.Fatalf("gen_ms = %d, want 750(从首个 delta 起算)", sample.GenMs)
	}
}

func TestGenSampleTrackerSkipsInvalidMessages(t *testing.T) {
	cases := []struct {
		name       string
		start      string
		end        string
		delta      bool
		blockStart bool
	}{
		{
			name:  "error",
			start: `{"type":"message_start","message":{"role":"assistant","timestamp":1}}`,
			end:   `{"type":"message_end","message":{"role":"assistant","timestamp":1,"stopReason":"error","usage":{"output":30}}}`,
			delta: true,
		},
		{
			name:  "aborted",
			start: `{"type":"message_start","message":{"role":"assistant","timestamp":2}}`,
			end:   `{"type":"message_end","message":{"role":"assistant","timestamp":2,"stopReason":"aborted","usage":{"output":30}}}`,
			delta: true,
		},
		{
			name:  "无输出token",
			start: `{"type":"message_start","message":{"role":"assistant","timestamp":3}}`,
			end:   `{"type":"message_end","message":{"role":"assistant","timestamp":3,"stopReason":"stop","usage":{"output":0}}}`,
			delta: true,
		},
		{
			name:  "没有增量",
			start: `{"type":"message_start","message":{"role":"assistant","timestamp":4}}`,
			end:   `{"type":"message_end","message":{"role":"assistant","timestamp":4,"stopReason":"stop","usage":{"output":30}}}`,
		},
		{
			name:       "只有块开始",
			start:      `{"type":"message_start","message":{"role":"assistant","timestamp":6}}`,
			end:        `{"type":"message_end","message":{"role":"assistant","timestamp":6,"stopReason":"stop","usage":{"output":30}}}`,
			blockStart: true,
		},
		{
			name:  "缺timestamp",
			start: `{"type":"message_start","message":{"role":"assistant"}}`,
			end:   `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","usage":{"output":30}}}`,
			delta: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			tracker := genSampleTracker{now: func() time.Time { return now }}
			tracker.observe([]byte(tc.start))
			if tc.blockStart {
				now = now.Add(time.Second)
				tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`))
			}
			if tc.delta {
				now = now.Add(time.Second)
				tracker.observe([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"x"}}`))
			}
			now = now.Add(time.Second)
			if sample, ok := tracker.observe([]byte(tc.end)); ok {
				t.Fatalf("不应产生样本: %+v", sample)
			}
		})
	}
}

func TestGenSamplesRecordedFromFakePi(t *testing.T) {
	env := newTestEnv(t, "basic", withDelay(30))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.GenSamples) != 1 {
		t.Fatalf("生成样本数 = %d, want 1", len(meta.GenSamples))
	}
	sample := meta.GenSamples[0]
	if sample.Timestamp != 1793258300004 || sample.OutputTokens != 4 {
		t.Fatalf("样本标识/输出 = %+v", sample)
	}
	if sample.GenMs <= 0 || sample.GenMs > 5000 {
		t.Fatalf("gen_ms = %d, 应在合理范围内", sample.GenMs)
	}
}

// TestGenSampleTimestampJoinsSessionFile 验证事件里的 message.timestamp 与
// 会话文件里同一条 assistant 消息的 timestamp 一一对应:夹具事件的 timestamp
// 取 session/testdata/fork.jsonl 里 a101 的值,统计能据此把样本配到该消息上。
func TestGenSampleTimestampJoinsSessionFile(t *testing.T) {
	env := newTestEnv(t, "basic", withLocalFixture("gen-sample.jsonl"), withDelay(250))
	result := sendBasic(t, env)
	collectRun(t, env.runs, result.SessionID).wait(t)
	meta, err := env.store.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.GenSamples) != 1 {
		t.Fatalf("生成样本数 = %d, want 1", len(meta.GenSamples))
	}
	sample := meta.GenSamples[0]
	if sample.Timestamp != 1793248211000 || sample.OutputTokens != 120 {
		t.Fatalf("样本标识/输出 = %+v", sample)
	}
	if sample.GenMs < 200 {
		t.Fatalf("gen_ms = %d, 首个增量与消息结束之间应有可控延迟", sample.GenMs)
	}
	stats, err := env.store.Stats(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// fork.jsonl 当前分支上有两条 assistant 消息;速度非 null 才说明按
	// timestamp 配上了 a101(配不上会因过滤后无样本而为 null)。
	if stats.Steps != 2 {
		t.Fatalf("steps = %d, want 2(fork.jsonl 当前分支)", stats.Steps)
	}
	if stats.TokensPerSecond == nil || *stats.TokensPerSecond <= 0 {
		t.Fatalf("tokens_per_second = %v, 样本未与会话文件配对", stats.TokensPerSecond)
	}
}

// TestRecordGenSamplesKeepsLatest 验证 runner 写回路径上的 2000 条上限:
// 超出丢最旧,保留最后 MaxGenSamples 条。
func TestRecordGenSamplesKeepsLatest(t *testing.T) {
	env := newTestEnv(t, "basic")
	meta, err := env.store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]session.GenSample, session.MaxGenSamples+100)
	for i := range samples {
		samples[i] = session.GenSample{Timestamp: int64(i), OutputTokens: 30, GenMs: 300}
	}
	env.runner.recordGenSamples(&activeRun{sessionID: meta.ID, runID: "run-cap"}, samples)
	stored, err := env.store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.GenSamples) != session.MaxGenSamples {
		t.Fatalf("样本数 = %d, want %d", len(stored.GenSamples), session.MaxGenSamples)
	}
	if stored.GenSamples[0].Timestamp != 100 {
		t.Fatalf("最旧样本 timestamp = %d, want 100", stored.GenSamples[0].Timestamp)
	}
	if last := stored.GenSamples[session.MaxGenSamples-1].Timestamp; last != int64(session.MaxGenSamples+99) {
		t.Fatalf("最新样本 timestamp = %d, want %d", last, session.MaxGenSamples+99)
	}
}
