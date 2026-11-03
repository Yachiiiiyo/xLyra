package piconfig

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

const testBaseURL = "http://127.0.0.1:8080"

// parsedProvider 系列用独立的反序列化结构断言生成的 JSON,避免与生成结构共用字段定义。
type parsedProvider struct {
	BaseURL    string            `json:"baseUrl"`
	API        string            `json:"api"`
	APIKey     string            `json:"apiKey"`
	AuthHeader bool              `json:"authHeader"`
	Headers    map[string]string `json:"headers"`
	Models     []parsedModel     `json:"models"`
}

type parsedModel struct {
	ID               string            `json:"id"`
	Reasoning        bool              `json:"reasoning"`
	ThinkingLevelMap map[string]string `json:"thinkingLevelMap"`
	Input            []string          `json:"input"`
	Compat           *parsedCompat     `json:"compat"`
}

type parsedCompat struct {
	ForceAdaptiveThinking bool   `json:"forceAdaptiveThinking"`
	ThinkingFormat        string `json:"thinkingFormat"`
	AllowEmptySignature   bool   `json:"allowEmptySignature"`
}

func buildModels(t *testing.T, modelID, protocol string) (parsedProvider, string) {
	t.Helper()
	data, err := ModelsJSON(ModelsInput{ModelID: modelID, Protocol: protocol, BaseURL: testBaseURL, RunID: "run-1"})
	if err != nil {
		t.Fatalf("ModelsJSON(%s): %v", modelID, err)
	}
	var file struct {
		Providers map[string]parsedProvider `json:"providers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode models.json for %s: %v\n%s", modelID, err, data)
	}
	if len(file.Providers) != 1 {
		t.Fatalf("want exactly one provider, got %d: %s", len(file.Providers), data)
	}
	provider, ok := file.Providers[ProviderName]
	if !ok {
		t.Fatalf("provider %q missing: %s", ProviderName, data)
	}
	return provider, string(data)
}

// TestModelsJSONVendorMatrix 逐行覆盖 07 §九.4 的 compat 清单与 §三.2/§八 的 baseUrl 写法。
func TestModelsJSONVendorMatrix(t *testing.T) {
	cases := []struct {
		name           string
		modelID        string
		protocol       string
		wantBaseURL    string
		wantAPI        string
		wantAuthHeader bool
		wantReasoning  bool
		wantInput      []string
		wantCompat     *parsedCompat
		wantLevelMap   map[string]string
	}{
		{
			name: "opus", modelID: "claude-opus-5-5", protocol: ProtocolAnthropicMessages,
			wantBaseURL: testBaseURL + "/internal/agent-llm", wantAPI: "anthropic-messages", wantAuthHeader: true,
			wantReasoning: true, wantInput: []string{"text", "image"},
			wantCompat: &parsedCompat{ForceAdaptiveThinking: true}, wantLevelMap: map[string]string{"xhigh": "xhigh", "max": "max"},
		},
		{
			// Claude 5 系列不接受 thinking.type=enabled,必须走 adaptive(真实上游报错实测)
			name: "sonnet", modelID: "claude-sonnet-5-5", protocol: ProtocolAnthropicMessages,
			wantBaseURL: testBaseURL + "/internal/agent-llm", wantAPI: "anthropic-messages", wantAuthHeader: true,
			wantReasoning: true, wantInput: []string{"text", "image"},
			wantCompat: &parsedCompat{ForceAdaptiveThinking: true},
		},
		{
			name: "gpt-6-luna", modelID: "gpt-6-luna", protocol: ProtocolOpenAIResponses,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-responses",
			wantReasoning: true, wantInput: []string{"text", "image"},
			wantLevelMap: map[string]string{"xhigh": "xhigh", "max": "max"},
		},
		{
			name: "gpt-5.6-sol", modelID: "gpt-5.6-sol", protocol: ProtocolOpenAIResponses,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-responses",
			wantReasoning: true, wantInput: []string{"text", "image"},
		},
		{
			name: "grok", modelID: "grok-4.7", protocol: ProtocolOpenAIResponses,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-responses",
			wantReasoning: true, wantInput: []string{"text", "image"},
		},
		{
			// 内部入口没有 google 原生协议,Gemini 模型由网关跨协议转换(07 §八.2)。
			name: "gemini", modelID: "gemini-3.8-flash", protocol: ProtocolOpenAIResponses,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-responses",
			wantReasoning: true, wantInput: []string{"text", "image"},
		},
		{
			name: "deepseek", modelID: "deepseek-v4.1-flash", protocol: ProtocolOpenAIChatCompletions,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-completions",
			wantReasoning: false, wantInput: []string{"text", "image"},
			wantCompat: &parsedCompat{AllowEmptySignature: true},
		},
		{
			name: "glm", modelID: "glm-5.3", protocol: ProtocolOpenAIChatCompletions,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-completions",
			wantReasoning: true, wantInput: []string{"text"},
			wantCompat: &parsedCompat{ThinkingFormat: "zai", AllowEmptySignature: true},
		},
		{
			name: "kimi", modelID: "kimi-k3.1-pro", protocol: ProtocolOpenAIChatCompletions,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-completions",
			// 2026-11-03 复测 Kimi 接受推理参数,前端选的强度要传下去;无签名思考块要按空签名回传。
			wantReasoning: true, wantInput: []string{"text"},
			wantCompat: &parsedCompat{AllowEmptySignature: true},
		},
		{
			name: "qwen", modelID: "qwen3.8-max", protocol: ProtocolOpenAIChatCompletions,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-completions",
			wantReasoning: true, wantInput: []string{"text", "image"},
			wantCompat: &parsedCompat{ThinkingFormat: "qwen", AllowEmptySignature: true},
		},
		{
			// 未知模型走保守默认:可推理、可收图,无 compat / map。
			name: "unknown", modelID: "internal-ghost-1", protocol: ProtocolOpenAIResponses,
			wantBaseURL: testBaseURL + "/internal/agent-llm/v1", wantAPI: "openai-responses",
			wantReasoning: true, wantInput: []string{"text", "image"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, raw := buildModels(t, tc.modelID, tc.protocol)
			if provider.BaseURL != tc.wantBaseURL {
				t.Fatalf("baseUrl = %q, want %q", provider.BaseURL, tc.wantBaseURL)
			}
			if provider.API != tc.wantAPI {
				t.Fatalf("api = %q, want %q", provider.API, tc.wantAPI)
			}
			if provider.AuthHeader != tc.wantAuthHeader {
				t.Fatalf("authHeader = %v, want %v", provider.AuthHeader, tc.wantAuthHeader)
			}
			// apiKey 只能是对环境变量的引用,序列化结果里不得出现令牌明文。
			if provider.APIKey != "$"+TokenEnvName {
				t.Fatalf("apiKey = %q, want env reference %q", provider.APIKey, "$"+TokenEnvName)
			}
			if strings.Count(raw, "$"+TokenEnvName) != 1 || strings.Contains(raw, "Bearer ") {
				t.Fatalf("token material leaked into models.json:\n%s", raw)
			}
			if got := provider.Headers[RunIDHeader]; got != "run-1" {
				t.Fatalf("headers[%s] = %q, want run-1", RunIDHeader, got)
			}
			if len(provider.Models) != 1 {
				t.Fatalf("want exactly one model, got %d", len(provider.Models))
			}
			model := provider.Models[0]
			if model.ID != tc.modelID {
				t.Fatalf("model id = %q, want %q", model.ID, tc.modelID)
			}
			if model.Reasoning != tc.wantReasoning {
				t.Fatalf("reasoning = %v, want %v", model.Reasoning, tc.wantReasoning)
			}
			if !slices.Equal(model.Input, tc.wantInput) {
				t.Fatalf("input = %v, want %v", model.Input, tc.wantInput)
			}
			if !equalCompat(model.Compat, tc.wantCompat) {
				t.Fatalf("compat = %+v, want %+v", model.Compat, tc.wantCompat)
			}
			if !maps.Equal(model.ThinkingLevelMap, tc.wantLevelMap) {
				t.Fatalf("thinkingLevelMap = %v, want %v", model.ThinkingLevelMap, tc.wantLevelMap)
			}
		})
	}
}

func equalCompat(got, want *parsedCompat) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func TestModelsJSONNormalizesBaseURL(t *testing.T) {
	data, err := ModelsJSON(ModelsInput{ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses, BaseURL: testBaseURL + "/", RunID: "run-1"})
	if err != nil {
		t.Fatalf("ModelsJSON: %v", err)
	}
	if !strings.Contains(string(data), `"baseUrl": "`+testBaseURL+internalLLMPathV1+`"`) {
		t.Fatalf("trailing slash not normalized:\n%s", data)
	}
}

// TestModelsJSONTokenFileModes 覆盖 apiKey 的两种模式:令牌文件命令(!cat,
// 每次请求重读、可轮换)与环境变量引用($XLYRA_TOKEN,进程启动后固定)。
func TestModelsJSONTokenFileModes(t *testing.T) {
	cases := []struct {
		name      string
		tokenFile string
		wantKey   string
	}{
		{"env reference", "", "$" + TokenEnvName},
		{"token file", "/var/lib/xlyra/agent/tokens/run-1", "!cat '/var/lib/xlyra/agent/tokens/run-1'"},
		{"spaces in path", "/tmp/x lyra token", "!cat '/tmp/x lyra token'"},
		// POSIX 单引号转义:' 结束引号、\' 转义字面量、' 重开引号。
		{"single quote in path", "/tmp/it's", "!cat '/tmp/it'\\''s'"},
		{"quote mid path", "/tmp/a'b", "!cat '/tmp/a'\\''b'"},
		{"trimmed", " /tmp/token ", "!cat '/tmp/token'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := ModelsJSON(ModelsInput{
				ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses, BaseURL: testBaseURL, RunID: "run-1",
				TokenFile: tc.tokenFile,
			})
			if err != nil {
				t.Fatalf("ModelsJSON: %v", err)
			}
			var file struct {
				Providers map[string]parsedProvider `json:"providers"`
			}
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("decode: %v\n%s", err, data)
			}
			provider := file.Providers[ProviderName]
			if provider.APIKey != tc.wantKey {
				t.Fatalf("apiKey = %q, want %q", provider.APIKey, tc.wantKey)
			}
			if strings.Contains(string(data), "Bearer ") {
				t.Fatalf("token material leaked:\n%s", data)
			}
		})
	}
}

func TestModelsJSONRejectsInvalidInput(t *testing.T) {
	valid := ModelsInput{ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses, BaseURL: testBaseURL, RunID: "run-1"}
	if _, err := ModelsJSON(ModelsInput{ModelID: "gpt-5", Protocol: "google-generative-ai", BaseURL: testBaseURL, RunID: "run-1"}); err == nil {
		t.Fatal("unsupported protocol must be rejected")
	}
	if _, err := ModelsJSON(ModelsInput{ModelID: " ", Protocol: ProtocolOpenAIResponses, BaseURL: testBaseURL, RunID: "run-1"}); err == nil {
		t.Fatal("empty model id must be rejected")
	}
	if _, err := ModelsJSON(ModelsInput{ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses, BaseURL: "", RunID: "run-1"}); err == nil {
		t.Fatal("empty base url must be rejected")
	}
	if _, err := ModelsJSON(ModelsInput{ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses, BaseURL: testBaseURL, RunID: ""}); err == nil {
		t.Fatal("empty run id must be rejected")
	}
	if _, err := ModelsJSON(valid); err != nil {
		t.Fatalf("valid input must pass: %v", err)
	}
}

func TestCapsForVendors(t *testing.T) {
	cases := []struct {
		modelID string
		vendor  string
	}{
		{"claude-opus-5-5", "anthropic"},
		{"gpt-5.6-sol", "openai"},
		{"grok-4.7", "xai"},
		{"gemini-3.8-flash", "google"},
		{"deepseek-v4.1-flash", "deepseek"},
		{"glm-5.3", "glm"},
		{"kimi-k3.1-pro", "kimi"},
		{"qwen3.8-max", "qwen"},
		{"Qwen3.8-Max", "qwen"},
		{"totally-unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := CapsFor(tc.modelID).Vendor; got != tc.vendor {
			t.Fatalf("CapsFor(%s).Vendor = %q, want %q", tc.modelID, got, tc.vendor)
		}
	}
	// DeepSeek 只回 none:推理参数会被上游渠道拒绝。
	if efforts := CapsFor("deepseek-v4.1-flash").Efforts; !slices.Equal(efforts, []string{EffortNone}) {
		t.Fatalf("CapsFor(deepseek).Efforts = %v, want [none]", efforts)
	}
	// Kimi 经网关接受推理参数,走标准四档;无签名思考块要按空签名回传(allowEmptySignature)。
	kimi := CapsFor("kimi-k3.1-pro")
	if !kimi.Reasoning || !kimi.AllowEmptySignature || !slices.Equal(kimi.Efforts, standardEfforts) {
		t.Fatalf("CapsFor(kimi) = %+v, want Reasoning+AllowEmptySignature+standard efforts", kimi)
	}
	if got := ThinkingLevel(EffortHigh, kimi); got != LevelHigh {
		t.Fatalf("ThinkingLevel(high, kimi) = %q, want %q", got, LevelHigh)
	}
	// Claude 的思考块带签名,不应打开 allowEmptySignature。
	if CapsFor("claude-sonnet-5-5").AllowEmptySignature {
		t.Fatal("claude must not allow empty signature")
	}
}

func TestThinkingLevel(t *testing.T) {
	opus := CapsFor("claude-opus-5-5")
	luna := CapsFor("gpt-6-luna")
	plain := CapsFor("gpt-5.6-sol")
	deepseek := CapsFor("deepseek-v4.1-flash")
	cases := []struct {
		effort                      string
		opus, luna, plain, deepseek string
	}{
		{"none", "off", "off", "off", "off"},
		{"minimal", "low", "low", "low", "off"},
		{"low", "low", "low", "low", "off"},
		{"medium", "medium", "medium", "medium", "off"},
		{"high", "high", "high", "high", "off"},
		{"xhigh", "xhigh", "xhigh", "high", "off"},
		{"max", "max", "max", "high", "off"},
		{"ultra", "max", "max", "high", "off"},
	}
	for _, tc := range cases {
		for _, item := range []struct {
			name string
			caps Caps
			want string
		}{
			{"opus", opus, tc.opus},
			{"luna", luna, tc.luna},
			{"plain", plain, tc.plain},
			{"deepseek", deepseek, tc.deepseek},
		} {
			if got := ThinkingLevel(tc.effort, item.caps); got != item.want {
				t.Fatalf("ThinkingLevel(%s, %s) = %q, want %q", tc.effort, item.name, got, item.want)
			}
		}
	}
	// 只支持 xhigh 不支持 max 的模型:ultra/max 都应降到 xhigh。
	xhighOnly := Caps{Reasoning: true, Efforts: []string{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh}}
	if got := ThinkingLevel(EffortUltra, xhighOnly); got != "xhigh" {
		t.Fatalf("ThinkingLevel(ultra, xhighOnly) = %q, want xhigh", got)
	}
	if got := ThinkingLevel(EffortMax, xhighOnly); got != "xhigh" {
		t.Fatalf("ThinkingLevel(max, xhighOnly) = %q, want xhigh", got)
	}
	// Reasoning=false 恒为 off,空 Efforts 的自定义 Caps 也能安全夹取。
	if got := ThinkingLevel(EffortMax, Caps{Reasoning: false}); got != "off" {
		t.Fatalf("ThinkingLevel(max, reasoning=false) = %q, want off", got)
	}
	if got := ThinkingLevel(EffortXHigh, Caps{Reasoning: true}); got != "high" {
		t.Fatalf("ThinkingLevel(xhigh, empty efforts) = %q, want high", got)
	}
}

func TestSettingsJSON(t *testing.T) {
	data, err := SettingsJSON(SettingsInput{ModelID: "gpt-5.6-sol", ThinkingLevel: "high"})
	if err != nil {
		t.Fatalf("SettingsJSON: %v", err)
	}
	var file struct {
		DefaultProvider      string  `json:"defaultProvider"`
		DefaultModel         string  `json:"defaultModel"`
		DefaultThinkingLevel *string `json:"defaultThinkingLevel"`
		Compaction           struct {
			Enabled bool `json:"enabled"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode settings.json: %v\n%s", err, data)
	}
	if file.DefaultProvider != ProviderName || file.DefaultModel != "gpt-5.6-sol" {
		t.Fatalf("defaults = %q/%q, want %q/gpt-5.6-sol", file.DefaultProvider, file.DefaultModel, ProviderName)
	}
	if file.DefaultThinkingLevel == nil || *file.DefaultThinkingLevel != "high" {
		t.Fatalf("defaultThinkingLevel = %v, want high", file.DefaultThinkingLevel)
	}
	if !file.Compaction.Enabled {
		t.Fatal("compaction must be enabled")
	}

	// 空等级省略字段,交给 Pi 默认;非法等级直接报错。
	empty, err := SettingsJSON(SettingsInput{ModelID: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("SettingsJSON: %v", err)
	}
	if strings.Contains(string(empty), "defaultThinkingLevel") {
		t.Fatalf("empty level must omit defaultThinkingLevel:\n%s", empty)
	}
	if _, err := SettingsJSON(SettingsInput{ModelID: "gpt-5.6-sol", ThinkingLevel: "ultra"}); err == nil {
		t.Fatal("ultra is not a Pi level and must be rejected")
	}
	if _, err := SettingsJSON(SettingsInput{ModelID: "", ThinkingLevel: "high"}); err == nil {
		t.Fatal("empty model id must be rejected")
	}
}

func TestSettingsJSONCompactionReserve(t *testing.T) {
	data, err := SettingsJSON(SettingsInput{ModelID: "gpt-5", CompactionReserveTokens: 20000})
	if err != nil {
		t.Fatalf("SettingsJSON: %v", err)
	}
	var file struct {
		Compaction struct {
			Enabled       bool `json:"enabled"`
			ReserveTokens int  `json:"reserveTokens"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode settings.json: %v\n%s", err, data)
	}
	if !file.Compaction.Enabled || file.Compaction.ReserveTokens != 20000 {
		t.Fatalf("compaction = %+v, want enabled+20000:\n%s", file.Compaction, data)
	}

	// 0 时省略 reserveTokens,交给 Pi 默认。
	empty, err := SettingsJSON(SettingsInput{ModelID: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "reserveTokens") {
		t.Fatalf("0 应省略 reserveTokens:\n%s", empty)
	}
}

// TestModelsJSONContextWindow:窗口由调用方注入,未设置按 200000 兜底;
// maxTokens 占位保持不变。
func TestModelsJSONContextWindow(t *testing.T) {
	build := func(window int) int {
		t.Helper()
		data, err := ModelsJSON(ModelsInput{
			ModelID: "gpt-5", Protocol: ProtocolOpenAIResponses,
			BaseURL: testBaseURL, RunID: "run-1", ContextWindow: window,
		})
		if err != nil {
			t.Fatalf("ModelsJSON: %v", err)
		}
		var file struct {
			Providers map[string]struct {
				Models []struct {
					ContextWindow int `json:"contextWindow"`
					MaxTokens     int `json:"maxTokens"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatalf("decode: %v\n%s", err, data)
		}
		models := file.Providers[ProviderName].Models
		if len(models) != 1 {
			t.Fatalf("models = %d: %s", len(models), data)
		}
		if models[0].MaxTokens != defaultMaxTokens {
			t.Fatalf("maxTokens = %d, want 占位 %d", models[0].MaxTokens, defaultMaxTokens)
		}
		return models[0].ContextWindow
	}
	if got := build(100000); got != 100000 {
		t.Fatalf("contextWindow = %d, want 100000", got)
	}
	if got := build(0); got != fallbackContextWindow {
		t.Fatalf("contextWindow(0) = %d, want %d", got, fallbackContextWindow)
	}
}

func TestEnvIsClosed(t *testing.T) {
	t.Setenv("SECRET_X", "1")
	in := EnvInput{Home: "/home/agent", AgentDir: "/agent", SessionDir: "/sessions", Path: "/usr/local/bin:/usr/bin:/bin", Token: "tok-1"}
	env := Env(in)

	wantKeys := []string{"PATH", "HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "PI_OFFLINE", "PI_SKIP_VERSION_CHECK", "PI_TELEMETRY", TokenEnvName}
	keys := make([]string, 0, len(env))
	values := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		keys = append(keys, key)
		values[key] = value
	}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("env keys = %v, want exactly %v", keys, wantKeys)
	}
	wantValues := map[string]string{
		"PATH": in.Path, "HOME": in.Home,
		"PI_CODING_AGENT_DIR": in.AgentDir, "PI_CODING_AGENT_SESSION_DIR": in.SessionDir,
		"PI_OFFLINE": "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0",
		TokenEnvName: in.Token,
	}
	if !maps.Equal(values, wantValues) {
		t.Fatalf("env values = %v, want %v", values, wantValues)
	}
	// 宿主进程环境(os.Environ)里的任何变量都不得进入,包括测试进程注入的 SECRET_X。
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains(wantKeys, key) {
			continue
		}
		if _, ok := values[key]; ok {
			t.Fatalf("host environment variable %q leaked into Env", key)
		}
	}
}

