package backup

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"
	"time"
)

func TestDecryptFirstChunk(t *testing.T) {
	t.Parallel()

	// 两块明文:第一块恰好 backupChunkSize,第二块短一些。
	plaintext := append(bytes.Repeat([]byte("x"), backupChunkSize), []byte("tail")...)
	var encrypted bytes.Buffer
	if err := encryptStream("passphrase", bytes.NewReader(plaintext), &encrypted); err != nil {
		t.Fatalf("encryptStream: %v", err)
	}
	full := encrypted.Bytes()

	// 信封头长度 + 第一个块(长度前缀 + 密文)的边界。
	headerLen := len(envelopeMagic) + 2 + envelopeSaltBytes + noncePrefixBytes
	firstChunkLen := int(binary.BigEndian.Uint32(full[headerLen : headerLen+4]))
	firstChunkEnd := headerLen + 4 + firstChunkLen

	first, err := decryptFirstChunk("passphrase", bytes.NewReader(full))
	if err != nil {
		t.Fatalf("decryptFirstChunk: %v", err)
	}
	if !bytes.Equal(first, plaintext[:backupChunkSize]) {
		t.Fatalf("first chunk = %d bytes, want first %d plaintext bytes", len(first), backupChunkSize)
	}

	// 输入在第一个块之后被截断:只提供头 + 第一个块仍能成功。
	truncated, err := decryptFirstChunk("passphrase", bytes.NewReader(full[:firstChunkEnd]))
	if err != nil {
		t.Fatalf("decryptFirstChunk truncated after first chunk: %v", err)
	}
	if !bytes.Equal(truncated, first) {
		t.Fatal("truncated input should decrypt the same first chunk")
	}

	// 只到头 + 长度前缀(密文不完整)应失败。
	if _, err := decryptFirstChunk("passphrase", bytes.NewReader(full[:headerLen+4])); err == nil {
		t.Fatal("incomplete first chunk should fail")
	}

	// 错误口令:与 decryptStream 同类的错误。
	if _, err := decryptFirstChunk("wrong", bytes.NewReader(full)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("decrypt backup payload")) {
		t.Fatalf("wrong passphrase err = %v, want decrypt backup payload", err)
	}

	// 空 payload:第一个块为空。
	var empty bytes.Buffer
	if err := encryptStream("passphrase", bytes.NewReader(nil), &empty); err != nil {
		t.Fatalf("encryptStream empty: %v", err)
	}
	if _, err := decryptFirstChunk("passphrase", bytes.NewReader(empty.Bytes())); err == nil {
		t.Fatal("empty payload should fail")
	}

	// 头损坏。
	if _, err := decryptFirstChunk("passphrase", bytes.NewReader([]byte("not-a-backup"))); err == nil {
		t.Fatal("corrupted header should fail")
	}
}

// inspectTestArchiveEntry 是手工拼 zip 备份时的一条目:name 为空表示目录
// 之外不再有含义,method 为 zip.Store 或 zip.Deflate。
type inspectTestArchiveEntry struct {
	name   string
	method uint16
	body   string
}

// inspectTestEncryptedArchive 把条目按顺序写成 zip(manifest.json 应放第一
// 条,与 writeArchive 一致)再整体加密,得到可直接预览的备份内容。
func inspectTestEncryptedArchive(t *testing.T, entries []inspectTestArchiveEntry) []byte {
	t.Helper()

	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.DefaultCompression)
	})
	for _, entry := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     entry.name,
			Method:   entry.method,
			Modified: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("create zip entry %s: %v", entry.name, err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write zip entry %s: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	var encrypted bytes.Buffer
	if err := encryptStream("passphrase", &plain, &encrypted); err != nil {
		t.Fatalf("encryptStream: %v", err)
	}
	return encrypted.Bytes()
}

func inspectTestManifestEntry(t *testing.T, value manifest, method uint16) inspectTestArchiveEntry {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return inspectTestArchiveEntry{name: "manifest.json", method: method, body: string(encoded)}
}

