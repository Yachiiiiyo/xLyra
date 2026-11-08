package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// v4SectionsWithoutAgent 是 agentRoot 为空时的 v4 全量导出类别。
func v4SectionsWithoutAgent() []string {
	return []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionAgentConfig,
	}
}

func v4Manifest(sections []string) manifest {
	return manifest{
		FormatVersion: 4,
		App:           backupAppName,
		Payload:       backupPayload,
		Sections:      sections,
		Tables:        TablesOf(sections),
	}
}

func TestValidateManifestV4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*manifest)
		wantErr string
	}{
		{
			name:   "valid",
			mutate: func(m *manifest) {},
		},
		{
			name: "valid with agent capabilities",
			mutate: func(m *manifest) {
				m.Sections = allExportedSections()
				m.Tables = TablesOf(allExportedSections())
			},
		},
		{
			name: "table list missing one table",
			mutate: func(m *manifest) {
				m.Tables = m.Tables[:len(m.Tables)-1]
			},
			wantErr: "backup table list mismatch",
		},
		{
			name: "table list wrong order",
			mutate: func(m *manifest) {
				m.Tables[0], m.Tables[1] = m.Tables[1], m.Tables[0]
			},
			wantErr: "backup table list mismatch",
		},
		{
			name: "sections missing requirement",
			mutate: func(m *manifest) {
				m.Sections = slices.DeleteFunc(slices.Clone(m.Sections), func(id string) bool {
					return id == sectionGatewaySites
				})
				// gateway_keys 依赖 gateway_sites;保留 gateway_keys,去掉其硬依赖。
				m.Tables = TablesOf(m.Sections)
			},
			wantErr: `backup section "gateway_keys" requires "gateway_sites"`,
		},
		{
			name: "unknown section",
			mutate: func(m *manifest) {
				m.Sections = append(m.Sections, "not_a_section")
				m.Tables = TablesOf(m.Sections)
			},
			wantErr: `unknown backup section "not_a_section"`,
		},
		{
			name: "empty sections",
			mutate: func(m *manifest) {
				m.Sections = nil
				m.Tables = nil
			},
			wantErr: "至少选择一个备份类别",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			value := v4Manifest(v4SectionsWithoutAgent())
			tt.mutate(&value)
			err := validateManifest(value)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateManifest err = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("validateManifest err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateManifestV3Legacy(t *testing.T) {
	t.Parallel()

	base := manifest{
		FormatVersion: 3,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append([]string(nil), legacyV3Tables...),
	}
	if err := validateManifest(base); err != nil {
		t.Fatalf("v3 legacy tables: %v", err)
	}
	withAgent := base
	withAgent.Sections = []string{sectionAgentCapabilities}
	if err := validateManifest(withAgent); err != nil {
		t.Fatalf("v3 with agent capabilities: %v", err)
	}

	missing := base
	missing.Tables = missing.Tables[:len(missing.Tables)-1]
	if err := validateManifest(missing); err == nil || err.Error() != "backup table list mismatch" {
		t.Fatalf("v3 missing table err = %v, want table list mismatch", err)
	}

	unknownSection := base
	unknownSection.Sections = []string{"future_section"}
	if err := validateManifest(unknownSection); err == nil || err.Error() != `unsupported backup section "future_section"` {
		t.Fatalf("v3 unknown section err = %v", err)
	}
}

func TestValidateManifestV2AllowsLegacyPlaygroundTables(t *testing.T) {
	t.Parallel()

	value := manifest{
		FormatVersion: 2,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append(append([]string(nil), exportTables...), legacyPlaygroundTables...),
	}
	if err := validateManifest(value); err != nil {
		t.Fatalf("v2 with playground tables: %v", err)
	}

	withSections := value
	withSections.Sections = []string{sectionAgentCapabilities}
	if err := validateManifest(withSections); err == nil || err.Error() != `unsupported backup section "agent_capabilities"` {
		t.Fatalf("v2 sections err = %v, want unsupported section", err)
	}
}

func TestEffectiveSections(t *testing.T) {
	t.Parallel()

	v2 := manifest{FormatVersion: 2, Tables: append([]string(nil), exportTables...)}
	if got := effectiveSections(v2); !slices.Equal(got, v4SectionsWithoutAgent()) {
		t.Fatalf("effectiveSections(v2) = %#v", got)
	}

	v3 := manifest{FormatVersion: 3}
	if got := effectiveSections(v3); !slices.Equal(got, v4SectionsWithoutAgent()) {
		t.Fatalf("effectiveSections(v3) = %#v", got)
	}

	v3Agent := manifest{FormatVersion: 3, Sections: []string{sectionAgentCapabilities}}
	want := append(v4SectionsWithoutAgent(), sectionAgentCapabilities)
	if got := effectiveSections(v3Agent); !slices.Equal(got, want) {
		t.Fatalf("effectiveSections(v3 with agent) = %#v, want %#v", got, want)
	}

	v4 := v4Manifest([]string{sectionSettings, sectionGatewaySites})
	if got := effectiveSections(v4); !slices.Equal(got, []string{sectionSettings, sectionGatewaySites}) {
		t.Fatalf("effectiveSections(v4) = %#v", got)
	}
}

// TestParseLegacyV3ArchiveIgnoresPlayground 用手工拼出的 v3 备份 zip 验证:
// parseArchive + validateManifest 接受含 playground 表条目与 playground/
// 文件的旧备份,并直接忽略 playground 内容。
func TestParseLegacyV3ArchiveIgnoresPlayground(t *testing.T) {
	t.Parallel()

	entries := map[string]string{
		"config.json": `{}`,
	}
	manifestValue := manifest{
		FormatVersion: 3,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append([]string(nil), legacyV3Tables...),
	}
	encoded, err := json.Marshal(manifestValue)
	if err != nil {
		t.Fatalf("marshal legacy manifest: %v", err)
	}
	entries["manifest.json"] = string(encoded)

	for _, table := range backupTables {
		entries["database/"+table.Name+".jsonl"] = ""
	}
	// 旧备份的 playground 表条目与 playground/ 文件,应被忽略。
	entries["database/playground_conversations.jsonl"] = "{\"id\":\"conv-1\"}\n"
	entries["database/playground_runs.jsonl"] = ""
	entries["database/playground_turn_indexes.jsonl"] = ""
	entries["database/playground_assets.jsonl"] = ""
	entries["playground/sessions/2026/08/24/rollout.jsonl"] = "legacy rollout\n"
	entries["playground/assets/conv-1/generated-images/a.png"] = "legacy asset"

	archivePath := filepath.Join(t.TempDir(), "legacy-v3.zip")
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
		t.Fatalf("close legacy archive: %v", err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write legacy archive: %v", err)
	}

	payload, dump, err := parseArchive(archivePath)
	if err != nil {
		t.Fatalf("parse legacy v3 archive: %v", err)
	}
	if err := validateManifest(payload.Manifest); err != nil {
		t.Fatalf("validate legacy v3 manifest: %v", err)
	}
	if dump.TotalRows != 0 {
		t.Fatalf("legacy dump rows = %d, want 0", dump.TotalRows)
	}
	if _, ok := dump.Tables["playground_conversations"]; ok {
		t.Fatal("legacy playground table should not be loaded")
	}
	if !slices.Contains(effectiveSections(payload.Manifest), sectionAgentConfig) {
		t.Fatalf("legacy sections = %#v, want full legacy set", effectiveSections(payload.Manifest))
	}
}

// TestServiceExportManifestV4 依赖真库导出,断言导出的 manifest 是 v4、
// sections 与 tables 一致、不含 playground。
func TestServiceExportManifestV4(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	sourceRoot := t.TempDir()
	writeAgentTestFile(t, sourceRoot, "skills/demo/SKILL.md", "技能正文")

	exporter := NewService(st, confFile, "master-key").WithAgentRoot(sourceRoot)
	encrypted := exportAgentBackupTestArchive(t, exporter)

	archivePath := filepath.Join(t.TempDir(), "exported.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive file: %v", err)
	}
	if err := decryptStream("passphrase", bytes.NewReader(encrypted), archiveFile); err != nil {
		t.Fatalf("decrypt export: %v", err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}

	payload, dump, err := parseArchive(archivePath)
	if err != nil {
		t.Fatalf("parse exported archive: %v", err)
	}
	if err := validateManifest(payload.Manifest); err != nil {
		t.Fatalf("validate exported manifest: %v", err)
	}
	if payload.Manifest.FormatVersion != 4 {
		t.Fatalf("format version = %d, want 4", payload.Manifest.FormatVersion)
	}
	if !slices.Equal(payload.Manifest.Sections, allExportedSections()) {
		t.Fatalf("sections = %#v, want %#v", payload.Manifest.Sections, allExportedSections())
	}
	if !slices.Equal(payload.Manifest.Tables, TablesOf(allExportedSections())) {
		t.Fatalf("tables = %#v, want TablesOf(sections)", payload.Manifest.Tables)
	}
	for _, table := range payload.Manifest.Tables {
		if table == "playground_conversations" || table == "playground_runs" ||
			table == "playground_turn_indexes" || table == "playground_assets" {
			t.Fatalf("manifest contains playground table %q", table)
		}
	}
	for _, table := range exportTables {
		if strings.HasPrefix(table, "playground_") {
			t.Fatalf("exportTables contains playground table %q", table)
		}
		if _, streamed := streamedImportTables[table]; streamed {
			continue
		}
		if _, ok := dump.Tables[table]; !ok {
			t.Fatalf("dump missing table %q", table)
		}
	}
}