// TestEnvOmitsTokenWhenEmpty:令牌改走文件轮换时 Token 留空,子进程环境只有
// 7 个键且不含 XLYRA_TOKEN(令牌明文不进入环境)。
func TestEnvOmitsTokenWhenEmpty(t *testing.T) {
	env := Env(EnvInput{Home: "/home/agent", AgentDir: "/agent", SessionDir: "/sessions", Path: "/usr/bin:/bin"})
	if len(env) != 7 {
		t.Fatalf("env 键数 = %d, want 7: %v", len(env), env)
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, TokenEnvName+"=") {
			t.Fatalf("Token 为空时不应输出 %s: %v", TokenEnvName, env)
		}
	}
}

func TestClaudeAdaptiveThinkingByMajorVersion(t *testing.T) {
	cases := []struct {
		modelID string
		want    bool
	}{
		{"claude-sonnet-5-5", true},
		{"claude-haiku-5-5", true},
		{"claude-fable-5-1", true},
		{"claude-opus-5-5", true},
		{"claude-opus-4-1", true}, // Opus 一直走 adaptive
		{"Claude-Sonnet-5-5", true},
		{"claude-sonnet-4-5", false},
		{"claude-3-5-sonnet", false},
		{"claude-haiku-4-5-20251001", false},
		{"claude-instant", false},
	}
	for _, tc := range cases {
		if got := CapsFor(tc.modelID).ForceAdaptiveThinking; got != tc.want {
			t.Fatalf("CapsFor(%s).ForceAdaptiveThinking = %v, want %v", tc.modelID, got, tc.want)
		}
	}
	// 只有 Opus 声明 xhigh/max 的真实取值,其余 Claude 5 仍用标准档位
	if CapsFor("claude-sonnet-5-5").ThinkingLevelMap != nil {
		t.Fatal("sonnet 不应声明 thinkingLevelMap")
	}
}
