package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/workspace"
	"xlyra/server/internal/httpx"
)

// SessionChanges GET /sessions/{id}/changes?turn_id=<可选>。
func (h *Handler) SessionChanges(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	meta, err := h.store.Get(id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	changes := make([]session.RunChange, 0)
	turnID := r.URL.Query().Get("turn_id")
	for _, change := range meta.RunChanges {
		if turnID == "" || change.TurnID == turnID {
			changes = append(changes, change)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{"changes": changes}})
}

type beforeSnapshot struct {
	TurnID   string             `json:"turn_id"`
	Manifest workspace.Manifest `json:"manifest"`
}

// SessionChangeDiff GET /sessions/{id}/changes/{turnID}/diff?path=<相对路径>。
func (h *Handler) SessionChangeDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	meta, err := h.store.Get(id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rel := r.URL.Query().Get("path")
	if !validSessionFilePath(rel) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "path 不合法")
		return
	}
	turnID := chi.URLParam(r, "turnID")
	var fileChange *session.FileChange
	for _, change := range meta.RunChanges {
		if change.TurnID != turnID {
			continue
		}
		for i := range change.Files {
			if change.Files[i].Path == rel {
				fileChange = &change.Files[i]
				break
			}
		}
	}
	if fileChange == nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "无变更记录")
		return
	}

	data, err := os.ReadFile(filepath.Join(h.store.StateDir(id), "before.json"))
	var before beforeSnapshot
	if err != nil || json.Unmarshal(data, &before) != nil || before.TurnID != turnID {
		httpx.Error(w, r, http.StatusGone, "gone", "更早轮次的 diff 不再保留")
		return
	}

	var oldText string
	if fileChange.Kind != "added" {
		entry := before.Manifest.Entries[rel]
		content, ok := workspace.NewStore(filepath.Join(h.store.StateDir(id), "blobs")).Get(entry.SHA256)
		if !ok || !diffText(content) {
			httpx.Error(w, r, http.StatusNotFound, "not_found", "无 diff")
			return
		}
		oldText = string(content)
	}
	var newText string
	if fileChange.Kind != "deleted" {
		file, info, ok := openSessionFile(h.store.WorkDir(id), rel)
		if !ok {
			h.sessionFileNotFound(w, r)
			return
		}
		defer file.Close()
		if info.Size() > workspace.BlobMaxBytes {
			httpx.Error(w, r, http.StatusNotFound, "not_found", "无 diff")
			return
		}
		content, err := io.ReadAll(io.LimitReader(file, workspace.BlobMaxBytes+1))
		if err != nil || int64(len(content)) > workspace.BlobMaxBytes || !diffText(content) {
			httpx.Error(w, r, http.StatusNotFound, "not_found", "无 diff")
			return
		}
		newText = string(content)
	}
	patch, _, _, truncated := workspace.UnifiedDiff(rel, oldText, newText)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if truncated {
		w.Header().Set("X-Xlyra-Diff-Truncated", "1")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, patch)
}

func diffText(content []byte) bool {
	return utf8.Valid(content) && !bytes.ContainsRune(content, 0)
}
