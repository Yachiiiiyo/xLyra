package backup

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeWorkspaceTestFile(t *testing.T, root string, relPath string, content string, perm os.FileMode) {
	t.Helper()
	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create test file dir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte(content), perm); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

// createWorkspaceTestArchive 把 source root 的工作区导出到临时 zip 文件。
func createWorkspaceTestArchive(t *testing.T, source string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	if _, err := exportAgentWorkspace(zw, source); err != nil {
		t.Fatalf("export agent workspace: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	return archivePath
}

func TestExportAndRestoreAgentWorkspaceRoundTrip(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceTestFile(t, source, "readme.md", "普通文件", 0o644)
	writeWorkspaceTestFile(t, source, "docs/guide.md", "子目录文件", 0o644)
	writeWorkspaceTestFile(t, source, "run.sh", "#!/bin/sh\n", 0o755)
	writeWorkspaceTestFile(t, source, ".git/config", "[core]", 0o644)
	writeWorkspaceTestFile(t, source, "node_modules/x/y.js", "依赖", 0o644)
	writeWorkspaceTestFile(t, source, ".cache/z", "缓存", 0o644)
	writeWorkspaceTestFile(t, source, "sub/__pycache__/a.pyc", "字节码", 0o644)
	if err := os.Symlink("readme.md", filepath.Join(source, "link.md")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatalf("create empty dir: %v", err)
	}

	archivePath := createWorkspaceTestArchive(t, source)

	// 检查导出内容:跳过目录与符号链接,保留 .git 与可执行权限位。
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	names := make(map[string]bool)
	for _, entry := range zr.File {
		names[entry.Name] = true
		if strings.Contains(entry.Name, "node_modules") || strings.Contains(entry.Name, ".cache/") ||
			strings.Contains(entry.Name, "__pycache__") || entry.Name == "agent_workspace/link.md" {
			t.Fatalf("archive entry %s should be skipped", entry.Name)
		}
		if entry.Name == "agent_workspace/run.sh" && entry.Mode().Perm() != 0o755 {
			t.Fatalf("perm of run.sh = %o, want 755", entry.Mode().Perm())
		}
	}
	if !names["agent_workspace/.git/config"] {
		t.Fatalf("archive entry .git/config missing, got %#v", names)
	}

	// 目标先放一个同名旧文件与一个备份里没有的文件。
	target := t.TempDir()
	writeWorkspaceTestFile(t, target, "readme.md", "旧内容", 0o644)
	writeWorkspaceTestFile(t, target, "extra.txt", "额外文件", 0o644)

	files, _, err := restoreAgentWorkspace(archivePath, target, nil)
	if err != nil {
		t.Fatalf("restore agent workspace: %v", err)
	}
	if files != 4 {
		t.Fatalf("restored files = %d, want 4", files)
	}
	assertContent := func(relPath, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(relPath)))
		if err != nil {
			t.Fatalf("read restored %s: %v", relPath, err)
		}
		if string(data) != want {
			t.Fatalf("restored %s = %q, want %q", relPath, data, want)
		}
	}
	assertContent("readme.md", "普通文件")
	assertContent("docs/guide.md", "子目录文件")
	assertContent(".git/config", "[core]")
	assertContent("extra.txt", "额外文件")

	info, err := os.Stat(filepath.Join(target, "run.sh"))
	if err != nil {
		t.Fatalf("stat restored run.sh: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm of restored run.sh = %o, want 755", info.Mode().Perm())
	}
}

