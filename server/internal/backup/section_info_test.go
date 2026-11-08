package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/config"
)

func TestSectionAvailabilityRequiresAgentDirectories(t *testing.T) {
	t.Parallel()

	// 未配置 agent 目录时,agent 相关类别不可用并给出原因。
	bare := Service{}
	for _, id := range []string{sectionAgentCapabilities, sectionAgentSessions} {
		available, reason := bare.sectionAvailability(id)
		if available || reason != "agent data directory is not configured" {
			t.Fatalf("section %s availability = (%v, %q)", id, available, reason)
		}
	}
	available, reason := bare.sectionAvailability(sectionAgentWorkspace)
	if available || reason != "agent workspace directory is not configured" {
		t.Fatalf("agent_workspace availability = (%v, %q)", available, reason)
	}

	// 配置后全部可用;其余类别恒可用。
	configured := Service{agentRoot: t.TempDir(), agentWorkspaceRoot: t.TempDir()}
	for _, section := range Sections() {
		available, reason := configured.sectionAvailability(section.ID)
		if !available || reason != "" {
			t.Fatalf("section %s availability = (%v, %q)", section.ID, available, reason)
		}
	}
}

func TestSectionInfosReportsFileSizesAndOrder(t *testing.T) {
	t.Parallel()

	agentRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	skillsDir := filepath.Join(agentRoot, "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]int{
		"skills/demo/SKILL.md": 10,
		"AGENTS.md":            20,
		"config.json":          30,
	}
	for name, size := range files {
		if err := os.WriteFile(filepath.Join(agentRoot, filepath.FromSlash(name)), bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 临时文件与符号链接不计入估算。
	if err := os.WriteFile(filepath.Join(skillsDir, "demo", ".SKILL.md.tmp-1"), bytes.Repeat([]byte("y"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(agentRoot, "AGENTS.md"), filepath.Join(agentRoot, "link.md")); err != nil {
		t.Fatal(err)
	}

	// 会话:meta.json + pi/ 下一个文件;.xlyra/ 与非 UUID 目录不参与。
	sessionID := "0b8f5e67-1d2a-4c61-9c8e-2f0a6f6f7f10"
	sessionDir := filepath.Join(agentRoot, "sessions", sessionID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "pi"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "meta.json"), bytes.Repeat([]byte("m"), 7), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "pi", "session.jsonl"), bytes.Repeat([]byte("p"), 11), 0o600); err != nil {
		t.Fatal(err)
	}

	// 工作区:跳过 node_modules,只算普通文件。
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "node_modules", "pkg", "big.bin"), bytes.Repeat([]byte("n"), 500), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "notes.txt"), bytes.Repeat([]byte("w"), 13), 0o644); err != nil {
		t.Fatal(err)
	}

	service := Service{agentRoot: agentRoot, agentWorkspaceRoot: workspaceRoot}
	infos := service.SectionInfos(context.Background())

	if len(infos) != len(Sections()) {
		t.Fatalf("section infos count = %d, want %d", len(infos), len(Sections()))
	}
	byID := make(map[string]SectionInfo, len(infos))
	for i, info := range infos {
		if info.ID != Sections()[i].ID {
			t.Fatalf("infos[%d].ID = %q, want registry order", i, info.ID)
		}
		byID[info.ID] = info
	}

	capabilities := byID[sectionAgentCapabilities]
	if capabilities.SizeBytes == nil || *capabilities.SizeBytes != 60 {
		t.Fatalf("capabilities size = %#v, want 60", capabilities.SizeBytes)
	}
	if capabilities.Rows != nil {
		t.Fatalf("capabilities rows should be unset, got %#v", capabilities.Rows)
	}
	sessions := byID[sectionAgentSessions]
	if sessions.SizeBytes == nil || *sessions.SizeBytes != 18 {
		t.Fatalf("sessions size = %#v, want 18", sessions.SizeBytes)
	}
	workspace := byID[sectionAgentWorkspace]
	if workspace.SizeBytes == nil || *workspace.SizeBytes != 13 {
		t.Fatalf("workspace size = %#v, want 13", workspace.SizeBytes)
	}
	// 数据库类别在无数据库时不填 Rows。
	if byID[sectionGatewaySites].Rows != nil {
		t.Fatalf("gateway_sites rows should be unset without database")
	}
	// 未配置目录时不可用且不估算体积。
	bareInfos := Service{}.SectionInfos(context.Background())
	bareBy := map[string]SectionInfo{}
	for _, info := range bareInfos {
		bareBy[info.ID] = info
	}
	for _, id := range []string{sectionAgentCapabilities, sectionAgentSessions, sectionAgentWorkspace} {
		info := bareBy[id]
		if info.Available || info.UnavailableReason == "" || info.SizeBytes != nil {
			t.Fatalf("section %s info = %#v, want unavailable without size", id, info)
		}
	}
}

