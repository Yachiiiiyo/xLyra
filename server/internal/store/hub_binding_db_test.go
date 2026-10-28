package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/config"
)

func openHubBindingStore(t *testing.T) *Store {
	t.Helper()
	cfg := config.Config{
		AppEnv:           "test",
		DBConnectTimeout: 2 * time.Second,
		DBMaxConns:       4,
		PostgresDSN:      strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		DBHost:           hubBindingEnv("DB_HOST", "127.0.0.1"),
		DBPort:           5432,
		DBName:           hubBindingEnv("DB_NAME", "xlyra"),
		DBUser:           hubBindingEnv("DB_USER", "postgres"),
		DBPassword:       hubBindingEnv("DB_PASSWORD", "postgres"),
		DBSSLMode:        hubBindingEnv("DB_SSLMODE", "disable"),
	}
	if value := strings.TrimSpace(os.Getenv("DB_PORT")); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			t.Skipf("invalid DB_PORT %q", value)
		}
		cfg.DBPort = port
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := Open(ctx, cfg)
	if err != nil {
		t.Skipf("dev PostgreSQL unavailable: %v", err)
	}
	schema := "xlyra_hub_binding_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.DB().Exec(fmt.Sprintf("CREATE SCHEMA %q", schema)).Error; err != nil {
		base.Close()
		t.Skipf("cannot create a test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = base.DB().Exec(fmt.Sprintf("DROP SCHEMA %q CASCADE", schema)).Error
		base.Close()
	})
	parsed, err := url.Parse(cfg.DatabaseDSN())
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cfg.PostgresDSN = parsed.String()
	var migrateErr error
	for attempt := 0; attempt < 5; attempt++ {
		if migrateErr = EnsureDatabaseInitialized(ctx, cfg); migrateErr == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if migrateErr != nil {
		t.Fatalf("migrate the test schema: %v", migrateErr)
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open the test schema: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func hubBindingEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
