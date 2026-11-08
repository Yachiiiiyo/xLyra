package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// encryptBackupFixture 把明文备份 zip 加密成 Import 可用的字节流。
func encryptBackupFixture(t *testing.T, archivePath string) []byte {
	t.Helper()

	plain, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open plain archive: %v", err)
	}
	defer plain.Close()
	encryptedPath := filepath.Join(t.TempDir(), "encrypted.zip.xlyra")
	encryptedFile, err := os.Create(encryptedPath)
	if err != nil {
		t.Fatalf("create encrypted archive: %v", err)
	}
	if err := encryptStream("passphrase", plain, encryptedFile); err != nil {
		t.Fatalf("encrypt archive: %v", err)
	}
	if err := encryptedFile.Close(); err != nil {
		t.Fatalf("close encrypted archive: %v", err)
	}
	encrypted, err := os.ReadFile(encryptedPath)
	if err != nil {
		t.Fatalf("read encrypted archive: %v", err)
	}
	return encrypted
}

// seedRestoreSectionStore 在测试库里种入:一个站点、一个 API 密钥、一条密钥
// 站点权限、一条 request_log 与一条 usage_record(供导出/恢复往返使用)。
func seedRestoreSectionStore(t *testing.T, db *gorm.DB) (siteID uuid.UUID, apiKeyID uuid.UUID) {
	t.Helper()

	siteID = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	apiKeyID = uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	requestLogID := uuid.MustParse("00000000-0000-0000-0000-0000000000a3")

	if err := db.Exec(`INSERT INTO sites (id, name, slug, site_type, base_url, status, enabled, routing_priority)
		VALUES (?, 'seed-site', 'seed-site', 'openai', 'https://seed.example', 'active', true, 1)`, siteID).Error; err != nil {
		t.Fatalf("seed site: %v", err)
	}
	if err := db.Exec(`INSERT INTO api_keys (id, name, key_prefix, key_hash, masked_key, scope, status)
		VALUES (?, 'seed-key', 'sk-seed', 'hash-seed', 'sk-***-seed', 'all', 'active')`, apiKeyID).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.Exec(`INSERT INTO api_key_site_permissions (id, api_key_id, site_id, enabled)
		VALUES (?, ?, ?, true)`, uuid.New(), apiKeyID, siteID).Error; err != nil {
		t.Fatalf("seed api key site permission: %v", err)
	}
	if err := db.Exec(`INSERT INTO request_logs (id, request_id, api_key_id, site_id, endpoint, status_code, success)
		VALUES (?, 'req-seed', ?, ?, '/v1/chat/completions', 200, true)`, requestLogID, apiKeyID, siteID).Error; err != nil {
		t.Fatalf("seed request log: %v", err)
	}
	if err := db.Exec(`INSERT INTO usage_records (id, request_log_id, api_key_id, site_id)
		VALUES (?, ?, ?, ?)`, uuid.New(), requestLogID, apiKeyID, siteID).Error; err != nil {
		t.Fatalf("seed usage record: %v", err)
	}
	return siteID, apiKeyID
}

// exportAndImport 走完整的导出→导入往返,返回导入摘要。
func exportAndImport(t *testing.T, service Service, ids []string) ImportSummary {
	t.Helper()

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", ids)
	if err != nil {
		t.Fatalf("ExportSections %v: %v", ids, err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}
	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return summary
}

// TestImportRestoreFullRoundTripSummary 全量导出再导入:摘要的 Sections 等于
// 导出类别、ClearedSections 为空、Tables 为全部备份表数。
func TestImportRestoreFullRoundTripSummary(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	service := NewService(st, confFile, "master-key")

	// 默认导出类别含 plugins;注册表顺序为 gateway_logs 之后、agent_config 之前。
	wantSections := []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionPlugins,
		sectionAgentConfig,
	}
	summary := exportAndImport(t, service, wantSections)

	if !slices.Equal(summary.Sections, wantSections) {
		t.Fatalf("sections = %#v, want %#v", summary.Sections, wantSections)
	}
	if len(summary.ClearedSections) != 0 {
		t.Fatalf("cleared sections = %#v, want empty", summary.ClearedSections)
	}
	if summary.Tables != 29 {
		t.Fatalf("tables = %d, want 29", summary.Tables)
	}
}

