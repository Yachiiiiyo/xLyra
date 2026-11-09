package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xlyra/server/internal/agent/session"
)

func TestSessionArtifacts(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, store, meta, "old.txt", []byte("text"))
	writeSessionFile(t, store, meta, "new.png", testPNG)
	writeSessionFile(t, store, meta, "data.bin", []byte{0, 1})
	writeSessionFile(t, store, meta, ".git/ignored.txt", []byte("ignored"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(store.WorkDir(meta.ID), "old.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.WorkDir(meta.ID), "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(store.WorkDir(meta.ID), "hard.txt")); err != nil {
		t.Fatal(err)
	}
	setRunChanges(t, store, meta.ID, session.RunChange{TurnID: "turn-1", Files: []session.FileChange{
		{Path: "old.txt", Kind: "modified"}, {Path: "deleted.txt", Kind: "deleted"},
	}})
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/artifacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Files []struct {
				Path           string `json:"path"`
				Size           int64  `json:"size"`
				MTime          string `json:"mtime"`
				Kind           string `json:"kind"`
				AgentGenerated bool   `json:"agent_generated"`
			} `json:"files"`
			Truncated bool `json:"truncated"`
		} `json:"data"`
	}
	decodeBody(t, rec, &body)
	if len(body.Data.Files) != 3 {
		t.Fatalf("files = %+v", body.Data.Files)
	}
	last := body.Data.Files[len(body.Data.Files)-1]
	if last.Path != "old.txt" || !last.AgentGenerated || last.Kind != "text" || last.Size != 4 {
		t.Fatalf("oldest artifact = %+v", last)
	}
	if _, err := time.Parse(time.RFC3339, last.MTime); err != nil {
		t.Fatalf("mtime = %q: %v", last.MTime, err)
	}
	kinds := map[string]string{}
	for _, file := range body.Data.Files {
		kinds[file.Path] = file.Kind
	}
	if kinds["new.png"] != "image" || kinds["data.bin"] != "binary" {
		t.Fatalf("kinds = %+v", kinds)
	}
}

func TestSessionArtifactsEmptyWorkspace(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/artifacts", "")
	var body struct {
		Data struct {
			Files []sessionArtifact `json:"files"`
		} `json:"data"`
	}
	decodeBody(t, rec, &body)
	if rec.Code != http.StatusOK || body.Data.Files == nil || len(body.Data.Files) != 0 {
		t.Fatalf("empty artifacts = %d/%+v", rec.Code, body.Data.Files)
	}
}
