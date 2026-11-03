// Package piconfig 生成内置 Pi 子进程的运行配置:models.json、settings.json 与一份
// 清空重建的环境变量。全部是纯函数:不做 IO、不读取 os.Environ、不依赖 Pi 进程。
// 字段写法的权威来源:Pi docs(models.md / settings.md / environment-variables.md)
// 与 docs/pi-agent-integration/07-poc-results.md §三.2(baseUrl)、§八(真实网关)、
// §九.1–§九.4(推理强度、图片输入、compat 清单)。
package piconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ProviderName 是 models.json 里唯一 provider 的名字,settings.json 的
// defaultProvider 与之对应。
const ProviderName = "xlyra"

// TokenEnvName 承载绑定 run 的短期令牌。models.json 的 apiKey 通过
// "$XLYRA_TOKEN" 引用它:令牌明文只出现在子进程环境里,绝不写进配置文件。
const TokenEnvName = "XLYRA_TOKEN"

// RunIDHeader 随每个 LLM 请求发往网关,供按 run 归属用量(07 §八.3.5)。
const RunIDHeader = "x-xlyra-run-id"

// xLyra 内部 LLM 入口(router.go 的 /internal/agent-llm)的路径。anthropic
// 协议不带 /v1:Anthropic SDK 恒定再拼 /v1/messages,baseUrl 带 /v1 会变成
// /v1/v1/messages(07 §三.2)。
const (
	internalLLMPath   = "/internal/agent-llm"
	internalLLMPathV1 = internalLLMPath + "/v1"
)

// Protocol 取值同 app 包 SetProtocolResolver 的返回值。
const (
	ProtocolAnthropicMessages     = "anthropic-messages"
	ProtocolOpenAIResponses       = "openai-responses"
	ProtocolOpenAIChatCompletions = "openai-chat-completions"
)

// 前端 8 个推理等级(ReasoningEffort)。
const (
	EffortNone    = "none"
	EffortMinimal = "minimal"
	EffortLow     = "low"
	EffortMedium  = "medium"
	EffortHigh    = "high"
	EffortXHigh   = "xhigh"
	EffortMax     = "max"
	EffortUltra   = "ultra"
)

// Pi 的思考等级名(settings.md 的 defaultThinkingLevel / RPC set_thinking_level)。
const (
	LevelOff     = "off"
	LevelMinimal = "minimal"
	LevelLow     = "low"
	LevelMedium  = "medium"
	LevelHigh    = "high"
	LevelXHigh   = "xhigh"
	LevelMax     = "max"
)

