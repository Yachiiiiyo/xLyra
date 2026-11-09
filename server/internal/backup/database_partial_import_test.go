package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// writePartialBackupZipFixture 手工拼一个备份 zip:manifest + config + 给定表的
// 空 jsonl 条目(omit 里的表不写条目),可选附带 row_counts.json。
func writePartialBackupZipFixture(t *testing.T, m manifest, tables []string, omit map[string]struct{}, rowCounts map[string]int) string {
	t.Helper()

	entries := map[string]string{}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	entries["manifest.json"] = string(encoded)
	entries["config.json"] = `{}`
	for _, table := range tables {
		if _, ok := omit[table]; ok {
			continue
		}
		entries["database/"+table+".jsonl"] = ""
	}
	if rowCounts != nil {
		encoded, err := json.Marshal(rowCounts)
		if err != nil {
			t.Fatalf("marshal row counts: %v", err)
		}
		entries["row_counts.json"] = string(encoded)
	}

	archivePath := filepath.Join(t.TempDir(), "partial-backup.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close fixture archive: %v", err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write fixture archive: %v", err)
	}
	return archivePath
}

func v4PartialSections() []string {
	return []string{sectionSettings, sectionGatewaySites}
}

// TestParseArchiveV4PartialTables 解析只含站点类表的 v4 部分备份:Present 正确、
// 非 present 的表不加载、row_counts 只统计 present 的表。
func TestParseArchiveV4PartialTables(t *testing.T) {
	t.Parallel()

	tables := TablesOf(v4PartialSections())
	rowCounts := map[string]int{"sites": 2}
	for _, table := range tables {
		if _, ok := rowCounts[table]; !ok {
			rowCounts[table] = 0
		}
	}
	archivePath := writePartialBackupZipFixture(t, v4Manifest(v4PartialSections()), tables, nil, rowCounts)

	payload, dump, err := parseArchive(archivePath)
	if err != nil {
		t.Fatalf("parse v4 partial archive: %v", err)
	}
	if err := validateManifest(payload.Manifest); err != nil {
		t.Fatalf("validate v4 partial manifest: %v", err)
	}
	if !slices.Equal(dump.Present, tables) {
		t.Fatalf("dump.Present = %#v, want %#v", dump.Present, tables)
	}
	if dump.TotalRows != 2 {
		t.Fatalf("dump.TotalRows = %d, want 2", dump.TotalRows)
	}
	if _, ok := dump.Tables["api_keys"]; ok {
		t.Fatal("non-present table api_keys should not be loaded")
	}
	for _, table := range tables {
		if _, streamed := streamedImportTables[table]; streamed {
			continue
		}
		if _, ok := dump.Tables[table]; !ok {
			t.Fatalf("present table %s missing from dump.Tables", table)
		}
	}
}

// TestParseArchiveV4PartialMissingPresentEntry:v4 部分备份缺少 present 表的
// 条目时要报错(非流式与流式各验一次)。
func TestParseArchiveV4PartialMissingPresentEntry(t *testing.T) {
	t.Parallel()

	tables := TablesOf(v4PartialSections())
	archivePath := writePartialBackupZipFixture(t, v4Manifest(v4PartialSections()), tables, map[string]struct{}{"sites": {}}, nil)
	_, _, err := parseArchive(archivePath)
	if err == nil || err.Error() != "backup archive missing database/sites.jsonl" {
		t.Fatalf("missing sites entry err = %v", err)
	}

	allSections := append(v4PartialSections(), sectionGatewayKeys, sectionGatewayLogs)
	allTables := TablesOf(allSections)
	archivePath = writePartialBackupZipFixture(t, v4Manifest(allSections), allTables, map[string]struct{}{"request_logs": {}}, nil)
	_, _, err = parseArchive(archivePath)
	if err == nil || err.Error() != "backup archive missing database/request_logs.jsonl" {
		t.Fatalf("missing request_logs entry err = %v", err)
	}
}

// TestParseArchiveV4RejectsUnknownManifestTable:v4 manifest 里出现未知备份表
// 时解析直接报 backup table list mismatch。
func TestParseArchiveV4RejectsUnknownManifestTable(t *testing.T) {
	t.Parallel()

	tables := append(TablesOf(v4PartialSections()), "not_a_table")
	m := v4Manifest(v4PartialSections())
	m.Tables = tables
	archivePath := writePartialBackupZipFixture(t, m, tables, nil, nil)
	_, _, err := parseArchive(archivePath)
	if err == nil || err.Error() != "backup table list mismatch" {
		t.Fatalf("unknown manifest table err = %v, want backup table list mismatch", err)
	}
}

