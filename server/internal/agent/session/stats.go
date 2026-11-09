package session

import (
	"errors"
	"os"
	"sync"
)

// MaxGenSamples 是 meta 里保留的生成速度样本条数上限,超出丢弃最旧的。
const MaxGenSamples = 2000

// GenSample 是一条 assistant 消息的实测生成耗时样本:消息结束时刻减去该
// 消息第一个增量到达时刻(不含首字等待与网关排队/重试)。Timestamp 取 Pi
// 事件里的 message.timestamp(毫秒),与会话文件里同一条消息一一对应。
type GenSample struct {
	Timestamp    int64 `json:"timestamp"`
	OutputTokens int64 `json:"output_tokens"`
	GenMs        int64 `json:"gen_ms"`
}

// AppendGenSample 追加一条样本,只保留最近 MaxGenSamples 条。
func (m *Meta) AppendGenSample(sample GenSample) {
	m.GenSamples = append(m.GenSamples, sample)
	if len(m.GenSamples) > MaxGenSamples {
		m.GenSamples = append(m.GenSamples[:0], m.GenSamples[len(m.GenSamples)-MaxGenSamples:]...)
	}
}

// Stats 是会话统计(输入框下方那一行小字)。context_window 与 compact_ratio
// 来自能力配置,本包不关心来源,由调用方(handler)填充。
type Stats struct {
	Turns            int      `json:"turns"`              // 当前分支上的用户消息数
	Steps            int      `json:"steps"`              // 当前分支上的 assistant 消息数
	TotalTokens      int64    `json:"total_tokens"`       // 当前分支全部 assistant 消息的 totalTokens 之和
	InputTokens      int64    `json:"input_tokens"`       // 总输入:input+cacheRead+cacheWrite 之和
	OutputTokens     int64    `json:"output_tokens"`      // 当前分支全部 assistant 消息的 output 之和
	CacheReadTokens  int64    `json:"cache_read_tokens"`  // 当前分支全部 assistant 消息的 cacheRead 之和
	CacheWriteTokens int64    `json:"cache_write_tokens"` // 当前分支全部 assistant 消息的 cacheWrite 之和
	CacheHitRate     *float64 `json:"cache_hit_rate"`     // cacheRead / (input+cacheRead+cacheWrite),无缓存数据时为 null
	TokensPerSecond  *float64 `json:"tokens_per_second"`  // 实测生成速度,样本不足时为 null
	ContextTokens    int64    `json:"context_tokens"`     // 当前上下文大小
	ContextWindow    int      `json:"context_window"`     // 能力配置的上下文窗口
	CompactRatio     float64  `json:"compact_ratio"`      // 自动压缩水位线
}

// fileStats 是只从 Pi 会话文件得出的统计量(与 meta 无关),按文件大小 +
// 修改时间缓存;生成速度要等与 meta.GenSamples 按 timestamp 配对后再算。
type fileStats struct {
	turns       int
	steps       int
	totalTokens int64
	input       int64
	output      int64
	cacheRead   int64
	cacheWrite  int64
	context     int64
	// assistantTimestamps 是当前分支上可参与测速的 assistant 消息 timestamp
	// (毫秒);stopReason 为 error/aborted 的消息只累计 token,不进这里。
	assistantTimestamps map[int64]struct{}
}

type statsCacheEntry struct {
	modNano int64
	size    int64
	stats   fileStats
}

type statsCache struct {
	mu      sync.Mutex
	entries map[string]statsCacheEntry
}

var statsCaches sync.Map