var (
	// minimal 在任何协议上都未实测过(07 §九.2),不回传给前端,因此各等级集合都不含它。
	standardEfforts = []string{EffortNone, EffortLow, EffortMedium, EffortHigh}
	extendedEfforts = []string{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
	offOnlyEfforts  = []string{EffortNone}
	// verifiedLevelMap 声明 xhigh/max 的真实取值:只有显式配了 thinkingLevelMap
	// 的模型(Opus adaptive、gpt-6-luna),Pi 才不会把 xhigh/max 夹取到 high
	// (07 §九.1 夹取规则)。
	verifiedLevelMap = map[string]string{"xhigh": "xhigh", "max": "max"}
	// thinkingLevelLadder 是夹取降级的顺序,从高到低。
	thinkingLevelLadder = []string{LevelMax, LevelXHigh, LevelHigh}
	piThinkingLevels    = []string{LevelOff, LevelMinimal, LevelLow, LevelMedium, LevelHigh, LevelXHigh, LevelMax}
)

// Caps 描述一个模型经 xLyra 网关的实测能力(07 §九),驱动 models.json 的
// reasoning / input / compat / thinkingLevelMap 与可回传前端的推理等级。
type Caps struct {
	// Vendor: anthropic/openai/xai/google/deepseek/glm/kimi/qwen/unknown。
	Vendor string
	// Reasoning 为 false 时不向模型发任何推理参数(DeepSeek、Kimi 的上游渠道限制)。
	Reasoning bool
	// Image 为 false 时 input 只含 text,前端应禁用图片附件(GLM、Kimi)。
	Image bool
	// Efforts 是该模型可用的前端推理等级(前端 8 级的子集),供回传前端。
	Efforts []string
	// ForceAdaptiveThinking 对应 compat.forceAdaptiveThinking,目前仅 Anthropic
	// Opus 需要:网关对 Opus 非 adaptive 请求的校验不稳定,统一走 adaptive
	// (07 §九.5)。
	ForceAdaptiveThinking bool
	// ThinkingFormat 对应 compat.thinkingFormat:GLM 用 zai、Qwen 用 qwen
	// (07 §九.1)。
	ThinkingFormat string
	// ThinkingLevelMap 非空时写入 models.json,声明 xhigh/max 的真实取值。
	ThinkingLevelMap map[string]string
}

// claudeMajorVersion 取 claude 模型 id 里的主版本号:新写法 claude-sonnet-5-5、
// claude-fable-5-1 与旧写法 claude-3-5-sonnet 都是"第一个纯数字段";取不到返回 0。
func claudeMajorVersion(id string) int {
	for _, part := range strings.Split(strings.TrimPrefix(id, "claude-"), "-") {
		if n, err := strconv.Atoi(part); err == nil {
			return n
		}
	}
	return 0
}

// CapsFor 按模型 id 前缀/关键字识别厂商,返回 07 §九.4 compat 清单对应的能力;
// 未知模型走保守默认(Reasoning=true、Image=true、Efforts=none/low/medium/high)。
func CapsFor(modelID string) Caps {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.HasPrefix(id, "claude-"):
		caps := Caps{Vendor: "anthropic", Reasoning: true, Image: true, Efforts: slices.Clone(standardEfforts)}
		// Claude 5 起的模型不再接受 thinking.type=enabled(budget_tokens),上游会返回
		// "requires adaptive thinking or thinking.type=between_tools"(Pi 对不认识的新
		// 模型 id 默认发 enabled),所以主版本 >= 5 的一律走 adaptive。
		if strings.Contains(id, "opus") || claudeMajorVersion(id) >= 5 {
			caps.ForceAdaptiveThinking = true
		}
		if strings.Contains(id, "opus") {
			caps.ThinkingLevelMap = maps.Clone(verifiedLevelMap)
			caps.Efforts = slices.Clone(extendedEfforts)
		}
		return caps
	case strings.HasPrefix(id, "gpt-"):
		caps := Caps{Vendor: "openai", Reasoning: true, Image: true, Efforts: slices.Clone(standardEfforts)}
		// gpt-6-luna 是目前唯一实测 xhigh/max 直通的 OpenAI 模型(07 §九.1)。
		if strings.Contains(id, "luna") {
			caps.ThinkingLevelMap = maps.Clone(verifiedLevelMap)
			caps.Efforts = slices.Clone(extendedEfforts)
		}
		return caps
	case strings.HasPrefix(id, "grok-"):
		return Caps{Vendor: "xai", Reasoning: true, Image: true, Efforts: slices.Clone(standardEfforts)}
	case strings.HasPrefix(id, "gemini-"):
		return Caps{Vendor: "google", Reasoning: true, Image: true, Efforts: slices.Clone(standardEfforts)}
	case strings.HasPrefix(id, "deepseek-"):
		// 上游渠道拒绝任何推理参数;图片反而实测可用(07 §九.1、§九.3)。
		return Caps{Vendor: "deepseek", Reasoning: false, Image: true, Efforts: slices.Clone(offOnlyEfforts)}
	case strings.HasPrefix(id, "glm-"):
		return Caps{Vendor: "glm", Reasoning: true, Image: false, ThinkingFormat: "zai", Efforts: slices.Clone(standardEfforts)}
	case strings.HasPrefix(id, "kimi-"):
		return Caps{Vendor: "kimi", Reasoning: false, Image: false, Efforts: slices.Clone(offOnlyEfforts)}
	case strings.HasPrefix(id, "qwen"):
		return Caps{Vendor: "qwen", Reasoning: true, Image: true, ThinkingFormat: "qwen", Efforts: slices.Clone(standardEfforts)}
	default:
		return Caps{Vendor: "unknown", Reasoning: true, Image: true, Efforts: slices.Clone(standardEfforts)}
	}
}

