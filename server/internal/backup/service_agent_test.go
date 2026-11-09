package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/config"
	"xlyra/server/internal/store"
)

func TestManifestSectionsSerialization(t *testing.T) {
	value := validAgentSectionManifest()
	value.Sections = nil
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest without sections: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal manifest fields: %v", err)
	}
	if _, ok := fields["sections"]; ok {
		t.Fatalf("empty sections should be omitted: %s", encoded)
	}

	value.Sections = []string{sectionAgentCapabilities}
	encoded, err = json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest with sections: %v", err)
	}
	var decoded manifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal manifest with sections: %v", err)
	}
	if len(decoded.Sections) != 1 || decoded.Sections[0] != sectionAgentCapabilities {
		t.Fatalf("decoded sections = %#v, want [%q]", decoded.Sections, sectionAgentCapabilities)
	}
}

func TestValidateManifestRejectsUnknownSection(t *testing.T) {
	// v3 旧备份的 sections 只允许空或 agent_capabilities,未知段名沿用原有错误。
	value := validAgentSectionManifest()
	value.FormatVersion = 3
	value.Tables = append([]string(nil), legacyV3Tables...)
	value.Sections = []string{sectionAgentCapabilities}
	if err := validateManifest(value); err != nil {
		t.Fatalf("validate known section: %v", err)
	}

	value.Sections = append(value.Sections, "future_capabilities")
	err := validateManifest(value)
	if err == nil || err.Error() != `unsupported backup section "future_capabilities"` {
		t.Fatalf("validate unknown section error = %v", err)
	}
}

// allExportedSections 是 v4 全量导出包含的类别清单。
func allExportedSections() []string {
	return []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionPlugins,
		sectionAgentConfig,
		sectionAgentCapabilities,
	}
}

func validAgentSectionManifest() manifest {
	return manifest{
		FormatVersion: currentFormatVersion,
		App:           backupAppName,
		Payload:       backupPayload,
		Sections:      allExportedSections(),
		Tables:        TablesOf(allExportedSections()),
	}
}

func TestServiceAgentCapabilitiesExportImportRoundTrip(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()

	sourceFiles := map[string]string{
		"skills/demo/SKILL.md":           "技能正文",
		"skills/demo/resources/guide.md": "资源文件",
		"AGENTS.md":                      "全局指令",
		"config.json":                    `{"skills":true}`,
	}
	for name, content := range sourceFiles {
		writeAgentTestFile(t, sourceRoot, name, content)
	}
	writeAgentTestFile(t, targetRoot, "skills/stale/SKILL.md", "旧技能")

	exporter := NewService(st, confFile, "master-key").WithAgentRoot(sourceRoot)
	encrypted := exportAgentBackupTestArchive(t, exporter)
	importer := NewService(st, confFile, "master-key").WithAgentRoot(targetRoot)
	var events []ProgressEvent
	summary, err := importer.Import(context.Background(), "passphrase", encrypted, ImportOptions{}, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("import agent capabilities backup: %v", err)
	}
	if summary.AgentFiles != len(sourceFiles) {
		t.Fatalf("restored agent files = %d, want %d", summary.AgentFiles, len(sourceFiles))
	}
	for name, want := range sourceFiles {
		assertAgentBackupFileContent(t, targetRoot, name, want)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, "skills", "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale skills should be replaced, stat error = %v", err)
	}

	var started, completed bool
	for _, event := range events {
		if event.Step != "agent" {
			continue
		}
		switch event.Status {
		case "in_progress":
			started = true
		case "complete":
			completed = true
		}
	}
	if !started || !completed {
		t.Fatalf("agent progress events missing start/complete: %#v", events)
	}
}

func TestServiceLegacyBackupLeavesAgentCapabilitiesUntouched(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	targetRoot := t.TempDir()
	existingFiles := map[string]string{
		"skills/keep/SKILL.md": "保留技能",
		"AGENTS.md":            "保留指令",
		"config.json":          `{"keep":true}`,
	}
	for name, content := range existingFiles {
		writeAgentTestFile(t, targetRoot, name, content)
	}

	exporter := NewService(st, confFile, "master-key")
	encrypted := exportAgentBackupTestArchive(t, exporter)
	importer := NewService(st, confFile, "master-key").WithAgentRoot(targetRoot)
	var events []ProgressEvent
	summary, err := importer.Import(context.Background(), "passphrase", encrypted, ImportOptions{}, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("import legacy backup: %v", err)
	}
	if summary.AgentFiles != 0 {
		t.Fatalf("legacy restore agent files = %d, want 0", summary.AgentFiles)
	}
	for name, want := range existingFiles {
		assertAgentBackupFileContent(t, targetRoot, name, want)
	}
	for _, event := range events {
		if event.Step == "agent" {
			t.Fatalf("legacy restore should not emit agent progress: %#v", events)
		}
	}
}

func exportAgentBackupTestArchive(t *testing.T, service Service) []byte {
	t.Helper()
	archivePath, _, err := service.Export(context.Background(), "passphrase")
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}
	return encrypted
}

func assertAgentBackupFileContent(t *testing.T, root string, name string, want string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read agent file %s: %v", name, err)
	}
	if string(content) != want {
		t.Fatalf("agent file %s = %q, want %q", name, content, want)
	}
}

// openAgentBackupTestStore 在开发 PostgreSQL 中建立一次性 schema，避免清空
// 真实业务表。连接或建 schema 不可用时跳过，与现有 PostgreSQL 测试一致。
func openAgentBackupTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := config.Config{
		AppEnv:           "test",
		DBConnectTimeout: 2 * time.Second,
		DBMinConns:       0,
		DBMaxConns:       4,
		PostgresDSN:      strings.TrimSpace(os.Getenv("POSTGRES_DSN")),
		DBHost:           agentBackupTestEnv("DB_HOST", "127.0.0.1"),
		DBPort:           5432,
		DBName:           agentBackupTestEnv("DB_NAME", "xlyra"),
		DBUser:           agentBackupTestEnv("DB_USER", "postgres"),
		DBPassword:       agentBackupTestEnv("DB_PASSWORD", "postgres"),
		DBSSLMode:        agentBackupTestEnv("DB_SSLMODE", "disable"),
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
	base, err := store.Open(ctx, cfg)
	if err != nil {
		t.Skipf("dev PostgreSQL unavailable: %v", err)
	}
	t.Cleanup(base.Close)

	schema := "xlyra_agent_backup_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := base.DB().Exec(fmt.Sprintf("CREATE SCHEMA %q", schema)).Error; err != nil {
		t.Skipf("cannot create agent backup test schema: %v", err)
	}
	t.Cleanup(func() {
		_ = base.DB().Exec(fmt.Sprintf("DROP SCHEMA %q CASCADE", schema)).Error
	})

	parsed, err := url.Parse(cfg.DatabaseDSN())
	if err != nil {
		t.Fatalf("parse test database DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cfg.PostgresDSN = parsed.String()
	if err := store.EnsureDatabaseInitialized(ctx, cfg); err != nil {
		t.Fatalf("initialize agent backup test schema: %v", err)
	}
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open agent backup test schema: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func agentBackupTestEnv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
