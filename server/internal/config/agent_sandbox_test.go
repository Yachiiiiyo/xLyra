package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAgentSandboxSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "bash 非法", mutate: func(c *Config) { c.AgentBash = "on" }, want: "AGENT_BASH"},
		{name: "默认超时为零", mutate: func(c *Config) { c.AgentExecDefaultTimeout = 0 }, want: "AGENT_EXEC_TIMEOUT"},
		{name: "最大超时为负", mutate: func(c *Config) { c.AgentExecMaxTimeout = -time.Second }, want: "AGENT_EXEC_MAX_TIMEOUT"},
		{name: "默认超时超过上限", mutate: func(c *Config) { c.AgentExecDefaultTimeout = 11 * time.Minute }, want: "AGENT_EXEC_TIMEOUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want %s", err, tc.want)
			}
		})
	}
	cfg := validConfig()
	cfg.AgentBash = "off"
	cfg.AgentExecDefaultTimeout = cfg.AgentExecMaxTimeout
	if err := cfg.Validate(); err != nil {
		t.Fatalf("合法沙箱配置未通过: %v", err)
	}
}
