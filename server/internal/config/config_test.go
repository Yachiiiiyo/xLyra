package config

import (
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

func TestValidateRejectsInvalidLogLevel(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.LogLevel = "verbose"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected LOG_LEVEL validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidConnectionPoolBounds(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.DBMinConns = 5
	cfg.DBMaxConns = 4

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "DB_MAX_CONNS") {
		t.Fatalf("expected DB_MAX_CONNS validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidDatabaseSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"DB_PORT":    func(cfg *Config) { cfg.DBPort = 70000 },
		"DB_HOST":    func(cfg *Config) { cfg.DBHost = "" },
		"DB_NAME":    func(cfg *Config) { cfg.DBName = "" },
		"DB_USER":    func(cfg *Config) { cfg.DBUser = "" },
		"DB_SSLMODE": func(cfg *Config) { cfg.DBSSLMode = "invalid" },
	}

	for name, mutate := range cases {
		cfg := validConfig()
		cfg.PostgresDSN = ""
		mutate(&cfg)

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s validation error, got %v", name, err)
		}
	}
}

func TestDatabaseDSNFromParts(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.PostgresDSN = ""
	cfg.DBHost = "postgres"
	cfg.DBPort = 5432
	cfg.DBName = "xlyra"
	cfg.DBUser = "postgres"
	cfg.DBPassword = "secret"
	cfg.DBSSLMode = "disable"

	if got := cfg.DatabaseDSN(); got != "postgres://postgres:secret@postgres:5432/xlyra?sslmode=disable" {
		t.Fatalf("unexpected database dsn: %s", got)
	}
}

func TestDatabaseDSNUsesPostgresDSNAndExtractsDatabaseName(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.PostgresDSN = " postgres://user:pass@localhost:5432/xlyra%20prod?sslmode=require "

	if got := cfg.DatabaseDSN(); got != "postgres://user:pass@localhost:5432/xlyra%20prod?sslmode=require" {
		t.Fatalf("database dsn = %q", got)
	}
	if got := cfg.DatabaseName(); got != "xlyra prod" {
		t.Fatalf("database name = %q, want xlyra prod", got)
	}
	maintenance, err := cfg.MaintenanceDatabaseDSN()
	if err != nil {
		t.Fatalf("maintenance dsn: %v", err)
	}
	if maintenance != "postgres://user:pass@localhost:5432/postgres?sslmode=require" {
		t.Fatalf("maintenance dsn = %q", maintenance)
	}
}

func TestMaintenanceDatabaseDSNRejectsInvalidPostgresDSN(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.PostgresDSN = "http://localhost/xlyra"

	if _, err := cfg.MaintenanceDatabaseDSN(); err == nil || !strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("expected invalid scheme error, got %v", err)
	}
}

func TestDatabaseHelpersUsePartsForMaintenanceAndIPv6(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.PostgresDSN = ""
	cfg.DBHost = "2001:db8::1"
	cfg.DBUser = "postgres"
	cfg.DBPassword = "secret"
	cfg.DBSSLMode = "disable"

	if got := cfg.DatabaseName(); got != "xlyra" {
		t.Fatalf("database name = %q", got)
	}
	if got := cfg.DatabaseDSN(); got != "postgres://postgres:secret@[2001:db8::1]:5432/xlyra?sslmode=disable" {
		t.Fatalf("database dsn = %q", got)
	}
	maintenance, err := cfg.MaintenanceDatabaseDSN()
	if err != nil {
		t.Fatalf("maintenance dsn: %v", err)
	}
	if maintenance != "postgres://postgres:secret@[2001:db8::1]:5432/postgres?sslmode=disable" {
		t.Fatalf("maintenance dsn = %q", maintenance)
	}
}

func TestValidateRejectsInvalidLogFileSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"LOG_DIR":            func(cfg *Config) { cfg.LogDir = " " },
		"LOG_FILE_PREFIX":    func(cfg *Config) { cfg.LogFilePrefix = "" },
		"LOG_RETENTION_DAYS": func(cfg *Config) { cfg.LogRetentionDays = 0 },
	}

	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s validation error, got %v", name, err)
		}
	}
}

func TestValidateRejectsInvalidSiteHealthSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"SITE_HEALTH_INTERVAL": func(cfg *Config) { cfg.SiteHealthInterval = 0 },
		"SITE_HEALTH_TIMEOUT":  func(cfg *Config) { cfg.SiteHealthTimeout = 0 },
		"SITE_HEALTH_WORKERS":  func(cfg *Config) { cfg.SiteHealthWorkers = 0 },
	}

	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s validation error, got %v", name, err)
		}
	}
}

func TestValidateRejectsInvalidCoreSettings(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"APP_ENV":                func(cfg *Config) { cfg.AppEnv = "local" },
		"HTTP_PORT":              func(cfg *Config) { cfg.HTTPPort = 0 },
		"READ_HEADER_TIMEOUT":    func(cfg *Config) { cfg.ReadHeaderTimeout = 0 },
		"REQUEST_TIMEOUT":        func(cfg *Config) { cfg.RequestTimeout = 0 },
		"MAX_REQUEST_BODY_BYTES": func(cfg *Config) { cfg.MaxRequestBodyBytes = 0 },
		"SHUTDOWN_TIMEOUT":       func(cfg *Config) { cfg.ShutdownTimeout = 0 },
		"DB_CONNECT_TIMEOUT":     func(cfg *Config) { cfg.DBConnectTimeout = 0 },
		"DB_MIN_CONNS":           func(cfg *Config) { cfg.DBMinConns = -1 },
		"CORS_ALLOWED_ORIGINS": func(cfg *Config) {
			cfg.CORSAllowedOrigins = nil
		},
	}

	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("expected %s validation error, got %v", name, err)
		}
	}
}

func TestLoadUsesEnvironmentAndSanitizesOrigins(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("HTTP_PORT", "5901")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost:5432/xlyra?sslmode=disable")
	t.Setenv("CORS_ALLOWED_ORIGINS", " http://localhost:5173 , ,https://admin.example.com ")
	t.Setenv("SITE_HEALTH_INTERVAL", "5m")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AppEnv != "test" || cfg.HTTPPort != 5901 {
		t.Fatalf("unexpected env config: %#v", cfg)
	}
	if cfg.MaxRequestBodyBytes != 256*1024*1024 {
		t.Fatalf("max request body bytes = %d, want %d", cfg.MaxRequestBodyBytes, 256*1024*1024)
	}
	if len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[1] != "https://admin.example.com" {
		t.Fatalf("origins were not sanitized: %#v", cfg.CORSAllowedOrigins)
	}
}

func TestValidateRejectsInvalidAgentEngine(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.AgentEngine = "codex"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "AGENT_ENGINE") {
		t.Fatalf("expected AGENT_ENGINE validation error, got %v", err)
	}
}

func TestValidateRejectsAgentRunTimeoutOutsideBounds(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"zero":     func(cfg *Config) { cfg.AgentRunTimeout = 0 },
		"negative": func(cfg *Config) { cfg.AgentRunTimeout = -time.Minute },
		"over max": func(cfg *Config) { cfg.AgentRunTimeout = 13 * time.Hour },
	}

	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)

		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "AGENT_RUN_TIMEOUT") {
			t.Fatalf("%s: expected AGENT_RUN_TIMEOUT validation error, got %v", name, err)
		}
	}
}

func TestValidateAcceptsAgentRunTimeoutAtLimit(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.AgentRunTimeout = 12 * time.Hour

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected 12h run timeout to validate, got %v", err)
	}
}

func TestLoadAppliesAgentDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@localhost:5432/xlyra?sslmode=disable")
	t.Setenv("AGENT_ENGINE", "")
	t.Setenv("AGENT_NODE_BIN", "")
	t.Setenv("AGENT_PI_ENTRY", "")
	t.Setenv("AGENT_EXTENSION_PATH", "")
	t.Setenv("AGENT_DATA_DIR", "")
	t.Setenv("AGENT_MAX_CONCURRENT", "")
	t.Setenv("AGENT_RUN_TIMEOUT", "")
	t.Setenv("AGENT_GATEWAY_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AgentEngine != "pi" {
		t.Fatalf("AGENT_ENGINE default = %q, want pi", cfg.AgentEngine)
	}
	if cfg.AgentNodeBin != "" || cfg.AgentPiEntry != "" || cfg.AgentExtensionPath != "" || cfg.AgentDataDir != "" || cfg.AgentGatewayBaseURL != "" {
		t.Fatalf("unexpected agent path defaults: %#v", cfg)
	}
	if cfg.AgentMaxConcurrent != 4 {
		t.Fatalf("AGENT_MAX_CONCURRENT default = %d, want 4", cfg.AgentMaxConcurrent)
	}
	if cfg.AgentRunTimeout != 60*time.Minute {
		t.Fatalf("AGENT_RUN_TIMEOUT default = %s, want 60m", cfg.AgentRunTimeout)
	}
}