func TestSectionInfosEstimatesDatabaseRows(t *testing.T) {
	st := openAgentBackupTestStore(t)

	confFile := backupConfigFile(t)
	service := NewService(st, confFile, "master-key")
	infos := service.SectionInfos(context.Background())

	byID := make(map[string]SectionInfo, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	for _, id := range []string{sectionSettings, sectionGatewaySites, sectionGatewayKeys} {
		if id == sectionSettings {
			if byID[id].Rows != nil {
				t.Fatalf("settings has no tables, rows should be unset, got %#v", byID[id].Rows)
			}
			continue
		}
		if byID[id].Rows == nil {
			t.Fatalf("section %s rows should be estimated with a live database", id)
		}
	}
}

func TestResolveAutomaticSectionsPrefersSavedAndFiltersDefaults(t *testing.T) {
	t.Parallel()

	bare := Service{}
	saved := []string{sectionSettings}
	if got := bare.resolveAutomaticSections(saved); len(got) != 1 || got[0] != sectionSettings {
		t.Fatalf("saved sections should be used as-is, got %#v", got)
	}

	defaults := bare.resolveAutomaticSections(nil)
	// 未配置 agent 目录时,默认类别过滤掉 agent_capabilities/agent_sessions。
	for _, id := range defaults {
		if id == sectionAgentCapabilities || id == sectionAgentSessions || id == sectionAgentWorkspace {
			t.Fatalf("unexpected unavailable default section %q", id)
		}
	}
	if !containsSection(defaults, sectionSettings) {
		t.Fatalf("auto defaults missing settings: %#v", defaults)
	}
	// 除 Agent 工作区外全部默认选中,包括请求日志与用量。
	if !containsSection(defaults, sectionGatewayLogs) {
		t.Fatalf("gateway_logs should be an auto default: %#v", defaults)
	}
	if containsSection(defaults, sectionAgentWorkspace) {
		t.Fatalf("agent_workspace must not be an auto default: %#v", defaults)
	}

	// 配置 agent 目录后,agent 数据类别进入默认集合。
	configured := Service{agentRoot: t.TempDir()}
	defaults = configured.resolveAutomaticSections(nil)
	if !containsSection(defaults, sectionAgentCapabilities) || !containsSection(defaults, sectionAgentSessions) {
		t.Fatalf("auto defaults with agent root = %#v", defaults)
	}
}

func containsSection(ids []string, id string) bool {
	for _, current := range ids {
		if current == id {
			return true
		}
	}
	return false
}

func TestAutomaticUpdateConfigSectionsLifecycle(t *testing.T) {
	t.Parallel()

	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatalf("load config file: %v", err)
	}
	agentRoot := t.TempDir()
	service := NewAutomaticService(NewService(nil, confFile, "master-key").WithAgentRoot(agentRoot), "master-key")

	// 保存合法类别。
	saved, err := service.UpdateConfig(AutomaticConfigInput{Sections: []string{"gateway_sites", "gateway_keys"}})
	if err != nil {
		t.Fatalf("update config with valid sections: %v", err)
	}
	if len(saved.Sections) != 2 || saved.Sections[0] != "gateway_sites" || saved.Sections[1] != "gateway_keys" {
		t.Fatalf("saved payload sections = %#v", saved.Sections)
	}

	// Sections 为 nil 时保持当前设置不变。
	kept, err := service.UpdateConfig(AutomaticConfigInput{RetentionCount: 5})
	if err != nil {
		t.Fatalf("update config without sections: %v", err)
	}
	if len(kept.Sections) != 2 || kept.Sections[0] != "gateway_sites" {
		t.Fatalf("sections should be kept, got %#v", kept.Sections)
	}
	got := service.GetConfig()
	if len(got.Sections) != 2 || got.Sections[1] != "gateway_keys" {
		t.Fatalf("GetConfig sections = %#v", got.Sections)
	}

	// 空数组、未知、重复、缺依赖、不可用类别被拒绝。
	for name, sections := range map[string][]string{
		"empty":       {},
		"unknown":     {"settings", "nope"},
		"duplicate":   {"settings", "settings"},
		"missing dep": {"gateway_keys"},
	} {
		if _, err := service.UpdateConfig(AutomaticConfigInput{Sections: sections}); err == nil {
			t.Fatalf("%s sections should be rejected", name)
		}
	}

	// agent 目录未配置的服务拒绝 agent_workspace(用全新配置文件,避免读到已保存类别)。
	freshConfFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatalf("load config file: %v", err)
	}
	noAgent := NewAutomaticService(NewService(nil, freshConfFile, "master-key"), "master-key")
	if _, err := noAgent.UpdateConfig(AutomaticConfigInput{Sections: []string{"agent_workspace"}}); err == nil || !strings.Contains(err.Error(), "is not available") {
		t.Fatalf("unavailable section should be rejected, got %v", err)
	}

	// 未保存过时,payload 返回自动默认类别中当前可用的。
	payload := noAgent.GetConfig()
	if !containsSection(payload.Sections, sectionSettings) || containsSection(payload.Sections, sectionAgentCapabilities) {
		t.Fatalf("default payload sections = %#v", payload.Sections)
	}
	if containsSection(payload.Sections, sectionAgentWorkspace) || !containsSection(payload.Sections, sectionGatewayLogs) {
		t.Fatalf("default payload sections = %#v", payload.Sections)
	}
}

