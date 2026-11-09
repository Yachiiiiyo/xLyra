package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// decryptExportSectionsArchive 导出后解密,返回解压后的 zip 路径。
func decryptExportSectionsArchive(t *testing.T, service Service, ids []string) string {
	t.Helper()

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", ids)
	if err != nil {
		t.Fatalf("ExportSections: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}
	plainPath := filepath.Join(t.TempDir(), "exported.zip")
	plain, err := os.Create(plainPath)
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	if err := decryptStream("passphrase", bytes.NewReader(encrypted), plain); err != nil {
		t.Fatalf("decrypt export: %v", err)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return plainPath
}

// readExportSectionsArchive 打开解密后的备份 zip,返回 manifest、config、
// row_counts 与全部条目名。
func readExportSectionsArchive(t *testing.T, path string) (manifest, map[string]any, map[string]int, []string) {
	t.Helper()

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()

	var m manifest
	if err := decodeArchiveJSON(zr.File, "manifest.json", &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	var confData map[string]any
	if err := decodeArchiveJSON(zr.File, "config.json", &confData); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	var rowCounts map[string]int
	if err := decodeArchiveJSON(zr.File, archiveRowCountsFile, &rowCounts); err != nil {
		t.Fatalf("decode row counts: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	for _, file := range zr.File {
		names = append(names, file.Name)
	}
	return m, confData, rowCounts, names
}

func TestExportSectionsGatewaySitesOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	if err := confFile.Set("network", map[string]any{"listen": ":8080"}); err != nil {
		t.Fatalf("set network config: %v", err)
	}
	if err := confFile.Set("agent", map[string]any{"model": "test-model"}); err != nil {
		t.Fatalf("set agent config: %v", err)
	}

	service := NewService(st, confFile, "master-key")
	path := decryptExportSectionsArchive(t, service, []string{sectionGatewaySites})
	m, confData, rowCounts, names := readExportSectionsArchive(t, path)

	if !slices.Equal(m.Sections, []string{sectionGatewaySites}) {
		t.Fatalf("sections = %#v", m.Sections)
	}
	tables := TablesOf([]string{sectionGatewaySites})
	if !slices.Equal(m.Tables, tables) {
		t.Fatalf("tables = %#v, want %#v", m.Tables, tables)
	}
	var databaseEntries []string
	for _, name := range names {
		if strings.HasPrefix(name, "database/") {
			databaseEntries = append(databaseEntries, strings.TrimSuffix(strings.TrimPrefix(name, "database/"), ".jsonl"))
		}
		if strings.HasPrefix(name, "agent/") {
			t.Fatalf("agent entry %q should not exist without agent_capabilities", name)
		}
	}
	if !slices.Equal(databaseEntries, tables) {
		t.Fatalf("database entries = %#v, want %#v", databaseEntries, tables)
	}
	if len(rowCounts) != len(tables) {
		t.Fatalf("row counts = %#v, want only %d tables", rowCounts, len(tables))
	}
	for _, table := range tables {
		if _, ok := rowCounts[table]; !ok {
			t.Fatalf("row counts missing %q: %#v", table, rowCounts)
		}
	}
	if len(confData) != 1 {
		t.Fatalf("config = %#v, want only network", confData)
	}
	if _, ok := confData["network"]; !ok {
		t.Fatalf("config = %#v, want network", confData)
	}
}

func TestExportSectionsSettingsOnlySkipsDatabase(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)

	service := NewService(st, confFile, "master-key")
	path := decryptExportSectionsArchive(t, service, []string{sectionSettings})
	m, _, rowCounts, names := readExportSectionsArchive(t, path)

	if !slices.Equal(m.Sections, []string{sectionSettings}) {
		t.Fatalf("sections = %#v", m.Sections)
	}
	if len(m.Tables) != 0 {
		t.Fatalf("tables = %#v, want empty", m.Tables)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "database/") {
			t.Fatalf("database entry %q should not exist for settings only", name)
		}
	}
	if len(rowCounts) != 0 {
		t.Fatalf("row counts = %#v, want empty", rowCounts)
	}
}

func TestExportSectionsValidationErrors(t *testing.T) {
	t.Parallel()

	service := NewService(nil, backupConfigFile(t), "master-key")

	cases := []struct {
		name    string
		ids     []string
		wantErr string
	}{
		{
			name:    "缺硬依赖",
			ids:     []string{sectionGatewayKeys},
			wantErr: `backup section "gateway_keys" requires "gateway_sites"`,
		},
		{
			name:    "未知类别",
			ids:     []string{"not_a_section"},
			wantErr: `unknown backup section "not_a_section"`,
		},
		{
			name:    "为空",
			ids:     nil,
			wantErr: "至少选择一个备份类别",
		},
		{
			name:    "agent_sessions 缺少 agent 目录",
			ids:     []string{sectionAgentSessions},
			wantErr: `backup section "agent_sessions" requires agent data directory`,
		},
		{
			name:    "agent_workspace 缺少工作区目录",
			ids:     []string{sectionAgentWorkspace},
			wantErr: `backup section "agent_workspace" requires agent workspace directory`,
		},
		{
			name:    "agent_capabilities 缺少 agent 目录",
			ids:     []string{sectionAgentCapabilities},
			wantErr: `backup section "agent_capabilities" requires agent data directory`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := service.ExportSections(context.Background(), "passphrase", tc.ids)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("ExportSections err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestExportSectionsSortsByRegistryOrder(t *testing.T) {
	t.Parallel()

	// 乱序传入也应按注册表顺序写入 manifest;此处只验证排序辅助函数。
	got := sortedSectionIDs([]string{sectionAgentConfig, sectionGatewaySites, sectionSettings})
	want := []string{sectionSettings, sectionGatewaySites, sectionAgentConfig}
	if !slices.Equal(got, want) {
		t.Fatalf("sortedSectionIDs = %#v, want %#v", got, want)
	}
}
