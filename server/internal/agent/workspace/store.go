package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Store 是按 SHA256 寻址的文本内容存储。
type Store struct {
	dir string
	// size 缓存目录总量:Put 逐个文件调用,每次重新遍历目录会是 O(n²)
	size      int64
	sizeKnown bool
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Store) Put(root string, e Entry) (bool, error) {
	if !e.Text || !shaPattern.MatchString(e.SHA256) {
		return false, nil
	}
	path := filepath.Join(s.dir, e.SHA256)
	// 同哈希的 blob 已经存在(写入时已校验过内容),不必再读一遍源文件
	if _, err := os.Stat(path); err == nil {
		return true, nil
	}
	file, _, ok := openRegular(root, e.Path)
	if !ok {
		return false, nil
	}
	content, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		return false, nil
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != e.SHA256 {
		return false, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return false, err
	}
	if s.Size()+int64(len(content)) > StoreMaxBytes {
		return false, nil
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return false, err
	}
	s.size += int64(len(content))
	return true, nil
}

func (s *Store) Get(sha string) ([]byte, bool) {
	if !shaPattern.MatchString(sha) {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, sha))
	return data, err == nil
}

func (s *Store) Prune(keep map[string]bool) error {
	s.sizeKnown = false
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && !keep[entry.Name()] {
			if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Size() int64 {
	if s.sizeKnown {
		return s.size
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	var size int64
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			size += info.Size()
		}
	}
	s.size, s.sizeKnown = size, true
	return size
}
