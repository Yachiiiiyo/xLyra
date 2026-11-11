package config

import "testing"

func TestPushDefaults(t *testing.T) {
	t.Setenv("PUSH_ENABLED", "")
	t.Setenv("PUSH_DONE_MIN_SECONDS", "")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PushEnabled {
		t.Fatal("PUSH_ENABLED default = false, want true")
	}
	if cfg.PushDoneMinSeconds != 20 {
		t.Fatalf("PUSH_DONE_MIN_SECONDS default = %d, want 20", cfg.PushDoneMinSeconds)
	}
}

func TestPushDoneMinSecondsAcceptsZero(t *testing.T) {
	t.Setenv("PUSH_ENABLED", "false")
	t.Setenv("PUSH_DONE_MIN_SECONDS", "0")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PushEnabled {
		t.Fatal("PUSH_ENABLED = true, want false")
	}
	if cfg.PushDoneMinSeconds != 0 {
		t.Fatalf("PUSH_DONE_MIN_SECONDS = %d, want 0", cfg.PushDoneMinSeconds)
	}
}

func TestPushDoneMinSecondsFallsBackOnInvalid(t *testing.T) {
	t.Setenv("PUSH_DONE_MIN_SECONDS", "soon")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PushDoneMinSeconds != 20 {
		t.Fatalf("PUSH_DONE_MIN_SECONDS = %d, want default 20", cfg.PushDoneMinSeconds)
	}
}
