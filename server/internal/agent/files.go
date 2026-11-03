package agent

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"xlyra/server/internal/httpx"
)

const sessionImageMaxBytes = 32 << 20

// SessionFile GET/HEAD /sessions/{id}/files?path=images/<文件名>。
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
	name, ok := sessionImageName(r.URL.Query().Get("path"))
	if !ok {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "path 必须是 images/ 下的单层文件名")
		return
	}

	workDir := h.store.WorkDir(id)
	imagesDir := filepath.Join(workDir, "images")
	path := filepath.Join(imagesDir, name)
	if !regularDir(workDir) || !regularDir(imagesDir) {
		h.sessionFileNotFound(w, r)
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > sessionImageMaxBytes {
		h.sessionFileNotFound(w, r)
		return
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		h.sessionFileNotFound(w, r)
		return
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > sessionImageMaxBytes || fileLinks(info) != 1 {
		h.sessionFileNotFound(w, r)
		return
	}

	header := make([]byte, 12)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		h.sessionFileNotFound(w, r)
		return
	}
	mime := imageContentType(header[:n])
	if mime == "" {
		h.sessionFileNotFound(w, r)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		h.sessionFileNotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = fmt.Sprintf("attachment; filename=%q", name)
	}
	w.Header().Set("Content-Disposition", disposition)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, file, info.Size())
	}
}

func sessionImageName(path string) (string, bool) {
	if path == "" || len(path) > 200 || filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') ||
		strings.Contains(path, "..") || strings.Contains(path, `\`) {
		return "", false
	}
	clean := filepath.Clean(path)
	name, ok := strings.CutPrefix(clean, "images/")
	if !ok || name == "" || name != filepath.Base(name) {
		return "", false
	}
	return name, true
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
	default:
		return ""
	}
}

func (h *Handler) sessionFileNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, "not_found", "图片不存在")
}
