package config

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	AppEnv              string        `env:"APP_ENV" envDefault:"development"`
	AppName             string        `env:"APP_NAME" envDefault:"xlyra-server"`
	HTTPHost            string        `env:"HTTP_HOST" envDefault:"0.0.0.0"`
	HTTPPort            int           `env:"HTTP_PORT" envDefault:"5801"`
	LogLevel            string        `env:"LOG_LEVEL" envDefault:"info"`
	LogDebugEnabled     bool          `env:"LOG_DEBUG_ENABLED" envDefault:"false"`
	LogDir              string        `env:"LOG_DIR" envDefault:"logs"`
	LogFilePrefix       string        `env:"LOG_FILE_PREFIX" envDefault:"xlyra-server"`
	LogRetentionDays    int           `env:"LOG_RETENTION_DAYS" envDefault:"14"`
	LogToStdout         bool          `env:"LOG_TO_STDOUT" envDefault:"true"`
	ReadHeaderTimeout   time.Duration `env:"READ_HEADER_TIMEOUT" envDefault:"5s"`
	RequestTimeout      time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s"`
	MaxRequestBodyBytes int64         `env:"MAX_REQUEST_BODY_BYTES" envDefault:"268435456"`
	ShutdownTimeout     time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"10s"`
	DBConnectTimeout    time.Duration `env:"DB_CONNECT_TIMEOUT" envDefault:"30s"`
	DBMinConns          int32         `env:"DB_MIN_CONNS" envDefault:"2"`
	DBMaxConns          int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	CORSAllowedOrigins  []string      `env:"CORS_ALLOWED_ORIGINS" envSeparator:"," envDefault:"http://localhost:5173"`
	PostgresDSN         string        `env:"POSTGRES_DSN"`
	DBHost              string        `env:"DB_HOST"`
	DBPort              int           `env:"DB_PORT" envDefault:"5432"`
	DBName              string        `env:"DB_NAME" envDefault:"xlyra"`
	DBUser              string        `env:"DB_USER" envDefault:"postgres"`
	DBPassword          string        `env:"DB_PASSWORD"`
	DBSSLMode           string        `env:"DB_SSLMODE" envDefault:"disable"`
	SiteHealthInterval  time.Duration `env:"SITE_HEALTH_INTERVAL" envDefault:"15m"`
	SiteHealthTimeout   time.Duration `env:"SITE_HEALTH_TIMEOUT" envDefault:"10s"`
	SiteHealthWorkers   int           `env:"SITE_HEALTH_WORKERS" envDefault:"4"`
	StaticDir           string        `env:"STATIC_DIR" envDefault:"/app/dist"`
	AgentRunnerBaseURL  string        `env:"AGENT_RUNNER_BASE_URL"`
	AgentRunnerToken    string        `env:"AGENT_RUNNER_INTERNAL_TOKEN"`
	AgentEngine         string        `env:"AGENT_ENGINE" envDefault:"pi"`
	AgentNodeBin        string        `env:"AGENT_NODE_BIN"`
	AgentPiEntry        string        `env:"AGENT_PI_ENTRY"`
	AgentExtensionPath  string        `env:"AGENT_EXTENSION_PATH"`
	AgentDataDir        string        `env:"AGENT_DATA_DIR"`
	// AgentWorkspaceDir 是 Agent 共享工作区目录,空则 <数据目录>/workspace。
	AgentWorkspaceDir  string        `env:"AGENT_WORKSPACE_DIR"`
	AgentMaxConcurrent int           `env:"AGENT_MAX_CONCURRENT" envDefault:"4"`
	AgentRunTimeout    time.Duration `env:"AGENT_RUN_TIMEOUT" envDefault:"60m"`
	// AgentQuestionTimeout 是模型提问无人回答时自动跳过的等待时长,0 表示不超时。
	AgentQuestionTimeout  time.Duration `env:"AGENT_QUESTION_TIMEOUT" envDefault:"30m"`
	AgentGatewayBaseURL   string        `env:"AGENT_GATEWAY_BASE_URL"`
	AgentImageMaxCalls    int           `env:"AGENT_IMAGE_MAX_CALLS" envDefault:"3"`
	AgentImageTimeout     time.Duration `env:"AGENT_IMAGE_TOTAL_TIMEOUT" envDefault:"10m"`
	AgentPromptSuggestion bool          `env:"AGENT_PROMPT_SUGGESTION" envDefault:"true"`
	// AgentIsolation 决定 uid 隔离的启动策略:auto(默认)按探测与自检
	// 结果决定;required 在环境不支持时直接关闭 Agent;off 完全不隔离
	// (仅本地开发)。
	AgentIsolation string `env:"AGENT_ISOLATION" envDefault:"auto"`
	// AgentUIDBase 与 AgentMaxSlots 是身份池参数(详见 agent/isolation 包)。
	AgentUIDBase  int `env:"AGENT_UID_BASE" envDefault:"20000"`
	AgentMaxSlots int `env:"AGENT_MAX_SLOTS" envDefault:"4000"`
	// AgentBash 在沙箱自检通过后开放 bash;off 始终关闭。
	AgentBash               string        `env:"AGENT_BASH" envDefault:"auto"`
	AgentExecDefaultTimeout time.Duration `env:"AGENT_EXEC_TIMEOUT" envDefault:"2m"`
	AgentExecMaxTimeout     time.Duration `env:"AGENT_EXEC_MAX_TIMEOUT" envDefault:"10m"`
	// AgentNodeMaxHeapMB 是 node 的 --max-old-space-size(MB);0 表示不传。
	AgentNodeMaxHeapMB     int           `env:"AGENT_NODE_MAX_HEAP_MB" envDefault:"1024"`
	AgentMaxOutputBytes    int64         `env:"AGENT_MAX_OUTPUT_BYTES" envDefault:"67108864"`
	AgentSessionQuotaBytes int64         `env:"AGENT_SESSION_QUOTA_BYTES" envDefault:"536870912"`
	AgentQueueTimeout      time.Duration `env:"AGENT_QUEUE_TIMEOUT" envDefault:"2m"`
	// AgentQueueMax 是并发满时的排队上限;-1 关闭排队。
	AgentQueueMax int `env:"AGENT_QUEUE_MAX" envDefault:"32"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}

	cfg.CORSAllowedOrigins = sanitizeOrigins(cfg.CORSAllowedOrigins)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