// automaticRunPUTCaptureTransport 捕获 PUT 请求的请求体,其余请求委托给
// 常规测试 transport。
type automaticRunPUTCaptureTransport struct {
	inner *automaticS3TestRoundTripper
	body  []byte
}

func (rt *automaticRunPUTCaptureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		rt.body = body
		return automaticS3TestResponse(r, http.StatusOK, ""), nil
	}
	return rt.inner.RoundTrip(r)
}

// decodeAWSChunked 解开 minio 流式签名上传的 aws-chunked 帧:
// 每帧为 `<hex 大小>;chunk-signature=<64hex>\r\n<数据>\r\n`,结束帧大小为 0。
func decodeAWSChunked(body []byte) ([]byte, error) {
	var out []byte
	for len(body) > 0 {
		headerEnd := bytes.IndexByte(body, '\n')
		if headerEnd < 0 {
			return nil, fmt.Errorf("missing chunk header")
		}
		header := string(bytes.TrimSpace(body[:headerEnd]))
		body = body[headerEnd+1:]
		header, _, _ = strings.Cut(header, ";")
		size, err := strconv.ParseUint(header, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("parse chunk size: %w", err)
		}
		if size == 0 {
			return out, nil
		}
		if uint64(len(body)) < size {
			return nil, fmt.Errorf("truncated chunk")
		}
		out = append(out, body[:size]...)
		body = body[size:]
		body = bytes.TrimPrefix(body, []byte("\r\n"))
	}
	return out, nil
}

func TestAutomaticRunUsesSavedSections(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile, err := config.LoadConfigFile(t.TempDir())
	if err != nil {
		t.Fatalf("load config file: %v", err)
	}
	if err := confFile.Set(config.AutomaticBackupConfigPath, config.AutomaticBackupConfigToMap(config.AutomaticBackupConfig{
		Sections: []string{sectionSettings},
		Storage: config.AutomaticBackupStorageConfig{
			Endpoint:       "s3.example.com",
			Bucket:         "xlyra",
			Prefix:         "prod",
			AccessKey:      "access",
			ForcePathStyle: true,
		},
	})); err != nil {
		t.Fatalf("set automatic backup config: %v", err)
	}

	service := NewAutomaticService(NewService(st, confFile, "master-key"), "master-key")
	cfg, ok := config.ReadAutomaticBackupConfig(confFile)
	if !ok {
		t.Fatal("expected automatic backup config")
	}
	capture := &automaticRunPUTCaptureTransport{inner: automaticS3TestTransport(t, nil, nil)}
	client := automaticS3TestClient(t, capture)

	_, err = service.run(context.Background(), cfg, client, "passphrase", time.Date(2026, 6, 23, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("automatic run: %v", err)
	}

	// 解密上传的备份并读取 manifest,确认只包含保存的类别。
	uploaded, err := decodeAWSChunked(capture.body)
	if err != nil {
		t.Fatalf("decode chunked upload: %v", err)
	}
	var plain bytes.Buffer
	if err := decryptStream("passphrase", bytes.NewReader(uploaded), &plain); err != nil {
		t.Fatalf("decrypt uploaded backup: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(plain.Bytes()), int64(plain.Len()))
	if err != nil {
		t.Fatalf("open backup archive: %v", err)
	}
	var m manifest
	found := false
	for _, entry := range zr.File {
		if entry.Name != "manifest.json" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatalf("open manifest: %v", err)
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatal("manifest.json missing from uploaded backup")
	}
	if len(m.Sections) != 1 || m.Sections[0] != sectionSettings {
		t.Fatalf("manifest sections = %#v, want only settings", m.Sections)
	}
	if len(m.Tables) != 0 {
		t.Fatalf("manifest tables = %#v, want none", m.Tables)
	}
}

// Requires/Recommends 为空的类别在 JSON 里必须是 [] 而不是 null,前端按数组遍历。
func TestSectionInfosSerializeEmptySlicesAsArrays(t *testing.T) {
	infos := NewService(nil, nil, "").SectionInfos(context.Background())
	if len(infos) == 0 {
		t.Fatal("SectionInfos returned nothing")
	}
	data, err := json.Marshal(infos)
	if err != nil {
		t.Fatalf("marshal section infos: %v", err)
	}
	if bytes.Contains(data, []byte(`"requires":null`)) || bytes.Contains(data, []byte(`"recommends":null`)) {
		t.Fatalf("requires/recommends must serialize as [] not null: %s", data)
	}
}
