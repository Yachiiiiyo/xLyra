package agent

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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

func readArtifactsZip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	files := make(map[string]string)
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = string(data)
	}
	return files
}

func TestSessionArtifactsZip(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, store, meta, "docs/a.txt", []byte("alpha"))
	writeSessionFile(t, store, meta, "b.bin", []byte{1, 2, 3})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.WorkDir(meta.ID), "link.txt")); err != nil {
		t.Fatal(err)
	}
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/artifacts.zip", "")
	files := readArtifactsZip(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || files["docs/a.txt"] != "alpha" || files["b.bin"] != string([]byte{1, 2, 3}) || len(files) != 2 {
		t.Fatalf("zip = %d/%+v", rec.Code, files)
	}
	if rec.Header().Get("Content-Type") != "application/zip" || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("zip headers = %v", rec.Header())
	}
	wantDisposition := `attachment; filename="workspace-` + meta.ID[:8] + `.zip"`
	if rec.Header().Get("Content-Disposition") != wantDisposition {
		t.Fatalf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
}

func TestSessionArtifactsZipLimitAndEmpty(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, store, meta, "a.txt", []byte("1234"))
	writeSessionFile(t, store, meta, "b.txt", []byte("5678"))
	oldLimit := artifactsZipMaxBytes
	artifactsZipMaxBytes = 5
	defer func() { artifactsZipMaxBytes = oldLimit }()
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/artifacts.zip", "")
	files := readArtifactsZip(t, rec.Body.Bytes())
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(files) != 1 || files["a.txt"] != "1234" {
		t.Fatalf("truncated zip = %+v", files)
	}

	artifactsZipMaxBytes = oldLimit
	empty, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+empty.ID+"/artifacts.zip", "")
	if files := readArtifactsZip(t, rec.Body.Bytes()); len(files) != 0 {
		t.Fatalf("empty zip = %+v", files)
	}
}
