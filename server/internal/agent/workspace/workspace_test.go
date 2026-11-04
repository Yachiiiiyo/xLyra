package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRulesAndReuse(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".git", "node_modules", ".cache", "__pycache__", ".xlyra"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "bad.txt"), []byte("bad"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.tmp-1"), []byte("tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "a.txt"), filepath.Join(root, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	first, err := Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 0 { // a.txt 因硬链接也应跳过
		t.Fatalf("意外条目: %#v", first.Entries)
	}
	if err := os.Remove(filepath.Join(root, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	first, err = Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := first.Entries["a.txt"]
	if !e.Text || e.SHA256 == "" {
		t.Fatalf("文本识别失败: %#v", e)
	}
	info, _ := os.Stat(filepath.Join(root, "a.txt"))
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "a.txt"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	second, err := Scan(context.Background(), root, first)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entries["a.txt"].SHA256 != e.SHA256 {
		t.Fatal("size+mtime 相同时没有复用哈希")
	}
}

func TestScanLimitsAndCancel(t *testing.T) {
	root := t.TempDir()
	path := root
	for i := 0; i < MaxDepth+2; i++ {
		path = filepath.Join(path, "d")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Scan(context.Background(), root, nil)
	if err != nil || !m.Truncated {
		t.Fatalf("深度限制: %#v %v", m, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, err = Scan(ctx, root, nil)
	if err != nil || !m.Truncated {
		t.Fatalf("取消: %#v %v", m, err)
	}
}

func TestManifestDiffAndStore(t *testing.T) {
	before := &Manifest{Entries: map[string]Entry{
		"b.txt": {Path: "b.txt", Size: 1, SHA256: "old"},
		"c.txt": {Path: "c.txt", Size: 1, SHA256: "same"},
	}}
	after := &Manifest{Entries: map[string]Entry{
		"a.txt": {Path: "a.txt", Size: 1},
		"b.txt": {Path: "b.txt", Size: 1, SHA256: "new"},
	}}
	changes := Diff(before, after)
	if len(changes) != 3 || changes[0].Kind != "added" || changes[1].Kind != "modified" || changes[2].Kind != "deleted" {
		t.Fatalf("差集错误: %#v", changes)
	}
	manifestPath := filepath.Join(t.TempDir(), "state", "manifest.json")
	if err := after.Save(manifestPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(manifestPath)
	if err != nil || len(loaded.Entries) != 2 {
		t.Fatalf("清单存取: %#v %v", loaded, err)
	}
	missing, err := LoadManifest(manifestPath + ".missing")
	if err != nil || missing != nil {
		t.Fatal("缺失清单应返回 nil,nil")
	}

	root := t.TempDir()
	content := []byte("hello\n")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	store := NewStore(filepath.Join(t.TempDir(), "blobs"))
	ok, err := store.Put(root, Entry{Path: "a.txt", Text: true, SHA256: strings.Repeat("0", 64)})
	if err != nil || ok {
		t.Fatal("哈希不符不应落盘")
	}
	ok, err = store.Put(root, Entry{Path: "a.txt", Text: true, SHA256: sha})
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got, ok := store.Get(sha); !ok || string(got) != string(content) {
		t.Fatal("读取快照失败")
	}
	if _, ok := store.Get("BAD"); ok {
		t.Fatal("非法哈希不应可读")
	}
	if err := store.Prune(map[string]bool{}); err != nil || store.Size() != 0 {
		t.Fatal("清理失败")
	}
}

func TestStoreLimit(t *testing.T) {
	store := NewStore(t.TempDir())
	filler := strings.Repeat("f", 64)
	if err := os.WriteFile(filepath.Join(store.dir, filler), make([]byte, StoreMaxBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	content := []byte("x")
	if err := os.WriteFile(filepath.Join(root, "x.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	ok, err := store.Put(root, Entry{Path: "x.txt", Text: true, SHA256: hex.EncodeToString(sum[:])})
	if err != nil || ok {
		t.Fatal("超过存储上限应跳过")
	}
}

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name, old, new, want string
		a, d                 int
	}{
		{"新增", "", "a\nb\n", "--- x\n+++ x\n@@ -0,0 +1,2 @@\n+a\n+b\n", 2, 0},
		{"删除", "a\nb\n", "", "--- x\n+++ x\n@@ -1,2 +0,0 @@\n-a\n-b\n", 0, 2},
		{"修改", "a\nb\nc\n", "a\nB\nc\n", "--- x\n+++ x\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n", 1, 1},
		{"无差异", "a\r\n", "a\r\n", "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, a, d, truncated := UnifiedDiff("x", tt.old, tt.new)
			if got != tt.want || a != tt.a || d != tt.d || truncated {
				t.Fatalf("got %q %d %d %v", got, a, d, truncated)
			}
			if a2, d2 := Stats(tt.old, tt.new); a2 != a || d2 != d {
				t.Fatal("Stats 不一致")
			}
		})
	}
	old := strings.Repeat("old\n", 2100)
	patch, a, d, truncated := UnifiedDiff("x", old, "")
	if !truncated || a != 0 || d != 2100 || len(patch) > 200<<10 {
		t.Fatalf("截断失败: %d %d %v %d", a, d, truncated, len(patch))
	}
}

func TestEntryLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxEntries+1; i++ {
		name := filepath.Join(root, fmt.Sprintf("%04d.txt", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := Scan(context.Background(), root, nil)
	if err != nil || !m.Truncated || len(m.Entries) != MaxEntries {
		t.Fatalf("条目限制: %d %v %v", len(m.Entries), m.Truncated, err)
	}
}
