package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"xlyra/server/internal/agent/workspace"
	"xlyra/server/internal/httpx"
)

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
