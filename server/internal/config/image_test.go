package config

import (
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

func TestAgentImageDefaultsAndValidation(t *testing.T) {
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if cfg.AgentImageMaxCalls != 3 || cfg.AgentImageTimeout != 10*time.Minute {
		t.Fatalf("image defaults: max=%d timeout=%s", cfg.AgentImageMaxCalls, cfg.AgentImageTimeout)
	}
	for name, mutate := range map[string]func(*Config){
		"AGENT_IMAGE_MAX_CALLS":     func(c *Config) { c.AgentImageMaxCalls = 0 },
		"AGENT_IMAGE_TOTAL_TIMEOUT": func(c *Config) { c.AgentImageTimeout = 0 },
	} {
		cfg := validConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: error=%v", name, err)
		}
	}
}
