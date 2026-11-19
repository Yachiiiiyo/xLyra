package config

import (
	"strings"
	"testing"
)

func TestDeviceIdleDaysDefaultsTo180(t *testing.T) {
	t.Setenv("DEVICE_IDLE_DAYS", "")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceIdleDays != 180 {
		t.Fatalf("DEVICE_IDLE_DAYS default = %d, want 180", cfg.DeviceIdleDays)
	}
}

func TestDeviceIdleDaysAcceptsZeroAndPositive(t *testing.T) {
	t.Setenv("DEVICE_IDLE_DAYS", "0")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceIdleDays != 0 {
		t.Fatalf("DEVICE_IDLE_DAYS = %d, want 0 (unlimited)", cfg.DeviceIdleDays)
	}

	t.Setenv("DEVICE_IDLE_DAYS", "365")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceIdleDays != 365 {
		t.Fatalf("DEVICE_IDLE_DAYS = %d, want 365", cfg.DeviceIdleDays)
	}
}

func TestDeviceIdleDaysRejectsNegative(t *testing.T) {
	t.Setenv("DEVICE_IDLE_DAYS", "-1")
	t.Setenv("POSTGRES_DSN", "postgres://localhost/xlyra")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "DEVICE_IDLE_DAYS") {
		t.Fatalf("Load error = %v, want DEVICE_IDLE_DAYS validation failure", err)
	}
}