// TestImportRestoreGatewaySitesOnly 只恢复 gateway_sites:站点恢复为导出内容,
// 外键引用的密钥权限/日志/用量与两张用量汇总表被清空,api_keys 保留,
// ClearedSections 指明 gateway_keys(partial)与 gateway_logs(全部)。
func TestImportRestoreGatewaySitesOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	siteID, _ := seedRestoreSectionStore(t, st.DB())
	service := NewService(st, confFile, "master-key")

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", []string{sectionGatewaySites})
	if err != nil {
		t.Fatalf("ExportSections gateway_sites: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	// 导出后修改站点数据,验证导入会替换回导出时的内容。
	if err := st.DB().Exec("UPDATE sites SET name = 'modified-site' WHERE id = ?", siteID).Error; err != nil {
		t.Fatalf("modify site: %v", err)
	}

	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
	if err != nil {
		t.Fatalf("Import gateway_sites backup: %v", err)
	}

	db := st.DB()
	var siteName string
	if err := db.Raw("SELECT name FROM sites WHERE id = ?", siteID).Scan(&siteName).Error; err != nil || siteName != "seed-site" {
		t.Fatalf("restored site name = %q err=%v, want seed-site", siteName, err)
	}
	for _, table := range []string{
		"api_key_site_permissions",
		"request_logs",
		"usage_records",
		"request_usage_daily_summaries",
		"request_usage_summary_days",
	} {
		if got := countTableRows(t, db, table); got != 0 {
			t.Fatalf("%s rows = %d, want 0", table, got)
		}
	}
	if got := countTableRows(t, db, "api_keys"); got != 1 {
		t.Fatalf("api_keys rows = %d, want 1 (retained)", got)
	}

	want := []SectionImpact{
		{ID: sectionGatewayKeys, Partial: true},
		{ID: sectionGatewayLogs, Partial: false},
	}
	if !slices.Equal(summary.ClearedSections, want) {
		t.Fatalf("cleared sections = %#v, want %#v", summary.ClearedSections, want)
	}
}

// TestImportRestoreSettingsOnly 只恢复 settings:数据库不动,network/agent 键
// 保持导入前的值,settings 拥有的键被替换,且不发数据库相关的进度事件。
func TestImportRestoreSettingsOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	seedRestoreSectionStore(t, st.DB())
	service := NewService(st, confFile, "master-key")

	if err := confFile.Set("appearance", map[string]any{"theme": "dark"}); err != nil {
		t.Fatalf("set appearance: %v", err)
	}
	if err := confFile.Set("network", map[string]any{"listen": ":8080"}); err != nil {
		t.Fatalf("set network: %v", err)
	}
	if err := confFile.Set("agent", map[string]any{"model": "export-model"}); err != nil {
		t.Fatalf("set agent: %v", err)
	}

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", []string{sectionSettings})
	if err != nil {
		t.Fatalf("ExportSections settings: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	before := countAllBackupTableRows(t, st.DB())
	if err := confFile.Set("network", map[string]any{"listen": ":9999"}); err != nil {
		t.Fatalf("change network: %v", err)
	}
	if err := confFile.Set("agent", map[string]any{"model": "changed-model"}); err != nil {
		t.Fatalf("change agent: %v", err)
	}
	if err := confFile.Set("appearance", map[string]any{"theme": "light"}); err != nil {
		t.Fatalf("change appearance: %v", err)
	}

	var events []ProgressEvent
	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{}, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Import settings backup: %v", err)
	}

	if after := countAllBackupTableRows(t, st.DB()); !mapsEqualInt64(before, after) {
		t.Fatalf("database rows changed: before=%#v after=%#v", before, after)
	}
	if network, _ := confFile.Data()["network"].(map[string]any); network["listen"] != ":9999" {
		t.Fatalf("network = %#v, want to keep pre-import value", confFile.Data()["network"])
	}
	if agent, _ := confFile.Data()["agent"].(map[string]any); agent["model"] != "changed-model" {
		t.Fatalf("agent = %#v, want to keep pre-import value", confFile.Data()["agent"])
	}
	if appearance, _ := confFile.Data()["appearance"].(map[string]any); appearance["theme"] != "dark" {
		t.Fatalf("appearance = %#v, want restored from backup", confFile.Data()["appearance"])
	}
	if summary.Tables != 0 {
		t.Fatalf("tables = %d, want 0", summary.Tables)
	}
	for _, event := range events {
		if event.Step == "import" {
			t.Fatalf("settings-only restore should not emit database progress: %#v", events)
		}
	}
}

// TestImportRestoreAgentConfigOnly 只恢复 agent_config:agent 子树被替换,
// 但当前配置里的 runner_base_url / runner_internal_token 保持原值。
func TestImportRestoreAgentConfigOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	service := NewService(st, confFile, "master-key")

	if err := confFile.Set("agent", map[string]any{
		"model":                 "export-model",
		"temperature":           0.5,
		"runner_base_url":       "https://old-runner",
		"runner_internal_token": "old-token",
	}); err != nil {
		t.Fatalf("set agent config: %v", err)
	}

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", []string{sectionAgentConfig})
	if err != nil {
		t.Fatalf("ExportSections agent_config: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	if err := confFile.Set("agent", map[string]any{
		"model":                 "changed-model",
		"runner_base_url":       "https://current-runner",
		"runner_internal_token": "current-token",
	}); err != nil {
		t.Fatalf("change agent config: %v", err)
	}

	if _, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{}); err != nil {
		t.Fatalf("Import agent_config backup: %v", err)
	}

	agent, _ := confFile.Data()["agent"].(map[string]any)
	if agent["model"] != "export-model" {
		t.Fatalf("agent model = %#v, want export-model (restored)", agent["model"])
	}
	if temp, ok := agent["temperature"].(json.Number); !ok || temp.String() != "0.5" {
		t.Fatalf("agent temperature = %#v, want 0.5 (restored)", agent["temperature"])
	}
	if agent["runner_base_url"] != "https://current-runner" {
		t.Fatalf("agent runner_base_url = %#v, want current value", agent["runner_base_url"])
	}
	if agent["runner_internal_token"] != "current-token" {
		t.Fatalf("agent runner_internal_token = %#v, want current value", agent["runner_internal_token"])
	}
}

