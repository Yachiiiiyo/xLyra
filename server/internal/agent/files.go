package agent

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"xlyra/server/internal/httpx"
)

const (
	sessionImageMaxBytes   = 32 << 20
	sessionPreviewMaxBytes = 2 << 20
	sessionDownloadMaxSize = 64 << 20
)

var sessionTextExtensions = map[string]bool{
	"md": true, "markdown": true, "txt": true, "log": true, "json": true,
	"yaml": true, "yml": true, "toml": true, "csv": true, "tsv": true,
	"html": true, "htm": true, "css": true, "js": true, "mjs": true,
	"ts": true, "tsx": true, "jsx": true, "py": true, "go": true,
	"rs": true, "java": true, "c": true, "h": true, "cpp": true,
	"sh": true, "sql": true, "xml": true, "svg": true, "ini": true,
	"conf": true, "env": true,
}

// SessionFile GET/HEAD /sessions/{id}/files?path=<相对路径>&download=1。
// 工作区由会话子进程写入,因此文件类型、链接关系与内容都不可信。
func (h *Handler) SessionFile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	if _, err := h.store.Get(id); err != nil {
		h.fail(w, r, err)
		return
	}
	rel := r.URL.Query().Get("path")
	if !validSessionFilePath(rel) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "path 不合法")
		return
	}

	file, info, ok := openSessionFile(h.store.WorkDir(id), rel)
	if !ok {
		h.sessionFileNotFound(w, r)
		return
	}
	defer file.Close()

	download := r.URL.Query().Get("download") == "1"
	if download && info.Size() > sessionDownloadMaxSize {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "文件过大,请下载查看")
		return
	}

	header := make([]byte, 12)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		h.sessionFileNotFound(w, r)
		return
	}
	mime := imageContentType(header[:n])
	kind := "binary"
	if mime != "" {
		kind = "image"
	} else if sessionTextCandidate(rel) {
		sniffSize := info.Size()
		if !download && sniffSize > sessionPreviewMaxBytes {
			sniffSize = sessionPreviewMaxBytes
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			h.sessionFileNotFound(w, r)
			return
		}
		content, err := io.ReadAll(io.LimitReader(file, sniffSize))
		if err != nil {
			h.sessionFileNotFound(w, r)
			return
		}
		// 在嗅探上限处截断可能切开末尾的多字节字符,只在确实被截断时去掉不完整的尾部
		if sniffSize < info.Size() {
			for i := 0; i < utf8.UTFMax && len(content) > 0 && !utf8.Valid(content); i++ {
				content = content[:len(content)-1]
			}
		}
		if utf8.Valid(content) && bytes.IndexByte(content, 0) < 0 {
			kind = "text"
		}
	}
	w.Header().Set("X-Xlyra-File-Kind", kind)

	if download {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			h.sessionFileNotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Disposition", sessionDownloadDisposition(filepath.Base(rel)))
	} else {
		switch kind {
		case "image":
			if info.Size() > sessionImageMaxBytes {
				h.sessionFileNotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Cache-Control", "private, max-age=3600")
		case "text":
			if info.Size() > sessionPreviewMaxBytes {
				httpx.Error(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "文件过大,请下载查看")
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "sandbox")
			w.Header().Set("Cache-Control", "private, no-store")
		default:
			httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "该文件类型不支持预览,请下载")
			return
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			h.sessionFileNotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "inline")
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, file, info.Size())
	}
}

func validSessionFilePath(path string) bool {
	if path == "" || len(path) > 400 || filepath.IsAbs(path) || strings.Contains(path, "..") ||
		strings.Contains(path, `\`) || filepath.Clean(path) != path {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] < 0x20 || path[i] == 0x7f {
			return false
		}
	}
	return true
}

// openSessionFile 逐级拒绝链接与非目录,打开后再复核普通文件及硬链接数。
func openSessionFile(workDir, rel string) (*os.File, os.FileInfo, bool) {
	if !regularDir(workDir) {
		return nil, nil, false
	}
	path := workDir
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		path = filepath.Join(path, part)
		if !regularDir(path) {
			return nil, nil, false
		}
	}
	path = filepath.Join(path, parts[len(parts)-1])
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, false
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, false
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || fileLinks(info) != 1 {
		file.Close()
		return nil, nil, false
	}
	return file, info, true
}

func sessionTextCandidate(path string) bool {
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
	return sessionTextExtensions[strings.ToLower(ext)]
}

func sessionDownloadDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(name))
}

func regularDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

func fileLinks(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 0
}

func imageContentType(header []byte) string {
	switch {
	case len(header) >= 8 && string(header[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "image/jpeg"
	case len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP":
		return "image/webp"
	case len(header) >= 6 && (string(header[:6]) == "GIF87a" || string(header[:6]) == "GIF89a"):
		return "image/gif"
	default:
		return ""
	}
}

func (h *Handler) sessionFileNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, "not_found", "文件不存在")
}