// legacyV3PresentTables 是 v2/v3 旧备份实际存在的表:legacyV3Tables 里属于
// 已知备份表的那部分(即旧的 22 张,不含后来新增的备份表)。
func legacyV3PresentTables() []string {
	present := make([]string, 0, len(legacyV3Tables))
	for _, table := range legacyV3Tables {
		if _, ok := backupTableByName(table); ok {
			present = append(present, table)
		}
	}
	return present
}

// TestParseArchiveV3PresentAll:v3 备份的 Present 是旧全量表(不含新增备份表)。
func TestParseArchiveV3PresentAll(t *testing.T) {
	t.Parallel()

	archivePath := writePartialBackupZipFixture(t, manifest{
		FormatVersion: 3,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append([]string(nil), legacyV3Tables...),
	}, legacyV3PresentTables(), nil, nil)

	_, dump, err := parseArchive(archivePath)
	if err != nil {
		t.Fatalf("parse v3 archive: %v", err)
	}
	if !slices.Equal(dump.Present, legacyV3PresentTables()) {
		t.Fatalf("dump.Present = %#v, want legacy backup tables", dump.Present)
	}
	for _, table := range []string{"site_model_endpoint_overrides", "js_plugins"} {
		if slices.Contains(dump.Present, table) {
			t.Fatalf("v3 present should not contain new table %q", table)
		}
	}
}

func tablesOfBackupTables() []string {
	out := make([]string, 0, len(backupTables))
	for _, table := range backupTables {
		out = append(out, table.Name)
	}
	return out
}

// seedPartialImportStore 在测试库里种入:一个站点、一个 API 密钥、一条密钥
// 站点权限、一条 request_log 与一条 usage_record。
func countTableRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM " + table).Scan(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestImportDatabasePartialPresentAndClear 依赖真库:先种数据,再用
// Present=站点类表、Clear=ClearedTables(gateway_sites) 导入,断言站点被
// 替换、权限/日志/用量被清空、api_keys 保留。
func TestImportDatabasePartialPresentAndClear(t *testing.T) {
	st := openAgentBackupTestStore(t)
	db := st.DB()

	seedSiteID := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	seedAPIKeyID := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	seedRequestLogID := uuid.MustParse("00000000-0000-0000-0000-0000000000a3")
	restoredSiteID := uuid.MustParse("00000000-0000-0000-0000-0000000000b1")

	if err := db.Exec(`INSERT INTO sites (id, name, slug, site_type, base_url, status, enabled, routing_priority)
		VALUES (?, 'seed-site', 'seed-site', 'openai', 'https://seed.example', 'active', true, 1)`, seedSiteID).Error; err != nil {
		t.Fatalf("seed site: %v", err)
	}
	if err := db.Exec(`INSERT INTO api_keys (id, name, key_prefix, key_hash, masked_key, scope, status)
		VALUES (?, 'seed-key', 'sk-seed', 'hash-seed', 'sk-***-seed', 'all', 'active')`, seedAPIKeyID).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.Exec(`INSERT INTO api_key_site_permissions (id, api_key_id, site_id, enabled)
		VALUES (?, ?, ?, true)`, uuid.New(), seedAPIKeyID, seedSiteID).Error; err != nil {
		t.Fatalf("seed api key site permission: %v", err)
	}
	if err := db.Exec(`INSERT INTO request_logs (id, request_id, api_key_id, site_id, endpoint, status_code, success)
		VALUES (?, 'req-seed', ?, ?, '/v1/chat/completions', 200, true)`, seedRequestLogID, seedAPIKeyID, seedSiteID).Error; err != nil {
		t.Fatalf("seed request log: %v", err)
	}
	if err := db.Exec(`INSERT INTO usage_records (id, request_log_id, api_key_id, site_id)
		VALUES (?, ?, ?, ?)`, uuid.New(), seedRequestLogID, seedAPIKeyID, seedSiteID).Error; err != nil {
		t.Fatalf("seed usage record: %v", err)
	}

	restoredAt := "2026-09-01T00:00:00Z"
	dump := databaseDump{
		Tables:  map[string][]map[string]any{},
		Present: TablesOf([]string{sectionGatewaySites}),
		Clear:   ClearedTables([]string{sectionGatewaySites}),
	}
	dump.Tables["sites"] = []map[string]any{{
		"id":               restoredSiteID.String(),
		"name":             "restored-site",
		"slug":             "restored-site",
		"site_type":        "openai",
		"base_url":         "https://restored.example",
		"status":           "active",
		"enabled":          true,
		"routing_priority": 1,
		"meta":             map[string]any{},
		"created_at":       restoredAt,
		"updated_at":       restoredAt,
	}}

	rows, totalRows, err := importDatabase(context.Background(), db, "master-key", dump, uuid.Nil)
	if err != nil {
		t.Fatalf("importDatabase partial: %v", err)
	}
	if rows != 1 || totalRows != 1 {
		t.Fatalf("rows/totalRows = %d/%d, want 1/1", rows, totalRows)
	}

	var siteName string
	if err := db.Raw("SELECT name FROM sites WHERE id = ?", restoredSiteID).Scan(&siteName).Error; err != nil || siteName != "restored-site" {
		t.Fatalf("restored site = %q err=%v, want restored-site", siteName, err)
	}
	if got := countTableRows(t, db, "sites"); got != 1 {
		t.Fatalf("sites rows = %d, want 1 (seed site replaced)", got)
	}
	if got := countTableRows(t, db, "api_keys"); got != 1 {
		t.Fatalf("api_keys rows = %d, want 1 (api keys retained)", got)
	}
	if got := countTableRows(t, db, "api_key_site_permissions"); got != 0 {
		t.Fatalf("api_key_site_permissions rows = %d, want 0", got)
	}
	if got := countTableRows(t, db, "request_logs"); got != 0 {
		t.Fatalf("request_logs rows = %d, want 0", got)
	}
	if got := countTableRows(t, db, "usage_records"); got != 0 {
		t.Fatalf("usage_records rows = %d, want 0", got)
	}
}