// ThinkingLevel 把前端 8 个推理等级映射成 Pi 等级(07 §九.2):none→off、
// minimal→low、low/medium/high 同名;xhigh/max/ultra 按 Caps 能力夹取,模型
// 不支持时降级到它能达到的最高档(ultra→max→xhigh→high);Reasoning=false
// 的模型恒返回 off。未识别的等级落到 medium(Pi 的默认档)。
func ThinkingLevel(effort string, caps Caps) string {
	if !caps.Reasoning {
		return LevelOff
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case EffortNone:
		return LevelOff
	case EffortMinimal, EffortLow:
		return LevelLow
	case EffortMedium:
		return LevelMedium
	case EffortHigh:
		return LevelHigh
	case EffortXHigh:
		return clampThinkingLevel(LevelXHigh, caps)
	case EffortMax, EffortUltra:
		return clampThinkingLevel(LevelMax, caps)
	default:
		return LevelMedium
	}
}

// clampThinkingLevel 从 preferred 开始沿 max→xhigh→high 向下找模型支持的档位;
// high 是所有已验证模型都支持的兜底。
func clampThinkingLevel(preferred string, caps Caps) string {
	supported := make(map[string]bool, len(caps.Efforts))
	for _, level := range caps.Efforts {
		supported[level] = true
	}
	for i, level := range thinkingLevelLadder {
		if level != preferred {
			continue
		}
		for _, candidate := range thinkingLevelLadder[i:] {
			if supported[candidate] {
				return candidate
			}
		}
	}
	return LevelHigh
}

// ModelsInput 描述一次 run 需要的 models.json 输入。
type ModelsInput struct {
	ModelID string
	// Protocol 见 SetProtocolResolver 的返回值。
	Protocol string
	// BaseURL 是 xLyra 本机根地址,如 http://127.0.0.1:8080(无尾斜杠)。
	BaseURL string
	// RunID 写进请求头 x-xlyra-run-id。
	RunID string
	// TokenFile 非空时 apiKey 写成 "!cat '<TokenFile>'":Pi 在每次请求时经
	// shell 执行该命令读取令牌文件且不缓存(models.md),令牌轮换只需重写
	// 文件;为空时退回引用环境变量 XLYRA_TOKEN(进程启动后无法续期)。
	TokenFile string
	// ContextWindow 写入模型的 contextWindow,参与 Pi 的压缩触发;0 表示
	// 未设置,按 200000 兜底。
	ContextWindow int
}

// models.json 的落地结构。字段名与 Pi 源码的 ModelDefinitionSchema /
// ProviderConfigSchema(dist/core/model-config.d.ts)一致;compat 与
// thinkingLevelMap 未出现在官方 .md 里,以该 schema 与 PoC 实测为准。
type modelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

type piProvider struct {
	BaseURL string            `json:"baseUrl"`
	API     string            `json:"api"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers"`
	// authHeader 让 Anthropic 协议在 x-api-key 之外再发 Authorization: Bearer
	// (07 §二 T8);内部 LLM 入口只认 Bearer,必须开启。
	AuthHeader bool      `json:"authHeader,omitempty"`
	Models     []piModel `json:"models"`
}

type piModel struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Reasoning        bool              `json:"reasoning"`
	ThinkingLevelMap map[string]string `json:"thinkingLevelMap,omitempty"`
	Input            []string          `json:"input"`
	Cost             piCost            `json:"cost"`
	ContextWindow    int               `json:"contextWindow"`
	MaxTokens        int               `json:"maxTokens"`
	Compat           *piCompat         `json:"compat,omitempty"`
}

type piCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type piCompat struct {
	ForceAdaptiveThinking bool   `json:"forceAdaptiveThinking,omitempty"`
	ThinkingFormat        string `json:"thinkingFormat,omitempty"`
}