func TestRestoreAgentWorkspaceRejectsMaliciousEntries(t *testing.T) {
	// 逐条验证路径清洗:拒绝越界、绝对路径、反斜杠。
	for _, name := range []string{
		"agent_workspace/../x",
		"agent_workspace//x",
		`agent_workspace/a\b`,
	} {
		if _, _, err := cleanArchiveAgentWorkspacePath(name); err == nil {
			t.Fatalf("path %q should be rejected", name)
		}
	}

	malicious := []string{
		"agent_workspace/../x",
		"agent_workspace//x",
		`agent_workspace/a\b`,
	}
	for _, entryName := range malicious {
		archivePath := buildWorkspaceArchiveWithEntries(t, map[string]string{entryName: "bad"})
		target := t.TempDir()
		writeWorkspaceTestFile(t, target, "keep.txt", "原内容", 0o644)
		if _, _, err := restoreAgentWorkspace(archivePath, target, nil); err == nil {
			t.Fatalf("restore should reject entry %q", entryName)
		}
		data, err := os.ReadFile(filepath.Join(target, "keep.txt"))
		if err != nil || string(data) != "原内容" {
			t.Fatalf("existing file should be intact, got %q, %v", data, err)
		}
	}

	// 符号链接条目。
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "agent_workspace/link", Method: zip.Deflate, Modified: time.Now().UTC()}
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
	writeWorkspaceTestFile(t, target, "keep.txt", "原内容", 0o644)
	if _, _, err := restoreAgentWorkspace(archivePath, target, nil); err == nil {
		t.Fatal("restore should reject symlink entry")
	}
	if _, err := os.Lstat(filepath.Join(target, "link")); !os.IsNotExist(err) {
		t.Fatalf("symlink should not be created: %v", err)
	}
}

func TestRestoreAgentWorkspaceTargetDirBlockedByFile(t *testing.T) {
	// 目标路径已有目录而条目是文件:报错且目录内容不变。
	archivePath := buildWorkspaceArchiveWithEntries(t, map[string]string{"agent_workspace/a.txt": "新内容"})
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "a.txt"), 0o755); err != nil {
		t.Fatalf("create blocking dir: %v", err)
	}
	writeWorkspaceTestFile(t, target, "a.txt/inner.txt", "目录内容", 0o644)

	if _, _, err := restoreAgentWorkspace(archivePath, target, nil); err == nil {
		t.Fatal("restore should fail when target is a directory")
	}
	data, err := os.ReadFile(filepath.Join(target, "a.txt/inner.txt"))
	if err != nil || string(data) != "目录内容" {
		t.Fatalf("blocking dir content should be intact, got %q, %v", data, err)
	}

	// 父路径上有普通文件挡着。
	archivePath2 := buildWorkspaceArchiveWithEntries(t, map[string]string{"agent_workspace/b/c.txt": "新内容"})
	target2 := t.TempDir()
	writeWorkspaceTestFile(t, target2, "b", "挡路文件", 0o644)
	if _, _, err := restoreAgentWorkspace(archivePath2, target2, nil); err == nil {
		t.Fatal("restore should fail when parent is a regular file")
	}
	data, err = os.ReadFile(filepath.Join(target2, "b"))
	if err != nil || string(data) != "挡路文件" {
		t.Fatalf("blocking file should be intact, got %q, %v", data, err)
	}
}

func TestRestoreAgentWorkspaceRejectsOversize(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceTestFile(t, source, "big.txt", strings.Repeat("a", 32), 0o644)
	archivePath := createWorkspaceTestArchive(t, source)

	oldFile, oldTotal := maxAgentWorkspaceFileBytes, maxAgentWorkspaceTotalBytes
	defer func() {
		maxAgentWorkspaceFileBytes = oldFile
		maxAgentWorkspaceTotalBytes = oldTotal
	}()

	maxAgentWorkspaceFileBytes = 4
	if _, _, err := restoreAgentWorkspace(archivePath, t.TempDir(), nil); err == nil {
		t.Fatal("restore should reject oversize file")
	}
	maxAgentWorkspaceFileBytes = oldFile

	// 多个文件累计超总量上限。
	source2 := t.TempDir()
	writeWorkspaceTestFile(t, source2, "a.txt", "aaaa", 0o644)
	writeWorkspaceTestFile(t, source2, "b.txt", "bbbb", 0o644)
	archivePath2 := createWorkspaceTestArchive(t, source2)
	maxAgentWorkspaceTotalBytes = 4
	if _, _, err := restoreAgentWorkspace(archivePath2, t.TempDir(), nil); err == nil {
		t.Fatal("restore should reject oversize total")
	}
	maxAgentWorkspaceTotalBytes = oldTotal

	// 导出侧超限。
	maxAgentWorkspaceFileBytes = 4
	if _, err := exportAgentWorkspace(zip.NewWriter(io.Discard), source); err == nil {
		t.Fatal("export should reject oversize file")
	}
	maxAgentWorkspaceFileBytes = oldFile

	source4 := t.TempDir()
	writeWorkspaceTestFile(t, source4, "a.txt", "aaaa", 0o644)
	writeWorkspaceTestFile(t, source4, "b.txt", "bbbb", 0o644)
	maxAgentWorkspaceTotalBytes = 4
	if _, err := exportAgentWorkspace(zip.NewWriter(io.Discard), source4); err == nil {
		t.Fatal("export should reject oversize total")
	}
}

