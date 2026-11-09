package agent

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/agent/session"
)

// handlerStatsResponse 对应统计接口的 data 字段;指针字段用于区分 0 与 null。
type handlerStatsResponse struct {
	Data struct {
		Turns            int      `json:"turns"`
		Steps            int      `json:"steps"`
		TotalTokens      int64    `json:"total_tokens"`
		InputTokens      int64    `json:"input_tokens"`
		OutputTokens     int64    `json:"output_tokens"`
		CacheReadTokens  int64    `json:"cache_read_tokens"`
		CacheWriteTokens int64    `json:"cache_write_tokens"`
		CacheHitRate     *float64 `json:"cache_hit_rate"`
		TokensPerSecond  *float64 `json:"tokens_per_second"`
		ContextTokens    int64    `json:"context_tokens"`
		ContextWindow    int      `json:"context_window"`
		CompactRatio     float64  `json:"compact_ratio"`
	} `json:"data"`
}

func TestSessionStatsNotFound(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{})
	rec := doJSONAgent(t, handler, http.MethodGet,
		"/api/v1/agent/sessions/0f0f0f0f-0f0f-0f0f-0f0f-0f0f0f0f0f0f/stats", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	code, _ := errorBody(t, rec)
	if code != "not_found" {
		t.Fatalf("error code = %q, want not_found", code)
	}
}

func TestSessionStatsEmptySession(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body handlerStatsResponse
	decodeBody(t, rec, &body)
	data := body.Data
	if data.Turns != 0 || data.Steps != 0 || data.TotalTokens != 0 || data.ContextTokens != 0 {
		t.Fatalf("空会话统计 = %+v", data)
	}
	if data.CacheHitRate != nil || data.TokensPerSecond != nil {
		t.Fatalf("空会话速率应为 null: %+v", data)
	}
	if data.ContextWindow != 200000 || data.CompactRatio != 0.9 {
		t.Fatalf("默认上下文参数 = %d/%v, want 200000/0.9", data.ContextWindow, data.CompactRatio)
	}
}

func TestSessionStatsResponse(t *testing.T) {
	caps := capabilities.NewStore(t.TempDir())
	config := capabilities.DefaultConfig()
	config.ContextWindow = 128000
	config.CompactTriggerRatio = 0.8
	if _, err := caps.PutConfig(config); err != nil {
		t.Fatal(err)
	}
	handler, store, _ := newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.Capabilities = caps })
	meta, err := store.Create("mock-1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session","version":3,"id":"handler-stats","timestamp":"2026-10-29T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-29T00:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"第一问"}],"timestamp":1000}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"第一答"}],"usage":{"input":100,"output":40,"cacheRead":50,"cacheWrite":10,"totalTokens":140},"stopReason":"stop","timestamp":2000}}
{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-10-29T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"第二问"}],"timestamp":3000}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-10-29T00:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"第二答"}],"usage":{"input":200,"output":60,"cacheRead":150,"cacheWrite":0,"totalTokens":260},"stopReason":"stop","timestamp":4000}}
`
	path := filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_handler-stats.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.PiSessionID = "handler-stats"
		m.GenSamples = []session.GenSample{
			{Timestamp: 2000, OutputTokens: 40, GenMs: 400},
			{Timestamp: 4000, OutputTokens: 60, GenMs: 600},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	// 字段集合必须与契约完全一致,多一个少一个都算契约漂移。
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	decodeBody(t, rec, &raw)
	gotKeys := make([]string, 0, len(raw.Data))
	for key := range raw.Data {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{
		"cache_hit_rate", "cache_read_tokens", "cache_write_tokens", "compact_ratio",
		"context_tokens", "context_window", "input_tokens", "output_tokens",
		"steps", "tokens_per_second", "total_tokens", "turns",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("data 字段 = %v, want %v", gotKeys, wantKeys)
	}
	var body handlerStatsResponse
	decodeBody(t, rec, &body)
	data := body.Data
	if data.Turns != 2 || data.Steps != 2 || data.TotalTokens != 400 || data.ContextTokens != 260 {
		t.Fatalf("统计字段 = %+v", data)
	}
	if data.InputTokens != 510 || data.OutputTokens != 100 || data.CacheReadTokens != 200 || data.CacheWriteTokens != 10 {
		t.Fatalf("输入/输出/缓存 token = %d/%d/%d/%d, want 510/100/200/10",
			data.InputTokens, data.OutputTokens, data.CacheReadTokens, data.CacheWriteTokens)
	}
	if data.CacheHitRate == nil || math.Abs(*data.CacheHitRate-200.0/510.0) > 1e-9 {
		t.Fatalf("cache_hit_rate = %v, want %v", data.CacheHitRate, 200.0/510.0)
	}
	if data.TokensPerSecond == nil || *data.TokensPerSecond != 100 {
		t.Fatalf("tokens_per_second = %v, want 100", data.TokensPerSecond)
	}
	if data.ContextWindow != 128000 || data.CompactRatio != 0.8 {
		t.Fatalf("能力上下文参数 = %d/%v, want 128000/0.8", data.ContextWindow, data.CompactRatio)
	}
}