// 窗口与输出上限:contextWindow 由调用方按能力配置传入(ModelsInput.ContextWindow,
// 压缩触发条件 contextTokens > contextWindow - reserveTokens 的一部分),未设置时
// 按 200000 兜底;maxTokens 是占位值,逐模型的元数据尚未接入,不能过小,否则
// 高推理档位会被输出上限吃掉(07 §九.4:Opus 的 max 在 max_tokens=768 时回答为空)。
const (
	fallbackContextWindow = 200000
	defaultMaxTokens      = 8192
)

// ModelsJSON 生成 models.json:单 provider("xlyra")、单模型。baseUrl 按
// 07 §三.2/§八:openai-* 两种协议带 /v1,anthropic-messages 不带;apiKey 按
// TokenFile 取 "!cat <文件>" 命令或环境变量引用;按 CapsFor 写入 reasoning:false /
// input 不含 image / compat / thinkingLevelMap。文件落在 PI_CODING_AGENT_DIR 下,
// 由调用方写盘。
func ModelsJSON(in ModelsInput) ([]byte, error) {
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		return nil, errors.New("piconfig: model id is required")
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return nil, errors.New("piconfig: base url is required")
	}
	if strings.TrimSpace(in.RunID) == "" {
		return nil, errors.New("piconfig: run id is required")
	}
	api, err := piAPIName(in.Protocol)
	if err != nil {
		return nil, err
	}
	apiKey := "$" + TokenEnvName
	if path := strings.TrimSpace(in.TokenFile); path != "" {
		apiKey = tokenFileCommand(path)
	}
	provider := piProvider{
		BaseURL: piBaseURL(in.BaseURL, in.Protocol),
		API:     api,
		APIKey:  apiKey,
		Headers: map[string]string{RunIDHeader: strings.TrimSpace(in.RunID)},
		Models:  []piModel{newPIModel(modelID, CapsFor(modelID), resolveContextWindow(in.ContextWindow))},
	}
	if in.Protocol == ProtocolAnthropicMessages {
		provider.AuthHeader = true
	}
	return json.MarshalIndent(modelsFile{Providers: map[string]piProvider{ProviderName: provider}}, "", "  ")
}

// resolveContextWindow 把未设置(0)的窗口归一到 200000 兜底。
func resolveContextWindow(configured int) int {
	if configured <= 0 {
		return fallbackContextWindow
	}
	return configured
}

func newPIModel(modelID string, caps Caps, window int) piModel {
	input := []string{"text"}
	if caps.Image {
		input = append(input, "image")
	}
	model := piModel{
		ID:               modelID,
		Name:             modelID,
		Reasoning:        caps.Reasoning,
		ThinkingLevelMap: maps.Clone(caps.ThinkingLevelMap),
		Input:            input,
		Cost:             piCost{},
		ContextWindow:    window,
		MaxTokens:        defaultMaxTokens,
	}
	if caps.ForceAdaptiveThinking || caps.ThinkingFormat != "" {
		model.Compat = &piCompat{ForceAdaptiveThinking: caps.ForceAdaptiveThinking, ThinkingFormat: caps.ThinkingFormat}
	}
	return model
}