func TestSanitizeOriginsDropsBlankValues(t *testing.T) {
	t.Parallel()

	origins := sanitizeOrigins([]string{" http://localhost:5173 ", "", "  ", "https://admin.example.com"})
	if len(origins) != 2 {
		t.Fatalf("expected 2 origins after sanitize, got %d", len(origins))
	}

	if origins[0] != "http://localhost:5173" {
		t.Fatalf("unexpected first origin %q", origins[0])
	}
}

func validConfig() Config {
	return Config{
		AppEnv:                  "development",
		AppName:                 "xlyra-server",
		HTTPHost:                "0.0.0.0",
		HTTPPort:                5801,
		LogLevel:                "info",
		LogDir:                  "logs",
		LogFilePrefix:           "xlyra-server",
		LogRetentionDays:        14,
		ReadHeaderTimeout:       5 * time.Second,
		RequestTimeout:          30 * time.Second,
		MaxRequestBodyBytes:     33554432,
		ShutdownTimeout:         10 * time.Second,
		DBConnectTimeout:        30 * time.Second,
		DBMinConns:              2,
		DBMaxConns:              10,
		CORSAllowedOrigins:      []string{"http://localhost:5173"},
		DBHost:                  "postgres",
		DBPort:                  5432,
		DBName:                  "xlyra",
		DBUser:                  "postgres",
		DBPassword:              "postgres",
		DBSSLMode:               "disable",
		SiteHealthInterval:      15 * time.Minute,
		SiteHealthTimeout:       10 * time.Second,
		SiteHealthWorkers:       4,
		AgentEngine:             "runner",
		AgentMaxConcurrent:      4,
		AgentRunTimeout:         60 * time.Minute,
		AgentIsolation:          "auto",
		AgentUIDBase:            20000,
		AgentMaxSlots:           4000,
		AgentBash:               "auto",
		AgentExecDefaultTimeout: 2 * time.Minute,
		AgentExecMaxTimeout:     10 * time.Minute,
		AgentNodeMaxHeapMB:      1024,
		AgentMaxOutputBytes:     64 << 20,
		AgentSessionQuotaBytes:  512 << 20,
		AgentQueueTimeout:       2 * time.Minute,
		AgentQueueMax:           32,
		AgentImageMaxCalls:      3,
		AgentImageTimeout:       10 * time.Minute,
	}
}

