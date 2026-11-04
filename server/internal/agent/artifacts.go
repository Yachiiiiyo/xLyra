package agent

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"xlyra/server/internal/agent/workspace"
	"xlyra/server/internal/httpx"
)

var artifactsZipMaxBytes int64 = 256 << 20

type sessionArtifact struct {
	Path           string    `json:"path"`
	Size           int64     `json:"size"`
	ModTime        time.Time `json:"mtime"`
	Kind           string    `json:"kind"`
	AgentGenerated bool      `json:"agent_generated"`
}

func scanArtifacts(ctx context.Context, root string) (*workspace.Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return workspace.Scan(ctx, root, nil)
}

// SessionArtifacts GET /sessions/{id}/artifacts。
func (h *Handler) SessionArtifacts(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	meta, err := h.store.Get(id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	manifest, err := scanArtifacts(r.Context(), h.store.WorkDir(id))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	generated := make(map[string]bool)
	for _, change := range meta.RunChanges {
		for _, file := range change.Files {
			if file.Kind != "deleted" {
				generated[file.Path] = true
			}
		}
	}
	files := make([]sessionArtifact, 0, len(manifest.Entries))
	for path, entry := range manifest.Entries {
		files = append(files, sessionArtifact{
			Path: path, Size: entry.Size, ModTime: time.Unix(0, entry.ModNano).UTC(),
			Kind: artifactKind(path, entry), AgentGenerated: generated[path],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Path < files[j].Path
		}
		return files[i].ModTime.After(files[j].ModTime)
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"files": files, "truncated": manifest.Truncated,
	}})
}

func artifactKind(path string, entry workspace.Entry) string {
	switch strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".") {
	case "png", "jpg", "jpeg", "webp", "gif":
		return "image"
	}
	if sessionTextCandidate(path) && (entry.Text || entry.Size > workspace.BlobMaxBytes) {
		return "text"
	}
	return "binary"
}

// SessionArtifactsZip GET /sessions/{id}/artifacts.zip。
func (h *Handler) SessionArtifactsZip(w http.ResponseWriter, r *http.Request) {
	id, ok := h.sessionID(w, r)
	if !ok {
		return
	}
	if _, err := h.store.Get(id); err != nil {
		h.fail(w, r, err)
		return
	}
	manifest, err := scanArtifacts(r.Context(), h.store.WorkDir(id))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	paths := make([]string, 0, len(manifest.Entries))
	for path := range manifest.Entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "application/zip")
	name := id
	if len(name) > 8 {
		name = name[:8]
	}
	w.Header().Set("Content-Disposition", `attachment; filename="workspace-`+name+`.zip"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()
	var total int64
	for _, path := range paths {
		if r.Context().Err() != nil {
			return
		}
		file, info, ok := openSessionFile(h.store.WorkDir(id), path)
		if !ok {
			continue
		}
		if info.Size() > artifactsZipMaxBytes-total {
			// 放不下的文件跳过,后面更小的文件仍可入包
			file.Close()
			continue
		}
		header := &zip.FileHeader{Name: path, Method: zip.Deflate}
		header.Modified = info.ModTime()
		entry, err := zw.CreateHeader(header)
		if err == nil {
			_, _ = io.CopyN(entry, file, info.Size())
			total += info.Size()
		}
		file.Close()
		if err != nil {
			return
		}
	}
}