// TestImportRestoreLegacyV3Backup v3 旧备份仍可导入,摘要 Sections 为旧全量集合。
func TestImportRestoreLegacyV3Backup(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	service := NewService(st, confFile, "master-key")

	archivePath := writePartialBackupZipFixture(t, manifest{
		FormatVersion: 3,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append([]string(nil), legacyV3Tables...),
	}, tablesOfBackupTables(), nil, nil)
	encrypted := encryptBackupFixture(t, archivePath)

	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
	if err != nil {
		t.Fatalf("Import legacy v3 backup: %v", err)
	}
	if !slices.Equal(summary.Sections, v4SectionsWithoutAgent()) {
		t.Fatalf("sections = %#v, want %#v", summary.Sections, v4SectionsWithoutAgent())
	}
	if summary.Tables != 22 {
		t.Fatalf("tables = %d, want 22", summary.Tables)
	}
}

// TestImportRestoreRequiresAgentDirs 备份里含 agent_sessions/agent_workspace
// 但 Service 没配对应目录时,导入报错且库与配置都没有被改动。
func TestImportRestoreRequiresAgentDirs(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	siteID, _ := seedRestoreSectionStore(t, st.DB())
	if err := confFile.Set("appearance", map[string]any{"theme": "dark"}); err != nil {
		t.Fatalf("set appearance: %v", err)
	}
	service := NewService(st, confFile, "master-key")

	cases := []struct {
		name    string
		section string
		wantErr string
	}{
		{
			name:    "能力文件缺数据目录",
			section: sectionAgentCapabilities,
			wantErr: `cannot restore backup section "agent_capabilities": agent data directory is not configured`,
		},
		{
			name:    "会话缺数据目录",
			section: sectionAgentSessions,
			wantErr: `cannot restore backup section "agent_sessions": agent data directory is not configured`,
		},
		{
			name:    "工作区缺目录",
			section: sectionAgentWorkspace,
			wantErr: `cannot restore backup section "agent_workspace": agent workspace directory is not configured`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := writePartialBackupZipFixture(t, v4Manifest([]string{tc.section}), nil, nil, nil)
			encrypted := encryptBackupFixture(t, archivePath)

			_, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Import %s err = %v, want %q", tc.section, err, tc.wantErr)
			}

			var siteName string
			if err := st.DB().Raw("SELECT name FROM sites WHERE id = ?", siteID).Scan(&siteName).Error; err != nil || siteName != "seed-site" {
				t.Fatalf("site name = %q err=%v, want seed-site (database untouched)", siteName, err)
			}
			if got := countTableRows(t, st.DB(), "api_keys"); got != 1 {
				t.Fatalf("api_keys rows = %d, want 1 (database untouched)", got)
			}
			if appearance, _ := confFile.Data()["appearance"].(map[string]any); appearance["theme"] != "dark" {
				t.Fatalf("appearance changed: %#v", confFile.Data()["appearance"])
			}
		})
	}
}