const (
	// agentRunTimeoutMax 限定内置 Agent 单次 run 的时长上限。令牌已改为文件
	// 轮换 + 后台续期,run 不再受令牌 TTL 约束,上限仅作为防失控的资源保护。
	agentRunTimeoutMax = 12 * time.Hour
)

func (c Config) Validate() error {
	switch c.AppEnv {
	case "development", "test", "staging", "production":
	default:
		return fmt.Errorf("invalid APP_ENV %q", c.AppEnv)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid LOG_LEVEL %q", c.LogLevel)
	}

	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}

	if c.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("READ_HEADER_TIMEOUT must be greater than 0")
	}

	if c.RequestTimeout <= 0 {
		return fmt.Errorf("REQUEST_TIMEOUT must be greater than 0")
	}

	if c.MaxRequestBodyBytes <= 0 {
		return fmt.Errorf("MAX_REQUEST_BODY_BYTES must be greater than 0")
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be greater than 0")
	}

	if c.DBConnectTimeout <= 0 {
		return fmt.Errorf("DB_CONNECT_TIMEOUT must be greater than 0")
	}

	if c.DBMinConns < 0 {
		return fmt.Errorf("DB_MIN_CONNS must be greater than or equal to 0")
	}

	if c.DBMaxConns <= 0 {
		return fmt.Errorf("DB_MAX_CONNS must be greater than 0")
	}

	if c.DBMaxConns < c.DBMinConns {
		return fmt.Errorf("DB_MAX_CONNS must be greater than or equal to DB_MIN_CONNS")
	}

	if len(c.CORSAllowedOrigins) == 0 {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS must contain at least one origin")
	}

	if strings.TrimSpace(c.LogDir) == "" {
		return fmt.Errorf("LOG_DIR must not be empty")
	}

	if strings.TrimSpace(c.LogFilePrefix) == "" {
		return fmt.Errorf("LOG_FILE_PREFIX must not be empty")
	}

	if c.LogRetentionDays <= 0 {
		return fmt.Errorf("LOG_RETENTION_DAYS must be greater than 0")
	}

	if c.DBPort < 1 || c.DBPort > 65535 {
		return fmt.Errorf("DB_PORT must be between 1 and 65535")
	}

	if strings.TrimSpace(c.PostgresDSN) == "" {
		if strings.TrimSpace(c.DBHost) == "" {
			return fmt.Errorf("DB_HOST must not be empty")
		}
		if strings.TrimSpace(c.DBName) == "" {
			return fmt.Errorf("DB_NAME must not be empty")
		}
		if strings.TrimSpace(c.DBUser) == "" {
			return fmt.Errorf("DB_USER must not be empty")
		}
	}

	switch strings.TrimSpace(c.DBSSLMode) {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("DB_SSLMODE must be one of disable, allow, prefer, require, verify-ca, verify-full")
	}

	if c.SiteHealthInterval <= 0 {
		return fmt.Errorf("SITE_HEALTH_INTERVAL must be greater than 0")
	}

	if c.SiteHealthTimeout <= 0 {
		return fmt.Errorf("SITE_HEALTH_TIMEOUT must be greater than 0")
	}

	if c.SiteHealthWorkers <= 0 {
		return fmt.Errorf("SITE_HEALTH_WORKERS must be greater than 0")
	}

	switch c.AgentEngine {
	case "runner", "pi":
	default:
		return fmt.Errorf("AGENT_ENGINE must be one of runner, pi")
	}

	if c.AgentMaxConcurrent <= 0 {
		return fmt.Errorf("AGENT_MAX_CONCURRENT must be greater than 0")
	}

	if c.AgentQuestionTimeout < 0 {
		return fmt.Errorf("AGENT_QUESTION_TIMEOUT must not be negative")
	}
	if c.AgentRunTimeout <= 0 || c.AgentRunTimeout > agentRunTimeoutMax {
		return fmt.Errorf("AGENT_RUN_TIMEOUT must be greater than 0 and at most %s", agentRunTimeoutMax)
	}

	switch c.AgentIsolation {
	case "auto", "required", "off":
	default:
		return fmt.Errorf("AGENT_ISOLATION must be one of auto, required, off")
	}

	// 身份池范围:UIDBase 不得落入系统用户区,池上限与 isolation 包的
	// uid 越界校验保持一致(最大 ExecUID = UIDBase + 2*MaxSlots + 1)。
	if c.AgentUIDBase < 1000 {
		return fmt.Errorf("AGENT_UID_BASE must be at least 1000")
	}
	if c.AgentMaxSlots < 4 {
		return fmt.Errorf("AGENT_MAX_SLOTS must be at least 4")
	}
	if int64(c.AgentUIDBase)+2*int64(c.AgentMaxSlots)+1 > math.MaxInt32 {
		return fmt.Errorf("AGENT_UID_BASE + 2*AGENT_MAX_SLOTS exceeds the Linux uid limit")
	}
	switch c.AgentBash {
	case "auto", "off":
	default:
		return fmt.Errorf("AGENT_BASH must be one of auto, off")
	}
	if c.AgentExecDefaultTimeout <= 0 {
		return fmt.Errorf("AGENT_EXEC_TIMEOUT must be greater than 0")
	}
	if c.AgentExecMaxTimeout <= 0 {
		return fmt.Errorf("AGENT_EXEC_MAX_TIMEOUT must be greater than 0")
	}
	if c.AgentExecDefaultTimeout > c.AgentExecMaxTimeout {
		return fmt.Errorf("AGENT_EXEC_TIMEOUT must be less than or equal to AGENT_EXEC_MAX_TIMEOUT")
	}
	if c.AgentNodeMaxHeapMB < 0 {
		return fmt.Errorf("AGENT_NODE_MAX_HEAP_MB must be greater than or equal to 0")
	}
	if c.AgentMaxOutputBytes <= 0 {
		return fmt.Errorf("AGENT_MAX_OUTPUT_BYTES must be greater than 0")
	}
	if c.AgentSessionQuotaBytes <= 0 {
		return fmt.Errorf("AGENT_SESSION_QUOTA_BYTES must be greater than 0")
	}
	if c.AgentQueueTimeout <= 0 {
		return fmt.Errorf("AGENT_QUEUE_TIMEOUT must be greater than 0")
	}
	if c.AgentQueueMax != -1 && c.AgentQueueMax <= 0 {
		return fmt.Errorf("AGENT_QUEUE_MAX must be greater than 0 or -1 to disable queueing")
	}

	if c.AgentImageMaxCalls < 1 {
		return fmt.Errorf("AGENT_IMAGE_MAX_CALLS must be greater than or equal to 1")
	}
	if c.AgentImageTimeout <= 0 {
		return fmt.Errorf("AGENT_IMAGE_TOTAL_TIMEOUT must be greater than 0")
	}

	return nil
}

func sanitizeOrigins(origins []string) []string {
	sanitized := make([]string, 0, len(origins))
	for _, origin := range origins {
		trimmed := strings.TrimSpace(origin)
		if trimmed == "" {
			continue
		}

		sanitized = append(sanitized, trimmed)
	}

	return sanitized
}