// inspectTestExport 用真库导出指定类别的加密备份,返回密文字节;
// sections 为 nil 时导出全部默认类别。
func inspectTestExport(t *testing.T, service Service, sections []string) []byte {
	t.Helper()

	var path string
	var err error
	if sections == nil {
		path, _, err = service.Export(context.Background(), "passphrase")
	} else {
		path, _, err = service.ExportSections(context.Background(), "passphrase", sections)
	}
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	encrypted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}
	return encrypted
}

func TestInspectReaderGatewaySitesImpact(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	exporter := NewService(st, confFile, "master-key")
	inspector := NewService(st, confFile, "master-key")

	encrypted := inspectTestExport(t, exporter, []string{sectionGatewaySites})
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader: %v", err)
	}
	if result.Legacy {
		t.Fatal("v4 backup should not be legacy")
	}
	if result.FormatVersion != currentFormatVersion {
		t.Fatalf("format version = %d, want %d", result.FormatVersion, currentFormatVersion)
	}
	if !slices.Equal(result.Sections, []string{sectionGatewaySites}) {
		t.Fatalf("sections = %#v", result.Sections)
	}
	wantImpact := []SectionImpact{
		{ID: sectionGatewayKeys, Partial: true},
		{ID: sectionGatewayLogs, Partial: false},
	}
	if !slices.Equal(result.Impact, wantImpact) {
		t.Fatalf("impact = %#v, want %#v", result.Impact, wantImpact)
	}
	if len(result.Unavailable) != 0 {
		t.Fatalf("unavailable = %#v, want empty", result.Unavailable)
	}
}

func TestInspectReaderSettingsOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	exporter := NewService(st, confFile, "master-key")
	inspector := NewService(st, confFile, "master-key")

	encrypted := inspectTestExport(t, exporter, []string{sectionSettings})
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader: %v", err)
	}
	if !slices.Equal(result.Sections, []string{sectionSettings}) {
		t.Fatalf("sections = %#v", result.Sections)
	}
	if len(result.Impact) != 0 {
		t.Fatalf("impact = %#v, want empty", result.Impact)
	}
}

func TestInspectReaderFullExportAndTruncatedHead(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	sourceRoot := t.TempDir()
	writeAgentTestFile(t, sourceRoot, "skills/demo/SKILL.md", "技能正文")

	exporter := NewService(st, confFile, "master-key").WithAgentRoot(sourceRoot)
	inspector := NewService(st, confFile, "master-key")

	encrypted := inspectTestExport(t, exporter, nil)
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader: %v", err)
	}
	if result.Legacy {
		t.Fatal("v4 backup should not be legacy")
	}
	if !slices.Equal(result.Sections, allExportedSections()) {
		t.Fatalf("sections = %#v, want %#v", result.Sections, allExportedSections())
	}
	if len(result.Impact) != 0 {
		t.Fatalf("impact = %#v, want empty", result.Impact)
	}

	// 只给文件开头 2MiB 也应成功:manifest 在第一个 1MiB 分块内。
	head := encrypted
	if len(head) > 2<<20 {
		head = head[:2<<20]
	}
	if _, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(head)); err != nil {
		t.Fatalf("InspectReader with 2MiB head: %v", err)
	}
}

func TestInspectReaderAgentCapabilitiesUnavailable(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	sourceRoot := t.TempDir()
	writeAgentTestFile(t, sourceRoot, "skills/demo/SKILL.md", "技能正文")

	exporter := NewService(st, confFile, "master-key").WithAgentRoot(sourceRoot)
	// 检查方没有配置 agent 目录:备份里的 agent_capabilities 无法恢复。
	inspector := NewService(st, confFile, "master-key")

	encrypted := inspectTestExport(t, exporter, nil)
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader: %v", err)
	}
	if len(result.Unavailable) != 1 || result.Unavailable[0].ID != sectionAgentCapabilities {
		t.Fatalf("unavailable = %#v, want only agent_capabilities", result.Unavailable)
	}
	if result.Unavailable[0].Reason == "" {
		t.Fatal("unavailable reason should not be empty")
	}
}

