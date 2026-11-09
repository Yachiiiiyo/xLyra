package session

import (
	"os"
	"path/filepath"
	"testing"
)

// statsFixture 是一个多消息会话:两轮对话 + error 步 + compaction + 之后的
// assistant 消息,覆盖统计的全部规则。
const statsFixture = `{"type":"session","version":3,"id":"statssess1","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-10-29T00:00:00.001Z","provider":"xlyra","modelId":"mock-1"}
{"type":"message","id":"a1","parentId":"m1","timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input":1000,"output":300,"cacheRead":800,"cacheWrite":100,"totalTokens":1200},"stopReason":"stop","timestamp":1793000000000}}
{"type":"message","id":"u1","parentId":"a1","timestamp":"2026-10-29T00:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"问"}],"timestamp":1793000001000}}
{"type":"message","id":"a2","parentId":"u1","timestamp":"2026-10-29T00:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"err"}],"usage":{"input":2000,"output":500,"cacheRead":1900,"cacheWrite":50,"totalTokens":2450},"stopReason":"error","timestamp":1793000002000}}
{"type":"compaction","id":"c1","parentId":"a2","timestamp":"2026-10-29T00:00:04.000Z","summary":"s","estimatedTokensAfter":500}
{"type":"message","id":"a3","parentId":"c1","timestamp":"2026-10-29T00:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input":600,"output":100,"cacheRead":500,"cacheWrite":0,"totalTokens":700},"stopReason":"stop","timestamp":1793000003000}}
`

// writePiContent 把自定义会话文件内容写进会话的 pi/ 目录。
func writePiContent(t *testing.T, store *Store, id, piSessionID, content string) string {
	t.Helper()
	name := "2026-10-29T00-00-00-000Z_" + piSessionID + ".jsonl"
	path := filepath.Join(store.PiDir(id), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入 Pi 会话文件: %v", err)
	}
	return name
}

func TestStatsMultiMessage(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiContent(t, store, meta.ID, "statssess1", statsFixture)
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "statssess1"
		// a1 的样本有效;a2 是 error 消息(其 timestamp 不在可测速集合);
		// a3 的样本 output 太小;999 不在会话文件里。
		m.GenSamples = []GenSample{
			{Timestamp: 1793000000000, OutputTokens: 100, GenMs: 1000},
			{Timestamp: 1793000002000, OutputTokens: 500, GenMs: 5000},
			{Timestamp: 1793000003000, OutputTokens: 10, GenMs: 1000},
			{Timestamp: 999, OutputTokens: 400, GenMs: 1000},
		}
	})
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Turns != 1 || stats.Steps != 3 {
		t.Errorf("turns/steps = %d/%d, want 1/3", stats.Turns, stats.Steps)
	}
	if stats.TotalTokens != 4350 {
		t.Errorf("total_tokens = %d, want 4350", stats.TotalTokens)
	}
	if stats.InputTokens != 6950 || stats.OutputTokens != 900 || stats.CacheReadTokens != 3200 || stats.CacheWriteTokens != 150 {
		t.Errorf("输入/输出/缓存 token = %d/%d/%d/%d, want 6950/900/3200/150",
			stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)
	}
	// cacheRead=3200,分母 input+cacheRead+cacheWrite=6950
	if stats.CacheHitRate == nil || *stats.CacheHitRate < 0.4604 || *stats.CacheHitRate > 0.4605 {
		t.Errorf("cache_hit_rate = %v, want ~0.4604", stats.CacheHitRate)
	}
	// 最后一个 compaction 之后有 a3:上下文取它的 totalTokens
	if stats.ContextTokens != 700 {
		t.Errorf("context_tokens = %d, want 700", stats.ContextTokens)
	}
	// 只有 a1 的样本算数:100 token / 1s = 100 tok/s
	if stats.TokensPerSecond == nil || *stats.TokensPerSecond != 100 {
		t.Errorf("tokens_per_second = %v, want 100", stats.TokensPerSecond)
	}
}

func TestStatsNoCache(t *testing.T) {
	// 所有消息 cacheRead 都为 0 时 cache_hit_rate 为 null。
	content := `{"type":"session","version":3,"id":"statssess2","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input":100,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":130},"stopReason":"stop","timestamp":1793000000000}}
`
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiContent(t, store, meta.ID, "statssess2", content)
	updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "statssess2" })
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.CacheHitRate != nil {
		t.Errorf("cache_hit_rate = %v, want nil", stats.CacheHitRate)
	}
	if stats.TokensPerSecond != nil {
		t.Errorf("tokens_per_second = %v, want nil(无样本)", stats.TokensPerSecond)
	}
}

