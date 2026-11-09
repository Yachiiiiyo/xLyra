package backup

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func writeSessionTestFile(t *testing.T, root string, relPath string, content string) {
	t.Helper()
	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatalf("create test file dir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

// seedAgentSession 造一个会话目录:meta.json(含 slot)、pi/ 下带文件头的
// jsonl 与另一文件、.xlyra/ 快照,以及不应进备份的 pi-agent/、exec-audit、
// workspace/。
func seedAgentSession(t *testing.T, root, id, piBody string) {
	t.Helper()
	writeSessionTestFile(t, root, "sessions/"+id+"/meta.json", `{"id":"`+id+`","title":"t","slot":3,"model":"m"}`)
	header := `{"type":"session","version":3,"id":"x","cwd":"/old/path"}`
	writeSessionTestFile(t, root, "sessions/"+id+"/pi/2024-01-01T00-00-00-000Z_x.jsonl", header+"\n"+piBody)
	writeSessionTestFile(t, root, "sessions/"+id+"/pi/other.json", `{"a":1}`)
	writeSessionTestFile(t, root, "sessions/"+id+"/.xlyra/blobs/ab/cd123", "快照内容")
	// 不应进备份的内容。
	writeSessionTestFile(t, root, "sessions/"+id+"/pi-agent/token", "secret")
	writeSessionTestFile(t, root, "sessions/"+id+"/exec-audit.jsonl", "{}")
	writeSessionTestFile(t, root, "sessions/"+id+"/workspace/f.txt", "工作区")
}

// createAgentSessionsArchive 把 source root 的会话导出到临时 zip 文件。
func createAgentSessionsArchive(t *testing.T, source string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	zw := zip.NewWriter(file)
	if _, err := exportAgentSessions(zw, source); err != nil {
		t.Fatalf("export agent sessions: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return archivePath
}

func TestExportAndRestoreAgentSessionsRoundTrip(t *testing.T) {
	id1 := uuid.NewString()
	id2 := uuid.NewString()
	source := t.TempDir()
	seedAgentSession(t, source, id1, `{"type":"message","id":"e1"}`+"\n"+`{"type":"message","id":"e2"}`)
	seedAgentSession(t, source, id2, `{"type":"message","id":"e3"}`)

	archivePath := createAgentSessionsArchive(t, source)

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	names := make(map[string]bool)
	for _, entry := range zr.File {
		names[entry.Name] = true
	}
	zr.Close()
	for _, want := range []string{
		"agent_sessions/" + id1 + "/meta.json",
		"agent_sessions/" + id1 + "/pi/2024-01-01T00-00-00-000Z_x.jsonl",
		"agent_sessions/" + id1 + "/pi/other.json",
		"agent_sessions/" + id1 + "/.xlyra/blobs/ab/cd123",
		"agent_sessions/" + id2 + "/meta.json",
	} {
		if !names[want] {
			t.Fatalf("archive entry %s missing, got %#v", want, names)
		}
	}
	for _, unwanted := range []string{
		"agent_sessions/" + id1 + "/pi-agent/token",
		"agent_sessions/" + id1 + "/exec-audit.jsonl",
		"agent_sessions/" + id1 + "/workspace/f.txt",
	} {
		if names[unwanted] {
			t.Fatalf("archive entry %s should be skipped", unwanted)
		}
	}

	// 目标 root:预置旧会话内容与一个备份里没有的会话。
	keptID := uuid.NewString()
	target := t.TempDir()
	writeSessionTestFile(t, target, "sessions/"+id1+"/meta.json", `{"id":"`+id1+`","old":true}`)
	writeSessionTestFile(t, target, "sessions/"+id1+"/pi/old.jsonl", "旧会话文件")
	writeSessionTestFile(t, target, "sessions/"+id1+"/pi-agent/token", "旧令牌")
	writeSessionTestFile(t, target, "sessions/"+id2+"/exec/output.txt", "运行产物")
	writeSessionTestFile(t, target, "sessions/"+keptID+"/meta.json", `{"id":"`+keptID+`","kept":true}`)
	writeSessionTestFile(t, target, "sessions/"+keptID+"/pi/a.jsonl", "保留会话")

	workspaceDir := "/new/workspace"
	sessions, files, _, err := restoreAgentSessions(archivePath, target, workspaceDir, nil)
	if err != nil {
		t.Fatalf("restore agent sessions: %v", err)
	}
	if sessions != 2 {
		t.Fatalf("restored sessions = %d, want 2", sessions)
	}
	if files != 8 {
		t.Fatalf("restored files = %d, want 8", files)
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
	// meta.json:slot 被删除,其余内容保留。
	assertFileContent("sessions/"+id1+"/meta.json", `{"id":"`+id1+`","model":"m","title":"t"}`)
	// pi 文件头 cwd 改写为新 workspace,其余行原样。
	jsonl := filepath.Join(target, "sessions", id1, "pi", "2024-01-01T00-00-00-000Z_x.jsonl")
	data, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("read restored jsonl: %v", err)
	}
	lines := strings.SplitN(strings.TrimSuffix(string(data), "\n"), "\n", 2)
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("parse restored header: %v (line=%q)", err, lines[0])
	}
	if header["cwd"] != workspaceDir {
		t.Fatalf("header cwd = %v, want %q", header["cwd"], workspaceDir)
	}
	if header["id"] != "x" || header["version"] != float64(3) || header["type"] != "session" {
		t.Fatalf("header other fields changed: %q", lines[0])
	}
	wantRest := `{"type":"message","id":"e1"}` + "\n" + `{"type":"message","id":"e2"}`
	if lines[1] != wantRest {
		t.Fatalf("remaining lines = %q, want %q", lines[1], wantRest)
	}
	assertFileContent("sessions/"+id1+"/pi/other.json", `{"a":1}`)
	assertFileContent("sessions/"+id1+"/.xlyra/blobs/ab/cd123", "快照内容")
	assertFileContent("sessions/"+id2+"/meta.json", `{"id":"`+id2+`","model":"m","title":"t"}`)

	// 旧 pi 文件被替换;备份里没有的会话原样保留;pi-agent/ 保留。
	if _, err := os.Stat(filepath.Join(target, "sessions", id1, "pi", "old.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("old pi file should be replaced: %v", err)
	}
	assertFileContent("sessions/"+keptID+"/pi/a.jsonl", "保留会话")
	assertFileContent("sessions/"+id1+"/pi-agent/token", "旧令牌")
	assertFileContent("sessions/"+id2+"/exec/output.txt", "运行产物")

	// 权限:会话目录与 pi/、.xlyra/ 目录 0o750,文件 0o600。
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
	assertPerm("sessions/"+id1, 0o750)
	assertPerm("sessions/"+id1+"/pi", 0o750)
	assertPerm("sessions/"+id1+"/.xlyra", 0o750)
	assertPerm("sessions/"+id1+"/.xlyra/blobs", 0o750)
	assertPerm("sessions/"+id1+"/meta.json", 0o600)
	assertPerm("sessions/"+id1+"/pi/2024-01-01T00-00-00-000Z_x.jsonl", 0o600)
	assertPerm("sessions/"+id1+"/.xlyra/blobs/ab/cd123", 0o600)
}

func TestExportAgentSessionsSkipsSymlinksTempFilesAndBadDirs(t *testing.T) {
	id := uuid.NewString()
	source := t.TempDir()
	seedAgentSession(t, source, id, `{"type":"message"}`)
	writeSessionTestFile(t, source, "sessions/"+id+"/pi/.x.tmp-1", "临时内容")
	writeSessionTestFile(t, source, "sessions/not-a-uuid/meta.json", `{"x":1}`)
	writeSessionTestFile(t, source, "sessions/"+strings.ToUpper(id)+"/meta.json", `{"x":1}`)
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "sessions", id, "pi", "link.jsonl")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	archivePath := createAgentSessionsArchive(t, source)

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if strings.Contains(entry.Name, "pi-agent") || strings.Contains(entry.Name, "exec") ||
			strings.Contains(entry.Name, "workspace") || strings.Contains(entry.Name, "tmp-") ||
			entry.Name == "agent_sessions/not-a-uuid/meta.json" ||
			entry.Name == "agent_sessions/"+strings.ToUpper(id)+"/meta.json" ||
			strings.HasSuffix(entry.Name, "link.jsonl") {
			t.Fatalf("archive entry %s should be skipped", entry.Name)
		}
	}
}

func buildAgentSessionsArchiveWithEntries(t *testing.T, entries map[string]string) string {
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

func TestRestoreAgentSessionsRejectsMaliciousEntries(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		name    string
		entries map[string]string
	}{
		{
			name:    "escape path",
			entries: map[string]string{"agent_sessions/../x": "bad"},
		},
		{
			name:    "non uuid dir",
			entries: map[string]string{"agent_sessions/not-a-uuid/meta.json": "bad"},
		},
		{
			name:    "unknown top level",
			entries: map[string]string{"agent_sessions/" + id + "/pi-agent/x": "bad"},
		},
		{
			name:    "backslash",
			entries: map[string]string{"agent_sessions/" + id + `\/../meta.json`: "bad"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := buildAgentSessionsArchiveWithEntries(t, tc.entries)
			target := t.TempDir()
			writeSessionTestFile(t, target, "sessions/"+id+"/meta.json", "旧内容")
			writeSessionTestFile(t, target, "sessions/"+id+"/pi/a.jsonl", "旧 pi")

			if _, _, _, err := restoreAgentSessions(archivePath, target, "", nil); err == nil {
				t.Fatalf("restore should fail for %#v", tc.entries)
			}
			data, err := os.ReadFile(filepath.Join(target, "sessions", id, "meta.json"))
			if err != nil || string(data) != "旧内容" {
				t.Fatalf("existing meta should be intact, got %q, %v", data, err)
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
		header := &zip.FileHeader{Name: "agent_sessions/" + id + "/pi/a.jsonl", Method: zip.Deflate, Modified: time.Now().UTC()}
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
		writeSessionTestFile(t, target, "sessions/"+id+"/pi/a.jsonl", "旧 pi")
		if _, _, _, err := restoreAgentSessions(archivePath, target, "", nil); err == nil {
			t.Fatal("restore should reject symlink entry")
		}
		if _, err := os.Lstat(filepath.Join(target, "sessions", id, "pi", "a.jsonl")); err != nil {
			t.Fatalf("existing pi file should be intact: %v", err)
		}
	})
}

func TestRestoreAgentSessionsRejectsOversizeEntry(t *testing.T) {
	id := uuid.NewString()
	source := t.TempDir()
	seedAgentSession(t, source, id, `{"type":"message"}`)
	archivePath := createAgentSessionsArchive(t, source)

	oldMax := maxAgentSessionFileBytes
	maxAgentSessionFileBytes = 4
	defer func() { maxAgentSessionFileBytes = oldMax }()

	if _, _, _, err := restoreAgentSessions(archivePath, t.TempDir(), "", nil); err == nil {
		t.Fatal("restore should reject oversize entry")
	}
}

func TestRestoreAgentSessionsIgnoresOtherPrefixes(t *testing.T) {
	archivePath := buildAgentSessionsArchiveWithEntries(t, map[string]string{
		"agent/skills/demo/SKILL.md": "能力",
		"settings.json":              "{}",
	})
	target := t.TempDir()
	sessions, files, _, err := restoreAgentSessions(archivePath, target, "", nil)
	if err != nil {
		t.Fatalf("restore agent sessions: %v", err)
	}
	if sessions != 0 || files != 0 {
		t.Fatalf("sessions = %d, files = %d, want 0, 0", sessions, files)
	}
	if _, err := os.Stat(filepath.Join(target, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("sessions dir should not be created: %v", err)
	}
}

func TestRestoreAgentSessionsReportsProgress(t *testing.T) {
	id := uuid.NewString()
	source := t.TempDir()
	seedAgentSession(t, source, id, `{"type":"message"}`)
	archivePath := createAgentSessionsArchive(t, source)

	var lastTotal int64
	calls := 0
	_, files, bytes, err := restoreAgentSessions(archivePath, t.TempDir(), "", func(total int64) {
		calls++
		lastTotal = total
	})
	if err != nil {
		t.Fatalf("restore agent sessions: %v", err)
	}
	if files != 4 || calls != 4 || lastTotal != bytes {
		t.Fatalf("files = %d, calls = %d, lastTotal = %d, bytes = %d", files, calls, lastTotal, bytes)
	}
}

func TestRestoreAgentSessionsRollbackOnSwapFailure(t *testing.T) {
	id1 := uuid.NewString()
	id2 := uuid.NewString()
	source := t.TempDir()
	seedAgentSession(t, source, id1, `{"type":"message"}`)
	seedAgentSession(t, source, id2, `{"type":"message"}`)
	archivePath := createAgentSessionsArchive(t, source)

	// 目标里两个会话都有旧内容。
	target := t.TempDir()
	writeSessionTestFile(t, target, "sessions/"+id1+"/meta.json", "旧meta1")
	writeSessionTestFile(t, target, "sessions/"+id1+"/pi/a.jsonl", "旧pi1")
	writeSessionTestFile(t, target, "sessions/"+id2+"/meta.json", "旧meta2")

	// id1 < id2:让 id1 正常完成,id2 的 set aside 阶段失败。
	oldRename := renameFn
	renameFn = func(oldPath, newPath string) error {
		if strings.Contains(oldPath, id2) && strings.Contains(newPath, ".trash-sessions-") {
			return os.ErrPermission
		}
		return oldRename(oldPath, newPath)
	}
	defer func() { renameFn = oldRename }()

	if _, _, _, err := restoreAgentSessions(archivePath, target, "", nil); err == nil {
		t.Fatal("restore should fail when swap fails")
	}
	// id1 应回滚到旧内容。
	data, err := os.ReadFile(filepath.Join(target, "sessions", id1, "meta.json"))
	if err != nil || string(data) != "旧meta1" {
		t.Fatalf("session 1 meta should be rolled back, got %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(target, "sessions", id1, "pi", "a.jsonl"))
	if err != nil || string(data) != "旧pi1" {
		t.Fatalf("session 1 pi should be rolled back, got %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(target, "sessions", id2, "meta.json"))
	if err != nil || string(data) != "旧meta2" {
		t.Fatalf("session 2 meta should be intact, got %q, %v", data, err)
	}
	// 不留 trash 与 staging 残留。
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".trash-") || strings.HasPrefix(e.Name(), ".restore-staging-") {
			t.Fatalf("residual dir %s should be removed", e.Name())
		}
	}
}