// TestImportRestoreAgentSessionsWorkspaceRoundTrip 会话与工作区类别的完整
// 导入恢复往返:会话 meta 的 slot 被删除,jsonl 文件头 cwd 改写为新的工作区
// 目录;工作区按文件覆盖,备份里没有的文件保留。
func TestImportRestoreAgentSessionsWorkspaceRoundTrip(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	agentRoot := t.TempDir()
	workspaceRoot := t.TempDir()

	sessionID := "00000000-0000-0000-0000-0000000000b1"
	writeAgentTestFile(t, agentRoot, "sessions/"+sessionID+"/meta.json", `{"model":"gpt","slot":3}`)
	writeAgentTestFile(t, agentRoot, "sessions/"+sessionID+"/pi/session.jsonl", "{\"cwd\":\"/old/workspace\",\"id\":\"s1\"}\n{\"role\":\"user\"}\n")
	writeAgentTestFile(t, agentRoot, "sessions/"+sessionID+"/.xlyra/state.json", `{"ok":true}`)
	writeAgentTestFile(t, workspaceRoot, "docs/readme.md", "工作区文件")
	writeAgentTestFile(t, workspaceRoot, "main.go", "package main")

	service := NewService(st, confFile, "master-key").WithAgentRoot(agentRoot).WithAgentWorkspaceRoot(workspaceRoot)
	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", []string{sectionSettings, sectionAgentSessions, sectionAgentWorkspace})
	if err != nil {
		t.Fatalf("ExportSections: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	// 导出后删除/修改会话与工作区文件,验证导入会恢复导出时的内容。
	if err := os.RemoveAll(filepath.Join(agentRoot, "sessions", sessionID, "pi")); err != nil {
		t.Fatalf("remove pi dir: %v", err)
	}
	writeAgentTestFile(t, agentRoot, "sessions/"+sessionID+"/meta.json", `{"model":"changed"}`)
	writeAgentTestFile(t, workspaceRoot, "docs/readme.md", "被改过的内容")
	// 备份里没有的文件:导入后应保留。
	writeAgentTestFile(t, workspaceRoot, "extra/new.txt", "新增文件")

	var events []ProgressEvent
	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{}, func(event ProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// 会话 meta:slot 被删除,其余键保留。
	metaContent, err := os.ReadFile(filepath.Join(agentRoot, "sessions", sessionID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaContent, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if _, ok := meta["slot"]; ok {
		t.Fatalf("meta slot = %#v, want removed", meta["slot"])
	}
	if meta["model"] != "gpt" {
		t.Fatalf("meta model = %#v, want gpt (restored)", meta["model"])
	}

	// jsonl 文件头 cwd 改写为 workspaceRoot,后续行保留。
	piContent, err := os.ReadFile(filepath.Join(agentRoot, "sessions", sessionID, "pi", "session.jsonl"))
	if err != nil {
		t.Fatalf("read pi session.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(piContent), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("pi session lines = %#v, want 2", lines)
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("parse pi header: %v", err)
	}
	if header["cwd"] != workspaceRoot {
		t.Fatalf("pi header cwd = %#v, want %q", header["cwd"], workspaceRoot)
	}
	if header["id"] != "s1" {
		t.Fatalf("pi header id = %#v, want s1 (kept)", header["id"])
	}
	if lines[1] != `{"role":"user"}` {
		t.Fatalf("pi second line = %q, want user message kept", lines[1])
	}
	assertAgentBackupFileContent(t, agentRoot, "sessions/"+sessionID+"/.xlyra/state.json", `{"ok":true}`)

	// 工作区:被改的文件恢复,备份里没有的文件保留。
	assertAgentBackupFileContent(t, workspaceRoot, "docs/readme.md", "工作区文件")
	assertAgentBackupFileContent(t, workspaceRoot, "main.go", "package main")
	assertAgentBackupFileContent(t, workspaceRoot, "extra/new.txt", "新增文件")

	if summary.AgentSessions != 1 {
		t.Fatalf("summary agent_sessions = %d, want 1", summary.AgentSessions)
	}
	if summary.AgentWorkspaceFiles != 2 {
		t.Fatalf("summary agent_workspace_files = %d, want 2", summary.AgentWorkspaceFiles)
	}

	// 进度事件包含会话与工作区两个步骤的 in_progress 与 complete。
	for _, step := range []string{"agent_sessions", "agent_workspace"} {
		var started, completed bool
		for _, event := range events {
			if event.Step != step {
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
			t.Fatalf("%s progress events missing start/complete: %#v", step, events)
		}
	}
}

// countAllBackupTableRows 统计全部备份表各自行数,用于比较导入前后是否不变。
func countAllBackupTableRows(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()

	out := make(map[string]int64, len(backupTables))
	for _, table := range backupTables {
		out[table.Name] = countTableRows(t, db, table.Name)
	}
	return out
}

func mapsEqualInt64(left map[string]int64, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