func TestStatsCompactionContext(t *testing.T) {
	// compaction 之后没有 assistant 消息:上下文取 estimatedTokensAfter;
	// 没有估值字段时为 0。
	head := `{"type":"session","version":3,"id":"statssess3","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input":100,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":130},"stopReason":"stop","timestamp":1793000000000}}
`
	cases := []struct {
		name    string
		tail    string
		context int64
	}{
		{"compaction-之后无assistant", `{"type":"compaction","id":"c1","parentId":"a1","timestamp":"2026-10-29T00:00:02.000Z","summary":"s","estimatedTokensAfter":500}` + "\n", 500},
		{"compaction-无估值", `{"type":"compaction","id":"c1","parentId":"a1","timestamp":"2026-10-29T00:00:02.000Z","summary":"s"}` + "\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			meta := mustCreate(t, store)
			writePiContent(t, store, meta.ID, "statssess3", head+tc.tail)
			updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "statssess3" })
			stats, err := store.Stats(meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stats.ContextTokens != tc.context {
				t.Errorf("context_tokens = %d, want %d", stats.ContextTokens, tc.context)
			}
		})
	}
}

func TestStatsCurrentBranchOnly(t *testing.T) {
	// branch.jsonl 的当前分支是 b001→u201→a201→u203→a203(a202 在被抛弃的
	// 旧分支上);旧分支消息的 token 与样本都不计。
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		// a202(旧分支)的样本不参与,有效样本只有 a3 一条。
		m.GenSamples = []GenSample{
			{Timestamp: 1793242804000, OutputTokens: 500, GenMs: 5000}, // a202,旧分支
			{Timestamp: 1793242806000, OutputTokens: 40, GenMs: 400},   // a203
		}
	})
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Turns != 2 || stats.Steps != 2 || stats.TotalTokens != 40 {
		t.Errorf("turns/steps/total = %d/%d/%d, want 2/2/40", stats.Turns, stats.Steps, stats.TotalTokens)
	}
	if stats.InputTokens != 30 || stats.OutputTokens != 10 || stats.CacheReadTokens != 0 || stats.CacheWriteTokens != 0 {
		t.Errorf("当前分支输入/输出/缓存 token = %d/%d/%d/%d, want 30/10/0/0",
			stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)
	}
	if stats.ContextTokens != 25 {
		t.Errorf("context_tokens = %d, want 25(a203)", stats.ContextTokens)
	}
	if stats.TokensPerSecond == nil || *stats.TokensPerSecond != 100 {
		t.Errorf("tokens_per_second = %v, want 100(仅 a203 样本)", stats.TokensPerSecond)
	}
}

func TestStatsIgnoresAssistantWithoutUsage(t *testing.T) {
	content := `{"type":"session","version":3,"id":"statssess6","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"有用量"}],"usage":{"input":7,"output":11,"cacheRead":3,"cacheWrite":2,"totalTokens":23},"stopReason":"stop","timestamp":1793000000000}}
{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-29T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"无用量"}],"stopReason":"stop","timestamp":1793000001000}}
`
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiContent(t, store, meta.ID, "statssess6", content)
	updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "statssess6" })
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Steps != 2 || stats.TotalTokens != 23 {
		t.Fatalf("steps/total = %d/%d, want 2/23", stats.Steps, stats.TotalTokens)
	}
	if stats.InputTokens != 12 || stats.OutputTokens != 11 || stats.CacheReadTokens != 3 || stats.CacheWriteTokens != 2 {
		t.Fatalf("无 usage 消息未忽略:%d/%d/%d/%d, want 12/11/3/2",
			stats.InputTokens, stats.OutputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)
	}
}

func TestStatsCacheInvalidation(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	second := `{"type":"session","version":3,"id":"statssess4","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input":100,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":130},"stopReason":"stop","timestamp":1793000000000}}
{"type":"message","id":"u1","parentId":"a1","timestamp":"2026-10-29T00:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"再问"}],"timestamp":1793000001000}}
{"type":"message","id":"a2","parentId":"u1","timestamp":"2026-10-29T00:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"again"}],"usage":{"input":200,"output":60,"cacheRead":0,"cacheWrite":0,"totalTokens":260},"stopReason":"stop","timestamp":1793000002000}}
`
	writePiContent(t, store, meta.ID, "statssess4", second)
	updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "statssess4" })

	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Steps != 2 || stats.TotalTokens != 390 {
		t.Fatalf("首次统计 steps/total = %d/%d, want 2/390", stats.Steps, stats.TotalTokens)
	}
	// 文件更新(大小与修改时间变化)后缓存必须失效。
	writePiContent(t, store, meta.ID, "statssess4", second+
		`{"type":"message","id":"u2","parentId":"a2","timestamp":"2026-10-29T00:00:04.000Z","message":{"role":"user","content":[{"type":"text","text":"又问"}],"timestamp":1793000003000}}
{"type":"message","id":"a3","parentId":"u2","timestamp":"2026-10-29T00:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"third"}],"usage":{"input":300,"output":90,"cacheRead":0,"cacheWrite":0,"totalTokens":390},"stopReason":"stop","timestamp":1793000003000}}
`)
	stats, err = store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Steps != 3 || stats.TotalTokens != 780 || stats.ContextTokens != 390 {
		t.Errorf("更新后统计 steps/total/context = %d/%d/%d, want 3/780/390", stats.Steps, stats.TotalTokens, stats.ContextTokens)
	}
}

