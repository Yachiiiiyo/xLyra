package backup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgentTestFile(t *testing.T, root string, relPath string, content string) {
	t.Helper()
	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatalf("create test file dir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

// createAgentTestArchive 把 source root 的能力内容导出到临时 zip 文件。
func createAgentTestArchive(t *testing.T, source string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	zw := zip.NewWriter(file)
	if _, err := exportAgentCapabilities(zw, source); err != nil {
		t.Fatalf("export agent capabilities: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return archivePath
}

func TestExportAndRestoreAgentCapabilitiesRoundTrip(t *testing.T) {
	source := t.TempDir()
	writeAgentTestFile(t, source, "skills/demo/SKILL.md", "---\nname: demo\n---\n技能正文")
	writeAgentTestFile(t, source, "skills/demo/resources/guide.md", "资源文件")
	writeAgentTestFile(t, source, "AGENTS.md", "全局指令")
	writeAgentTestFile(t, source, "config.json", `{"skills": true}`)

	archivePath := createAgentTestArchive(t, source)

	// 目标 root 预置不同的旧内容,恢复后应与备份完全一致。
	target := t.TempDir()
	writeAgentTestFile(t, target, "skills/old/SKILL.md", "旧技能")
	writeAgentTestFile(t, target, "AGENTS.md", "旧指令")
	writeAgentTestFile(t, target, "config.json", "{}")

	files, _, err := restoreAgentCapabilities(archivePath, target, nil)
	if err != nil {
		t.Fatalf("restore agent capabilities: %v", err)
	}
	if files != 4 {
		t.Fatalf("restored files = %d, want 4", files)
	}

	assertFileContent := func(relPath, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(relPath)))
		if err != nil {
			t.Fatalf("read restored %s: %v", relPath, err)
		}
		if string(data) != want {
			t.Fatalf("restored %s = %q, want %q", relPath, data, want)
		}
	}
	assertFileContent("skills/demo/SKILL.md", "---\nname: demo\n---\n技能正文")
	assertFileContent("skills/demo/resources/guide.md", "资源文件")
	assertFileContent("AGENTS.md", "全局指令")
	assertFileContent("config.json", `{"skills": true}`)
	if _, err := os.Stat(filepath.Join(target, "skills/old")); !os.IsNotExist(err) {
		t.Fatalf("old skills dir should be replaced: %v", err)
	}

	assertPerm := func(relPath string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(filepath.Join(target, filepath.FromSlash(relPath)))
		if err != nil {
			t.Fatalf("stat restored %s: %v", relPath, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("perm of %s = %o, want %o", relPath, info.Mode().Perm(), want)
		}
	}
	assertPerm("skills/demo", 0o755)
	assertPerm("skills/demo/resources", 0o755)
	assertPerm("skills/demo/SKILL.md", 0o644)
	assertPerm("skills/demo/resources/guide.md", 0o644)
	assertPerm("AGENTS.md", 0o640)
	assertPerm("config.json", 0o640)
}

func TestExportAgentCapabilitiesSkipsSymlinksAndTempFiles(t *testing.T) {
	source := t.TempDir()
	writeAgentTestFile(t, source, "skills/demo/SKILL.md", "技能正文")
	// 临时文件与符号链接都不应进入备份。
	writeAgentTestFile(t, source, "skills/demo/.SKILL.md.tmp-123", "临时内容")
	writeAgentTestFile(t, source, "skills/real/resource.txt", "真实资源")
	if err := os.Symlink(filepath.Join(source, "skills/real/resource.txt"), filepath.Join(source, "skills/demo/link.txt")); err != nil {
		t.Fatalf("create skill symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(source, "config.json"), filepath.Join(source, "AGENTS.md")); err != nil {
		t.Fatalf("create AGENTS.md symlink: %v", err)
	}
	writeAgentTestFile(t, source, "config.json", `{"skills": true}`)

	archivePath := createAgentTestArchive(t, source)

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	names := make(map[string]bool)
	for _, entry := range zr.File {
		names[entry.Name] = true
	}
	for _, want := range []string{"agent/skills/demo/SKILL.md", "agent/skills/real/resource.txt", "agent/config.json"} {
		if !names[want] {
			t.Fatalf("archive entry %s missing, got %#v", want, names)
		}
	}
	for _, unwanted := range []string{"agent/skills/demo/.SKILL.md.tmp-123", "agent/skills/demo/link.txt", "agent/AGENTS.md"} {
		if names[unwanted] {
			t.Fatalf("archive entry %s should be skipped", unwanted)
		}
	}
}

func TestRestoreAgentCapabilitiesRemovesMissingEntries(t *testing.T) {
	// 备份只含 config.json:恢复后目标原有的 skills 目录与 AGENTS.md 应被移除。
	source := t.TempDir()
	writeAgentTestFile(t, source, "config.json", `{"skills": true}`)
	archivePath := createAgentTestArchive(t, source)

	target := t.TempDir()
	writeAgentTestFile(t, target, "skills/old/SKILL.md", "旧技能")
	writeAgentTestFile(t, target, "AGENTS.md", "旧指令")
	writeAgentTestFile(t, target, "config.json", "{}")

	files, _, err := restoreAgentCapabilities(archivePath, target, nil)
	if err != nil {
		t.Fatalf("restore agent capabilities: %v", err)
	}
	if files != 1 {
		t.Fatalf("restored files = %d, want 1", files)
	}
	if _, err := os.Stat(filepath.Join(target, "skills")); !os.IsNotExist(err) {
		t.Fatalf("old skills dir should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("old AGENTS.md should be removed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "config.json"))
	if err != nil || string(data) != `{"skills": true}` {
		t.Fatalf("config.json = %q, %v, want backup content", data, err)
	}
}

func buildAgentArchiveWithEntries(t *testing.T, entries map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	for name, content := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("create entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	return archivePath
}

func TestRestoreAgentCapabilitiesRejectsMaliciousEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries map[string]string
	}{
		{
			name:    "escape path",
			entries: map[string]string{"agent/../x": "bad"},
		},
		{
			name:    "unexpected top level",
			entries: map[string]string{"agent/other/x": "bad"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := buildAgentArchiveWithEntries(t, tc.entries)
			target := t.TempDir()
			writeAgentTestFile(t, target, "skills/demo/SKILL.md", "旧技能")
			writeAgentTestFile(t, target, "config.json", "{}")

			if _, _, err := restoreAgentCapabilities(archivePath, target, nil); err == nil {
				t.Fatalf("restore should fail for %#v", tc.entries)
			}
			// 原目录不应被破坏。
			data, err := os.ReadFile(filepath.Join(target, "skills/demo/SKILL.md"))
			if err != nil || string(data) != "旧技能" {
				t.Fatalf("existing skills should be intact, got %q, %v", data, err)
			}
		})
	}

	t.Run("symlink entry", func(t *testing.T) {
		archivePath := filepath.Join(t.TempDir(), "backup.zip")
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatalf("create archive: %v", err)
		}
		defer file.Close()
		zw := zip.NewWriter(file)
		header := &zip.FileHeader{Name: "agent/skills/demo/SKILL.md", Method: zip.Deflate, Modified: time.Now().UTC()}
		header.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create symlink entry: %v", err)
		}
		if _, err := w.Write([]byte("/etc/passwd")); err != nil {
			t.Fatalf("write symlink entry: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close archive writer: %v", err)
		}

		target := t.TempDir()
		writeAgentTestFile(t, target, "skills/demo/SKILL.md", "旧技能")
		if _, _, err := restoreAgentCapabilities(archivePath, target, nil); err == nil {
			t.Fatal("restore should reject symlink entry")
		}
		if _, err := os.Lstat(filepath.Join(target, "skills/demo/SKILL.md")); err != nil {
			t.Fatalf("existing skills should be intact: %v", err)
		}
	})
}

func TestRestoreAgentCapabilitiesRejectsOversizeEntry(t *testing.T) {
	source := t.TempDir()
	writeAgentTestFile(t, source, "skills/demo/SKILL.md", "技能正文")
	archivePath := createAgentTestArchive(t, source)

	oldMax := maxAgentFileBytes
	maxAgentFileBytes = 4
	defer func() { maxAgentFileBytes = oldMax }()

	if _, _, err := restoreAgentCapabilities(archivePath, t.TempDir(), nil); err == nil {
		t.Fatal("restore should reject oversize entry")
	}
}

func TestRestoreAgentCapabilitiesIgnoresOtherPrefixes(t *testing.T) {
	archivePath := buildAgentArchiveWithEntries(t, map[string]string{
		"playground/sessions/x.jsonl": "{}",
		"settings.json":               "{}",
	})
	target := t.TempDir()
	files, _, err := restoreAgentCapabilities(archivePath, target, nil)
	if err != nil {
		t.Fatalf("restore agent capabilities: %v", err)
	}
	if files != 0 {
		t.Fatalf("restored files = %d, want 0", files)
	}
	if _, err := os.Stat(filepath.Join(target, "playground")); !os.IsNotExist(err) {
		t.Fatalf("non-agent entry should be ignored: %v", err)
	}
}

func TestRestoreAgentCapabilitiesReportsProgress(t *testing.T) {
	source := t.TempDir()
	writeAgentTestFile(t, source, "skills/demo/SKILL.md", "技能正文")
	writeAgentTestFile(t, source, "AGENTS.md", "全局指令")
	archivePath := createAgentTestArchive(t, source)

	var lastTotal int64
	calls := 0
	files, bytes, err := restoreAgentCapabilities(archivePath, t.TempDir(), func(total int64) {
		calls++
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("restore agent capabilities: %v", err)
	}
	if files != 2 || calls != 2 || lastTotal != bytes {
		t.Fatalf("files = %d, calls = %d, lastTotal = %d, bytes = %d", files, calls, lastTotal, bytes)
	}
	if lastTotal == 0 || lastTotal != bytes {
		t.Fatal("progress total should match returned bytes")
	}
}