// Stats 汇总会话统计:轮/步、token、缓存命中、上下文占用都只统计当前分支
// (Pi 会话文件里最后一个条目回溯出的链);tok/s 用 meta.GenSamples 与会话
// 文件里的 assistant 消息按 timestamp 配对。Pi 会话文件尚不存在时除
// context_window/compact_ratio 外全部为 0 或 null。
func (s *Store) Stats(id string) (Stats, error) {
	if !validID(id) {
		return Stats{}, ErrInvalidID
	}
	meta, err := s.Get(id)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{}
	file, err := PiSessionFile(s.PiDir(id), meta.PiSessionID)
	if err != nil {
		return stats, nil // 尚未跑过 Pi:没有统计,不算错误
	}
	info, err := os.Stat(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil // 定位后文件被删:同样按空会话处理
		}
		return Stats{}, err
	}
	file0, err := s.cachedFileStats(file, info)
	if err != nil {
		return Stats{}, err
	}
	stats.Turns = file0.turns
	stats.Steps = file0.steps
	stats.TotalTokens = file0.totalTokens
	stats.InputTokens = file0.input + file0.cacheRead + file0.cacheWrite
	stats.OutputTokens = file0.output
	stats.CacheReadTokens = file0.cacheRead
	stats.CacheWriteTokens = file0.cacheWrite
	stats.ContextTokens = file0.context
	if denom := file0.input + file0.cacheRead + file0.cacheWrite; file0.cacheRead > 0 && denom > 0 {
		rate := float64(file0.cacheRead) / float64(denom)
		stats.CacheHitRate = &rate
	}
	stats.TokensPerSecond = genSpeed(meta.GenSamples, file0.assistantTimestamps)
	return stats, nil
}

// cachedFileStats 按「会话文件大小 + 修改时间」缓存解析结果,避免每次请求
// 都重读整个会话文件。
func (s *Store) cachedFileStats(piPath string, info os.FileInfo) (fileStats, error) {
	value, _ := statsCaches.LoadOrStore(s.root, &statsCache{})
	cache := value.(*statsCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cached, ok := cache.entries[piPath]; ok &&
		cached.modNano == info.ModTime().UnixNano() && cached.size == info.Size() {
		return cached.stats, nil
	}
	entries, err := readPiEntries(piPath)
	if err != nil {
		return fileStats{}, err
	}
	stats := computeFileStats(activePath(entries))
	if cache.entries == nil {
		cache.entries = map[string]statsCacheEntry{}
	}
	cache.entries[piPath] = statsCacheEntry{
		modNano: info.ModTime().UnixNano(),
		size:    info.Size(),
		stats:   stats,
	}
	return stats, nil
}

// computeFileStats 按当前分支累计轮/步与 token;上下文取最后一个 compaction
// 之后的最近一条 assistant 消息的 totalTokens,compaction 之后还没有 assistant
// 消息时用它记录的 estimatedTokensAfter。
func computeFileStats(path []piEntry) fileStats {
	stats := fileStats{assistantTimestamps: map[int64]struct{}{}}
	for _, entry := range path {
		switch entry.Type {
		case "message":
			msg := entry.Message
			if msg == nil {
				continue
			}
			switch msg.Role {
			case "user":
				stats.turns++
			case "assistant":
				stats.steps++
				if msg.Usage != nil {
					stats.totalTokens += msg.Usage.TotalTokens
					stats.input += msg.Usage.Input
					stats.output += msg.Usage.Output
					stats.cacheRead += msg.Usage.CacheRead
					stats.cacheWrite += msg.Usage.CacheWrite
					stats.context = msg.Usage.TotalTokens
				}
				if msg.StopReason != "error" && msg.StopReason != "aborted" && msg.Timestamp != 0 {
					stats.assistantTimestamps[msg.Timestamp] = struct{}{}
				}
			}
		case "compaction":
			// 压缩后的上下文从估值起算;之后再有 assistant 消息会被覆盖。
			stats.context = 0
			if entry.EstimatedTokensAfter != nil {
				stats.context = *entry.EstimatedTokensAfter
			}
		}
	}
	return stats
}

// genSpeed 只取当前分支上有样本的 assistant 消息,忽略过短或过小的样本
// (output<20 或 gen<200ms),速度 = Σoutput ÷ (Σgen_ms/1000);没有样本为 null。
func genSpeed(samples []GenSample, onBranch map[int64]struct{}) *float64 {
	var tokens, genMs int64
	for _, sample := range samples {
		if _, ok := onBranch[sample.Timestamp]; !ok {
			continue
		}
		if sample.OutputTokens < 20 || sample.GenMs < 200 {
			continue
		}
		tokens += sample.OutputTokens
		genMs += sample.GenMs
	}
	if tokens <= 0 || genMs <= 0 {
		return nil
	}
	speed := float64(tokens) / (float64(genMs) / 1000)
	return &speed
}
