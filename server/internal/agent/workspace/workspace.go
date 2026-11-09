// Package workspace 提供工作区文件清单、内容快照与文本差异。
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	MaxEntries    = 5000
	MaxDepth      = 12
	HashMaxBytes  = 8 << 20
	BlobMaxBytes  = 256 << 10
	StoreMaxBytes = 32 << 20
)

type Entry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModNano int64  `json:"mod_nano"`
	Mode    uint32 `json:"mode"`
	SHA256  string `json:"sha256,omitempty"`
	Text    bool   `json:"text"`
	Binary  bool   `json:"binary"`
}

type Manifest struct {
	Entries   map[string]Entry `json:"entries"`
	Truncated bool             `json:"truncated"`
}

var textExtensions = map[string]bool{
	"md": true, "markdown": true, "txt": true, "log": true, "json": true,
	"yaml": true, "yml": true, "toml": true, "csv": true, "tsv": true,
	"html": true, "htm": true, "css": true, "js": true, "mjs": true,
	"ts": true, "tsx": true, "jsx": true, "py": true, "go": true,
	"rs": true, "java": true, "c": true, "h": true, "cpp": true,
	"sh": true, "sql": true, "xml": true, "svg": true, "ini": true,
	"conf": true, "env": true,
}

func textCandidate(path string) bool {
	name := filepath.Base(path)
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if ext == "" {
		switch name {
		case "Makefile", "Dockerfile", "README", "LICENSE":
			return true
		default:
			return false
		}
	}
	return textExtensions[strings.ToLower(ext)]
}

func textContent(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}

func openRegular(root, rel string) (*os.File, os.FileInfo, bool) {
	file, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(rel)), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, false
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		file.Close()
		return nil, nil, false
	}
	return file, info, true
}

// Scan 遍历工作区;取消或达到限制时返回已扫描的部分清单。
func Scan(ctx context.Context, root string, prev *Manifest) (*Manifest, error) {
	manifest := &Manifest{Entries: map[string]Entry{}}
	if ctx.Err() != nil {
		manifest.Truncated = true
		return manifest, nil
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 运行中被删除/不可读的目录或文件跳过,不让一处错误丢掉整份清单
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			manifest.Truncated = true
			return fs.SkipAll
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".cache", "__pycache__", ".xlyra":
				return fs.SkipDir
			}
			if depth > MaxDepth {
				manifest.Truncated = true
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return nil
		}
		if matched, _ := filepath.Match("*.tmp-*", d.Name()); matched {
			return nil
		}
		if depth > MaxDepth {
			manifest.Truncated = true
			return nil
		}
		if len(manifest.Entries) >= MaxEntries {
			manifest.Truncated = true
			return fs.SkipAll
		}
		e := Entry{Path: rel, Size: info.Size(), ModNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode())}
		if prev != nil {
			if old, ok := prev.Entries[rel]; ok && old.Size == e.Size && old.ModNano == e.ModNano {
				e.SHA256, e.Text, e.Binary = old.SHA256, old.Text, old.Binary
				manifest.Entries[rel] = e
				return nil
			}
		}
		file, openedInfo, ok := openRegular(root, rel)
		if !ok {
			return nil
		}
		defer file.Close()
		e.Size, e.ModNano, e.Mode = openedInfo.Size(), openedInfo.ModTime().UnixNano(), uint32(openedInfo.Mode())
		var content []byte
		if e.Size <= HashMaxBytes {
			content, err = io.ReadAll(file)
			if err != nil {
				return nil
			}
			sum := sha256.Sum256(content)
			e.SHA256 = hex.EncodeToString(sum[:])
		} else {
			content, err = io.ReadAll(io.LimitReader(file, 8<<10))
			if err != nil {
				return nil
			}
		}
		if e.Size <= BlobMaxBytes {
			e.Text = textCandidate(rel) && textContent(content)
		}
		e.Binary = !textContent(content)
		manifest.Entries[rel] = e
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	return manifest, nil
}

func (m *Manifest) Save(path string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".manifest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Entries == nil {
		m.Entries = map[string]Entry{}
	}
	return &m, nil
}

type Change struct {
	Path string
	Kind string
}

func Diff(before, after *Manifest) []Change {
	beforeEntries, afterEntries := map[string]Entry{}, map[string]Entry{}
	if before != nil {
		beforeEntries = before.Entries
	}
	if after != nil {
		afterEntries = after.Entries
	}
	changes := []Change{}
	for path, old := range beforeEntries {
		newEntry, ok := afterEntries[path]
		if !ok {
			changes = append(changes, Change{path, "deleted"})
			continue
		}
		modified := old.SHA256 != "" && newEntry.SHA256 != "" && old.SHA256 != newEntry.SHA256
		if old.SHA256 == "" || newEntry.SHA256 == "" {
			modified = old.Size != newEntry.Size || old.ModNano != newEntry.ModNano
		}
		if modified || old.Size != newEntry.Size {
			changes = append(changes, Change{path, "modified"})
		}
	}
	for path := range afterEntries {
		if _, ok := beforeEntries[path]; !ok {
			changes = append(changes, Change{path, "added"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}