// tokenFileCommand 生成 apiKey 的 "!cat <文件>" 命令:Pi 把 ! 后的整串交给
// shell 执行,路径按 POSIX 单引号规则转义(路径里的单引号先闭合引号、用
// 反斜杠转义后再重开),空格等特殊字符不外泄。
// 路径由本包调用方生成,仍做完整转义以防配置异常。
func tokenFileCommand(path string) string {
	return "!cat '" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// piAPIName 把 xLyra 协议名映射到 Pi 的 api 名;google-generative-ai 不在其中:
// 内部 LLM 入口没有 /v1beta 路由,Gemini 模型由网关做跨协议转换(07 §八.2)。
func piAPIName(protocol string) (string, error) {
	switch protocol {
	case ProtocolAnthropicMessages:
		return "anthropic-messages", nil
	case ProtocolOpenAIResponses:
		return "openai-responses", nil
	case ProtocolOpenAIChatCompletions:
		// Pi 对 chat/completions 的 api 名不含 "chat"。
		return "openai-completions", nil
	default:
		return "", fmt.Errorf("piconfig: unsupported downstream protocol %q", protocol)
	}
}

func piBaseURL(baseURL, protocol string) string {
	root := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if protocol == ProtocolAnthropicMessages {
		return root + internalLLMPath
	}
	return root + internalLLMPathV1
}

// SettingsInput 描述 settings.json 的输入;文件同样落在 PI_CODING_AGENT_DIR。
type SettingsInput struct {
	ModelID string
	// ThinkingLevel 是 Pi 等级(off/minimal/low/medium/high/xhigh/max);
	// 为空时省略该字段,交给 Pi 默认(medium)。
	ThinkingLevel string
	// CompactionReserveTokens 写入 compaction.reserveTokens:Pi 在上下文
	// token 超过 contextWindow - reserveTokens 时自动压缩(settings.md
	// §Compaction);0 时省略字段,交给 Pi 默认(16384)。
	CompactionReserveTokens int
}

type settingsFile struct {
	DefaultProvider      string       `json:"defaultProvider"`
	DefaultModel         string       `json:"defaultModel"`
	DefaultThinkingLevel string       `json:"defaultThinkingLevel,omitempty"`
	Compaction           piCompaction `json:"compaction"`
}

type piCompaction struct {
	Enabled       bool `json:"enabled"`
	ReserveTokens int  `json:"reserveTokens,omitempty"`
}

// SettingsJSON 生成 settings.json:默认 provider/model、默认推理等级、压缩开启;
// 其余字段省略,交给 Pi 默认(字段名以 Pi docs/settings.md 为准)。
func SettingsJSON(in SettingsInput) ([]byte, error) {
	modelID := strings.TrimSpace(in.ModelID)
	if modelID == "" {
		return nil, errors.New("piconfig: model id is required")
	}
	level := strings.ToLower(strings.TrimSpace(in.ThinkingLevel))
	if level != "" && !slices.Contains(piThinkingLevels, level) {
		return nil, fmt.Errorf("piconfig: invalid thinking level %q", in.ThinkingLevel)
	}
	file := settingsFile{DefaultProvider: ProviderName, DefaultModel: modelID, DefaultThinkingLevel: level}
	file.Compaction = piCompaction{Enabled: true, ReserveTokens: in.CompactionReserveTokens}
	return json.MarshalIndent(file, "", "  ")
}

// EnvInput 描述 Pi 子进程的环境变量输入。
type EnvInput struct {
	// Home 是会话目录(HOME)。
	Home string
	// AgentDir 是 PI_CODING_AGENT_DIR,models.json / settings.json 从这里读。
	AgentDir string
	// SessionDir 是 PI_CODING_AGENT_SESSION_DIR。
	SessionDir string
	// Path 需含 node 所在目录与系统目录。
	Path string
	// Token 是绑定该 run 的短期令牌;非空时经 XLYRA_TOKEN 传入(旧模式,
	// 令牌随进程启动固定)。令牌改走文件轮换时应留空,Env 即不含 XLYRA_TOKEN。
	Token string
}

// Env 构造 Pi 子进程的完整环境:PATH、HOME、PI_CODING_AGENT_DIR、
// PI_CODING_AGENT_SESSION_DIR、PI_OFFLINE=1、PI_SKIP_VERSION_CHECK=1、
// PI_TELEMETRY=0,Token 非空时再加 XLYRA_TOKEN(共 8 个键);宿主进程的其余
// 环境(含密钥)一律不继承(07 §二 T1:这样启动实测零出站连接)。
func Env(in EnvInput) []string {
	env := []string{
		"PATH=" + in.Path,
		"HOME=" + in.Home,
		"PI_CODING_AGENT_DIR=" + in.AgentDir,
		"PI_CODING_AGENT_SESSION_DIR=" + in.SessionDir,
		"PI_OFFLINE=1",
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
	}
	if in.Token != "" {
		env = append(env, TokenEnvName+"="+in.Token)
	}
	return env
}