// TestImportDatabaseRejectsPresentTableOutsideClear:present 里有表但不在清空
// 集合里时返回错误,防止插入后主键冲突。
func TestImportDatabaseRejectsPresentTableOutsideClear(t *testing.T) {
	st := openAgentBackupTestStore(t)
	db := st.DB()

	dump := databaseDump{
		Tables:  map[string][]map[string]any{"sites": nil},
		Present: []string{"sites"},
		Clear:   []string{"site_groups"},
	}
	_, _, err := importDatabase(context.Background(), db, "master-key", dump, uuid.Nil)
	if err == nil || err.Error() != "backup table sites is present but not cleared" {
		t.Fatalf("present outside clear err = %v", err)
	}
}

// TestImportDatabaseEmptyPresentAndClearSkipsDatabase:Present 与 Clear 都为空
// 时不动库、只触发 beforeCommit。
func TestImportDatabaseEmptyPresentAndClearSkipsDatabase(t *testing.T) {
	st := openAgentBackupTestStore(t)
	db := st.DB()

	siteID := uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	if err := db.Exec(`INSERT INTO sites (id, name, slug, site_type, base_url, status, enabled, routing_priority)
		VALUES (?, 'kept-site', 'kept-site', 'openai', 'https://kept.example', 'active', true, 1)`, siteID).Error; err != nil {
		t.Fatalf("seed site: %v", err)
	}

	called := false
	dump := databaseDump{
		Tables:  map[string][]map[string]any{},
		Present: []string{},
		Clear:   []string{},
		beforeCommit: func() error {
			called = true
			return nil
		},
	}
	rows, totalRows, err := importDatabase(context.Background(), db, "master-key", dump, uuid.Nil)
	if err != nil {
		t.Fatalf("importDatabase empty: %v", err)
	}
	if rows != 0 || totalRows != 0 {
		t.Fatalf("rows/totalRows = %d/%d, want 0/0", rows, totalRows)
	}
	if !called {
		t.Fatal("beforeCommit was not called")
	}
	if got := countTableRows(t, db, "sites"); got != 1 {
		t.Fatalf("sites rows = %d, want 1 (database untouched)", got)
	}
}