func TestStatsAbortedExcludedFromSpeed(t *testing.T) {
	// aborted 消息的 token 照常累计,但不进速度样本。
	content := `{"type":"session","version":3,"id":"statssess5","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"问一"}],"timestamp":1000}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"答一"}],"usage":{"input":50,"output":40,"cacheRead":0,"cacheWrite":0,"totalTokens":90},"stopReason":"stop","timestamp":2000}}
{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-10-29T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"问二"}],"timestamp":3000}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-10-29T00:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"半截"}],"usage":{"input":60,"output":500,"cacheRead":0,"cacheWrite":0,"totalTokens":560},"stopReason":"aborted","timestamp":4000}}
`
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiContent(t, store, meta.ID, "statssess5", content)
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "statssess5"
		m.GenSamples = []GenSample{
			{Timestamp: 2000, OutputTokens: 40, GenMs: 400},
			{Timestamp: 4000, OutputTokens: 500, GenMs: 5000},
		}
	})
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalTokens != 650 || stats.ContextTokens != 560 {
		t.Errorf("token 累计 = %d/%d, want 650/560", stats.TotalTokens, stats.ContextTokens)
	}
	if stats.TokensPerSecond == nil || *stats.TokensPerSecond != 100 {
		t.Errorf("tokens_per_second = %v, want 100(aborted 不计入)", stats.TokensPerSecond)
	}
}

func TestStatsEmptySession(t *testing.T) {
	// Pi 会话文件尚不存在:全部为 0/null。
	store := newTestStore(t)
	meta := mustCreate(t, store)
	stats, err := store.Stats(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Turns != 0 || stats.Steps != 0 || stats.TotalTokens != 0 || stats.ContextTokens != 0 {
		t.Errorf("空会话统计非零: %+v", stats)
	}
	if stats.CacheHitRate != nil || stats.TokensPerSecond != nil {
		t.Errorf("空会话速率非空: %+v", stats)
	}
}

func TestStatsNotFound(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Stats("0f0f0f0f-0f0f-0f0f-0f0f-0f0f0f0f0f0f"); err == nil {
		t.Fatal("不存在的会话应报错")
	}
}

func TestGenSpeedFilters(t *testing.T) {
	onBranch := map[int64]struct{}{1: {}, 2: {}, 3: {}, 4: {}}
	samples := []GenSample{
		{Timestamp: 1, OutputTokens: 100, GenMs: 1000}, // 有效
		{Timestamp: 2, OutputTokens: 19, GenMs: 1000},  // output < 20
		{Timestamp: 3, OutputTokens: 100, GenMs: 199},  // gen < 200
		{Timestamp: 4, OutputTokens: 100, GenMs: 500},  // 有效
		{Timestamp: 5, OutputTokens: 999, GenMs: 9999}, // 不在当前分支
	}
	speed := genSpeed(samples, onBranch)
	if speed == nil || *speed < 133.333 || *speed > 133.334 { // (100+100) / (1.0+0.5)
		t.Fatalf("speed = %v, want 133.333", speed)
	}
	if genSpeed(nil, onBranch) != nil {
		t.Fatal("无样本应为 nil")
	}
}

func TestAppendGenSampleCap(t *testing.T) {
	var meta Meta
	for i := 0; i < MaxGenSamples+100; i++ {
		meta.AppendGenSample(GenSample{Timestamp: int64(i), OutputTokens: int64(i + 1), GenMs: int64(i + 2)})
	}
	if len(meta.GenSamples) != MaxGenSamples {
		t.Fatalf("样本数 = %d, want %d", len(meta.GenSamples), MaxGenSamples)
	}
	// 丢的是最旧的:保留下的是最后 MaxGenSamples 条。
	wantFirst := int64(100)
	if meta.GenSamples[0].Timestamp != wantFirst {
		t.Errorf("最旧样本 timestamp = %d, want %d", meta.GenSamples[0].Timestamp, wantFirst)
	}
	wantLast := int64(MaxGenSamples + 99)
	if meta.GenSamples[MaxGenSamples-1].Timestamp != wantLast {
		t.Errorf("最新样本 timestamp = %d, want %d", meta.GenSamples[MaxGenSamples-1].Timestamp, wantLast)
	}
	// 截断后底层容量不受限地增长:追加继续可用。
	meta.AppendGenSample(GenSample{Timestamp: 1 << 40})
	if len(meta.GenSamples) != MaxGenSamples || meta.GenSamples[MaxGenSamples-1].Timestamp != 1<<40 {
		t.Fatal("截断后追加失败")
	}
	// 小集合不受影响。
	var small Meta
	small.AppendGenSample(GenSample{Timestamp: 7})
	if len(small.GenSamples) != 1 {
		t.Fatal("小集合追加失败")
	}
}