func TestInspectReaderLegacyV3(t *testing.T) {
	confFile := backupConfigFile(t)
	inspector := NewService(nil, confFile, "master-key")

	legacyManifest := manifest{
		FormatVersion: 3,
		App:           backupAppName,
		Payload:       backupPayload,
		Tables:        append([]string(nil), legacyV3Tables...),
	}
	encrypted := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		inspectTestManifestEntry(t, legacyManifest, zip.Deflate),
		{name: "config.json", method: zip.Deflate, body: `{}`},
	})
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader legacy v3: %v", err)
	}
	if !result.Legacy {
		t.Fatal("v3 backup should be legacy")
	}
	if result.FormatVersion != 3 {
		t.Fatalf("format version = %d, want 3", result.FormatVersion)
	}
	if !slices.Equal(result.Sections, v4SectionsWithoutAgent()) {
		t.Fatalf("sections = %#v, want legacy full set", result.Sections)
	}
}

func TestInspectReaderStoreMethodManifest(t *testing.T) {
	confFile := backupConfigFile(t)
	inspector := NewService(nil, confFile, "master-key")

	encrypted := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		inspectTestManifestEntry(t, v4Manifest([]string{sectionSettings, sectionGatewaySites}), zip.Store),
	})
	result, err := inspector.InspectReader(context.Background(), "passphrase", bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("InspectReader store method: %v", err)
	}
	if !slices.Equal(result.Sections, []string{sectionSettings, sectionGatewaySites}) {
		t.Fatalf("sections = %#v", result.Sections)
	}
}

func TestInspectReaderErrors(t *testing.T) {
	confFile := backupConfigFile(t)
	inspector := NewService(nil, confFile, "master-key")
	ctx := context.Background()

	encrypted := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		inspectTestManifestEntry(t, v4Manifest(v4SectionsWithoutAgent()), zip.Deflate),
	})

	// 错误口令:与导入时同类的错误。
	if _, err := inspector.InspectReader(ctx, "wrong", bytes.NewReader(encrypted)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("decrypt backup payload")) {
		t.Fatalf("wrong passphrase err = %v, want decrypt backup payload", err)
	}

	// 缺少口令与缺少文件。
	if _, err := inspector.InspectReader(ctx, "  ", bytes.NewReader(encrypted)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("passphrase")) {
		t.Fatalf("missing passphrase err = %v", err)
	}
	if _, err := inspector.InspectReader(ctx, "passphrase", nil); err == nil {
		t.Fatal("nil reader should fail")
	}

	// manifest 被篡改成未知 section。
	tampered := v4Manifest([]string{sectionSettings, "not_a_section"})
	tampered.Tables = TablesOf(tampered.Sections)
	tamperedEncrypted := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		inspectTestManifestEntry(t, tampered, zip.Deflate),
	})
	if _, err := inspector.InspectReader(ctx, "passphrase", bytes.NewReader(tamperedEncrypted)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("unknown backup section")) {
		t.Fatalf("tampered manifest err = %v, want unknown backup section", err)
	}

	// 第一个 zip 条目不是 manifest.json。
	wrongFirst := inspectTestEncryptedArchive(t, []inspectTestArchiveEntry{
		{name: "config.json", method: zip.Deflate, body: `{}`},
		inspectTestManifestEntry(t, v4Manifest(v4SectionsWithoutAgent()), zip.Deflate),
	})
	if _, err := inspector.InspectReader(ctx, "passphrase", bytes.NewReader(wrongFirst)); err == nil || !bytes.Contains([]byte(err.Error()), []byte("missing manifest.json")) {
		t.Fatalf("missing manifest err = %v", err)
	}
}