func TestRestoreAgentWorkspaceIgnoresOtherPrefixes(t *testing.T) {
	archivePath := buildWorkspaceArchiveWithEntries(t, map[string]string{
		"agent/skills/demo/SKILL.md": "技能",
		"settings.json":              "{}",
	})
	target := t.TempDir()
	files, _, err := restoreAgentWorkspace(archivePath, target, nil)
	if err != nil {
		t.Fatalf("restore agent workspace: %v", err)
	}
	if files != 0 {
		t.Fatalf("restored files = %d, want 0", files)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read target dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("target should stay empty, got %d entries", len(entries))
	}
}

func TestExportAgentWorkspaceSkipsTempFilesAndMissingRoot(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceTestFile(t, source, "readme.md", "正文", 0o644)
	writeWorkspaceTestFile(t, source, ".readme.md.tmp-123", "临时", 0o644)
	writeWorkspaceTestFile(t, source, "sub/.data.restore-tmp-9", "临时", 0o644)

	archivePath := createWorkspaceTestArchive(t, source)
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if strings.Contains(entry.Name, "tmp-") {
			t.Fatalf("archive entry %s should be skipped", entry.Name)
		}
	}

	// root 不存在返回 0,nil。
	files, err := exportAgentWorkspace(zip.NewWriter(io.Discard), filepath.Join(t.TempDir(), "missing"))
	if err != nil || files != 0 {
		t.Fatalf("export missing root = (%d, %v), want (0, nil)", files, err)
	}
}

func TestRestoreAgentWorkspaceReportsProgressAndCleansTemp(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceTestFile(t, source, "a.txt", "aaaa", 0o644)
	writeWorkspaceTestFile(t, source, "b/c.txt", "cc", 0o644)
	archivePath := createWorkspaceTestArchive(t, source)

	var lastTotal int64
	calls := 0
	files, bytes, err := restoreAgentWorkspace(archivePath, t.TempDir(), func(total int64) {
		calls++
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("restore agent workspace: %v", err)
	}
	if files != 2 || calls != 2 || lastTotal != bytes || bytes != 6 {
		t.Fatalf("files = %d, calls = %d, lastTotal = %d, bytes = %d", files, calls, lastTotal, bytes)
	}

	// 恢复失败时不留下 .restore-tmp-* 临时文件:用超限触发失败。
	oldFile := maxAgentWorkspaceFileBytes
	maxAgentWorkspaceFileBytes = 2
	defer func() { maxAgentWorkspaceFileBytes = oldFile }()
	target := t.TempDir()
	if _, _, err := restoreAgentWorkspace(archivePath, target, nil); err == nil {
		t.Fatal("restore should fail on oversize")
	}
	assertNoTempFiles(t, target)
}

// assertNoTempFiles 检查目录树里没有遗留 .restore-tmp-* 临时文件。
func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), "restore-tmp-") {
			t.Fatalf("temp file %s should be cleaned up", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk target dir: %v", err)
	}
}

func buildWorkspaceArchiveWithEntries(t *testing.T, entries map[string]string) string {
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
