package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedWorkspaceWorkDir(t *testing.T) {
	store := newTestStore(t)
	id := "00000000-0000-4000-8000-000000000001"
	otherID := "00000000-0000-4000-8000-000000000002"
	perSession := filepath.Join(store.root, id, workDirName)
	if got := store.WorkDir(id); got != perSession {
		t.Fatalf("未设置共享 workspace 时 WorkDir = %q, want %q", got, perSession)
	}

	workspace := filepath.Join(t.TempDir(), "shared")
	unclean := workspace + string(os.PathSeparator) + "."
	if got := store.SetWorkspace(unclean); got != store {
		t.Fatal("SetWorkspace 应返回 Store 自身")
	}
	for _, currentID := range []string{id, otherID} {
		if got := store.WorkDir(currentID); got != workspace {
			t.Errorf("设置共享 workspace 后 WorkDir(%s) = %q, want %q", currentID, got, workspace)
		}
	}
	if got := store.WorkDir("../invalid"); got != "" {
		t.Fatalf("非法 id 的 WorkDir = %q, want 空串", got)
	}

	store.SetWorkspace("")
	if got := store.WorkDir(id); got != perSession {
		t.Fatalf("清空共享 workspace 后 WorkDir = %q, want %q", got, perSession)
	}
}

func TestSharedWorkspaceEnsureDirs(t *testing.T) {
	store := newTestStore(t)
	workspace := filepath.Join(t.TempDir(), "shared")
	store.SetWorkspace(workspace)
	id := "00000000-0000-4000-8000-000000000001"
	if err := store.EnsureDirs(id); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, dir := range []string{store.Dir(id), store.PiDir(id), workspace} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录", dir)
		}
		if perm := info.Mode().Perm(); perm != 0o750 {
			t.Fatalf("%s 权限 = %o, want 750", dir, perm)
		}
	}
	oldDir := filepath.Join(store.Dir(id), workDirName)
	if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不应再创建会话内 workspace: %v", err)
	}
}

func TestSharedWorkspaceFork(t *testing.T) {
	store := newTestStore(t)
	workspace := filepath.Join(t.TempDir(), "shared")
	store.SetWorkspace(workspace)
	source := mustCreate(t, store)
	writePiFile(t, store, source.ID, "branch.jsonl", "branchess001")
	source = updateMeta(t, store, source.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
	})

	forked, err := store.Fork(source.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got := store.WorkDir(source.ID); got != workspace {
		t.Fatalf("源会话 WorkDir = %q, want %q", got, workspace)
	}
	if got := store.WorkDir(forked.ID); got != store.WorkDir(source.ID) {
		t.Fatalf("分叉会话 WorkDir = %q, 源会话 = %q", got, store.WorkDir(source.ID))
	}
	header, _, _ := forkedEntry(t, store, forked)
	if header["cwd"] != workspace {
		t.Fatalf("分叉文件头 cwd = %v, want %s", header["cwd"], workspace)
	}
}

func TestMigrateLegacyWorkspaces(t *testing.T) {
	t.Run("非空目录迁移且幂等", func(t *testing.T) {
		store := newTestStore(t)
		withFile := mustCreate(t, store)
		empty := mustCreate(t, store)
		oldDir := filepath.Join(store.Dir(withFile.ID), workDirName)
		emptyDir := filepath.Join(store.Dir(empty.ID), workDirName)
		if err := os.WriteFile(filepath.Join(oldDir, "note.txt"), []byte("保留内容"), 0o600); err != nil {
			t.Fatalf("写入旧 workspace 文件: %v", err)
		}
		workspace := filepath.Join(t.TempDir(), "shared")
		store.SetWorkspace(workspace)

		moved, err := store.MigrateLegacyWorkspaces()
		if err != nil || moved != 1 {
			t.Fatalf("MigrateLegacyWorkspaces = (%d, %v), want (1, nil)", moved, err)
		}
		if _, err := os.Stat(oldDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("非空旧 workspace 应被移走: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "_legacy", withFile.ID, "note.txt"))
		if err != nil || string(data) != "保留内容" {
			t.Fatalf("迁移后的文件内容 = %q, err = %v", data, err)
		}
		if info, err := os.Stat(emptyDir); err != nil || !info.IsDir() {
			t.Fatalf("空旧 workspace 应保持不动: info = %v, err = %v", info, err)
		}
		if _, err := os.Stat(filepath.Join(workspace, "_legacy", empty.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("空旧 workspace 不应生成迁移目标: %v", err)
		}

		moved, err = store.MigrateLegacyWorkspaces()
		if err != nil || moved != 0 {
			t.Fatalf("再次迁移 = (%d, %v), want (0, nil)", moved, err)
		}
	})

	t.Run("目标已存在不阻断其它会话", func(t *testing.T) {
		store := newTestStore(t)
		blocked := mustCreate(t, store)
		migratable := mustCreate(t, store)
		for _, meta := range []Meta{blocked, migratable} {
			path := filepath.Join(store.Dir(meta.ID), workDirName, "note.txt")
			if err := os.WriteFile(path, []byte(meta.ID), 0o600); err != nil {
				t.Fatalf("写入旧 workspace 文件: %v", err)
			}
		}
		workspace := filepath.Join(t.TempDir(), "shared")
		store.SetWorkspace(workspace)
		target := filepath.Join(workspace, "_legacy", blocked.ID)
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatalf("创建已存在的迁移目标: %v", err)
		}

		moved, err := store.MigrateLegacyWorkspaces()
		if err == nil || moved != 1 {
			t.Fatalf("MigrateLegacyWorkspaces = (%d, %v), want (1, 非 nil)", moved, err)
		}
		blockedFile := filepath.Join(store.Dir(blocked.ID), workDirName, "note.txt")
		if data, readErr := os.ReadFile(blockedFile); readErr != nil || string(data) != blocked.ID {
			t.Fatalf("目标冲突时会话旧目录不应移动: 内容 = %q, err = %v", data, readErr)
		}
		if entries, readErr := os.ReadDir(target); readErr != nil || len(entries) != 0 {
			t.Fatalf("已有迁移目标不应改动: entries = %v, err = %v", entries, readErr)
		}
		migratedFile := filepath.Join(workspace, "_legacy", migratable.ID, "note.txt")
		if data, readErr := os.ReadFile(migratedFile); readErr != nil || string(data) != migratable.ID {
			t.Fatalf("其它会话仍应迁移: 内容 = %q, err = %v", data, readErr)
		}
	})

	t.Run("未设置共享目录", func(t *testing.T) {
		store := newTestStore(t)
		meta := mustCreate(t, store)
		path := filepath.Join(store.Dir(meta.ID), workDirName, "note.txt")
		if err := os.WriteFile(path, []byte("不迁移"), 0o600); err != nil {
			t.Fatalf("写入旧 workspace 文件: %v", err)
		}
		moved, err := store.MigrateLegacyWorkspaces()
		if err != nil || moved != 0 {
			t.Fatalf("MigrateLegacyWorkspaces = (%d, %v), want (0, nil)", moved, err)
		}
		if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "不迁移" {
			t.Fatalf("未设置共享目录时旧文件应保持不动: 内容 = %q, err = %v", data, readErr)
		}
	})

	t.Run("root 不存在", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "missing"))
		store.SetWorkspace(filepath.Join(t.TempDir(), "shared"))
		moved, err := store.MigrateLegacyWorkspaces()
		if err != nil || moved != 0 {
			t.Fatalf("MigrateLegacyWorkspaces = (%d, %v), want (0, nil)", moved, err)
		}
	})
}