// TestAgentIsolationDefaults:空环境下解析应得到文档默认值,且默认值能通过校验。
func TestAgentIsolationDefaults(t *testing.T) {
	t.Parallel()

	var cfg Config
	// 用空环境解析,避免宿主机环境变量影响默认值断言
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: map[string]string{}}); err != nil {
		t.Fatalf("parse defaults: %v", err)
	}
	want := Config{
		AgentIsolation:          "auto",
		AgentUIDBase:            20000,
		AgentMaxSlots:           4000,
		AgentNodeMaxHeapMB:      1024,
		AgentMaxOutputBytes:     64 << 20,
		AgentSessionQuotaBytes:  512 << 20,
		AgentQueueTimeout:       2 * time.Minute,
		AgentQueueMax:           32,
		AgentBash:               "auto",
		AgentExecDefaultTimeout: 2 * time.Minute,
		AgentExecMaxTimeout:     10 * time.Minute,
	}
	if cfg.AgentBash != want.AgentBash || cfg.AgentExecDefaultTimeout != want.AgentExecDefaultTimeout ||
		cfg.AgentExecMaxTimeout != want.AgentExecMaxTimeout {
		t.Fatalf("沙箱默认值不符: %+v", cfg)
	}
	if cfg.AgentIsolation != want.AgentIsolation || cfg.AgentUIDBase != want.AgentUIDBase ||
		cfg.AgentMaxSlots != want.AgentMaxSlots || cfg.AgentNodeMaxHeapMB != want.AgentNodeMaxHeapMB ||
		cfg.AgentMaxOutputBytes != want.AgentMaxOutputBytes ||
		cfg.AgentSessionQuotaBytes != want.AgentSessionQuotaBytes ||
		cfg.AgentQueueTimeout != want.AgentQueueTimeout || cfg.AgentQueueMax != want.AgentQueueMax {
		t.Fatalf("隔离相关默认值不符:\n got %+v\nwant %+v", cfg, want)
	}
	// 默认值本身必须合法(把其余必填字段补齐后校验)
	base := validConfig()
	base.AgentIsolation = ""
	base.AgentUIDBase = 0
	base.AgentMaxSlots = 0
	base.AgentNodeMaxHeapMB = 0
	base.AgentMaxOutputBytes = 0
	base.AgentSessionQuotaBytes = 0
	base.AgentQueueTimeout = 0
	base.AgentQueueMax = 0
	base.AgentBash = cfg.AgentBash
	base.AgentExecDefaultTimeout = cfg.AgentExecDefaultTimeout
	base.AgentExecMaxTimeout = cfg.AgentExecMaxTimeout
	base.AgentIsolation = cfg.AgentIsolation
	base.AgentUIDBase = cfg.AgentUIDBase
	base.AgentMaxSlots = cfg.AgentMaxSlots
	base.AgentNodeMaxHeapMB = cfg.AgentNodeMaxHeapMB
	base.AgentMaxOutputBytes = cfg.AgentMaxOutputBytes
	base.AgentSessionQuotaBytes = cfg.AgentSessionQuotaBytes
	base.AgentQueueTimeout = cfg.AgentQueueTimeout
	base.AgentQueueMax = cfg.AgentQueueMax
	if err := base.Validate(); err != nil {
		t.Fatalf("默认值未通过校验: %v", err)
	}
	// QueueMax=-1(关闭排队)是显式合法值
	base.AgentQueueMax = -1
	if err := base.Validate(); err != nil {
		t.Fatalf("AGENT_QUEUE_MAX=-1 应合法: %v", err)
	}
}

// TestValidateRejectsInvalidAgentIsolation:非法 AGENT_ISOLATION 值。
func TestValidateRejectsInvalidAgentIsolation(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "On", "AUTO", "force", "auto "} {
		cfg := validConfig()
		cfg.AgentIsolation = mode
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "AGENT_ISOLATION") {
			t.Fatalf("AGENT_ISOLATION=%q 应报错,得到 %v", mode, err)
		}
	}
}

// TestValidateRejectsInvalidAgentIsolationRanges:身份池与限额的范围校验。
func TestValidateRejectsInvalidAgentIsolationRanges(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*Config){
		"AGENT_UID_BASE":  func(c *Config) { c.AgentUIDBase = 999 },
		"AGENT_MAX_SLOTS": func(c *Config) { c.AgentMaxSlots = 3 },
		"uid 池上限": func(c *Config) {
			c.AgentUIDBase = 2147483000
			c.AgentMaxSlots = 4000
		},
		"AGENT_NODE_MAX_HEAP_MB":    func(c *Config) { c.AgentNodeMaxHeapMB = -1 },
		"AGENT_MAX_OUTPUT_BYTES":    func(c *Config) { c.AgentMaxOutputBytes = 0 },
		"AGENT_SESSION_QUOTA_BYTES": func(c *Config) { c.AgentSessionQuotaBytes = -1 },
		"AGENT_QUEUE_TIMEOUT":       func(c *Config) { c.AgentQueueTimeout = 0 },
		"AGENT_QUEUE_MAX":           func(c *Config) { c.AgentQueueMax = 0 },
	}
	for name, mutate := range cases {
		cfg := validConfig()
		mutate(&cfg)
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("%s 应报错", name)
		}
		if name != "uid 池上限" && !strings.Contains(err.Error(), name) {
			t.Fatalf("%s 报错应带字段名,得到 %v", name, err)
		}
	}
	// uid 池上限的报错信息说明越界即可
	cfg := validConfig()
	cfg.AgentUIDBase = 2147483000
	cfg.AgentMaxSlots = 4000
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "uid limit") {
		t.Fatalf("uid 池越界应报错,得到 %v", err)
	}
}

func TestValidateAgentQuestionTimeout(t *testing.T) {
	cfg := validConfig()
	cfg.AgentQuestionTimeout = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("0 表示不超时: %v", err)
	}
	cfg.AgentQuestionTimeout = -time.Minute
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "AGENT_QUESTION_TIMEOUT") {
		t.Fatalf("负数应报错: %v", err)
	}
}
