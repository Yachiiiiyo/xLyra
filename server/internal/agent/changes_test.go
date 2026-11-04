package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/workspace"
)

func setRunChanges(t *testing.T, store *session.Store, id string, changes ...session.RunChange) {
	t.Helper()
	if _, err := store.Update(id, func(meta *session.Meta) error {
		meta.RunChanges = changes
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writeBeforeSnapshot(t *testing.T, store *session.Store, id, turnID string, blobs map[string]string) {
	t.Helper()
	stateDir := store.StateDir(id)
	entries := make(map[string]workspace.Entry)
	for path, content := range blobs {
		sum := sha256.Sum256([]byte(content))
		sha := hex.EncodeToString(sum[:])
		entries[path] = workspace.Entry{Path: path, SHA256: sha, Text: true}
		if err := os.MkdirAll(filepath.Join(stateDir, "blobs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, "blobs", sha), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(beforeSnapshot{TurnID: turnID, Manifest: workspace.Manifest{Entries: entries}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "before.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionChangesEmptyAndFilter(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/agent/sessions/" + meta.ID + "/changes"
	var body struct {
		Data struct {
			Changes []session.RunChange `json:"changes"`
		} `json:"data"`
	}
	rec := doJSONAgent(t, handler, http.MethodGet, url, "")
	decodeBody(t, rec, &body)
	if rec.Code != http.StatusOK || body.Data.Changes == nil || len(body.Data.Changes) != 0 {
		t.Fatalf("empty changes = %d/%+v", rec.Code, body.Data.Changes)
	}
	setRunChanges(t, store, meta.ID,
		session.RunChange{TurnID: "turn-1", Files: []session.FileChange{{Path: "a.txt", Kind: "added"}}},
		session.RunChange{TurnID: "turn-2", Files: []session.FileChange{{Path: "b.txt", Kind: "modified"}}},
	)
	rec = doJSONAgent(t, handler, http.MethodGet, url+"?turn_id=turn-2", "")
	decodeBody(t, rec, &body)
	if len(body.Data.Changes) != 1 || body.Data.Changes[0].TurnID != "turn-2" || body.Data.Changes[0].Files[0].Path != "b.txt" {
		t.Fatalf("filtered changes = %+v", body.Data.Changes)
	}
	rec = doJSONAgent(t, handler, http.MethodGet, url+"?turn_id=missing", "")
	decodeBody(t, rec, &body)
	if body.Data.Changes == nil || len(body.Data.Changes) != 0 {
		t.Fatalf("missing turn changes = %+v", body.Data.Changes)
	}
}

func TestSessionChangeDiffKinds(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	turnID := "turn-1"
	files := []session.FileChange{
		{Path: "added.txt", Kind: "added"},
		{Path: "modified.txt", Kind: "modified"},
		{Path: "deleted.txt", Kind: "deleted"},
	}
	setRunChanges(t, store, meta.ID, session.RunChange{TurnID: turnID, Files: files})
	writeBeforeSnapshot(t, store, meta.ID, turnID, map[string]string{
		"modified.txt": "one\ntwo\n", "deleted.txt": "gone\n",
	})
	writeSessionFile(t, store, meta, "added.txt", []byte("new\n"))
	writeSessionFile(t, store, meta, "modified.txt", []byte("one\nthree\n"))
	for _, tt := range []struct {
		path, old, new string
	}{
		{"added.txt", "", "new\n"},
		{"modified.txt", "one\ntwo\n", "one\nthree\n"},
		{"deleted.txt", "gone\n", ""},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/changes/"+turnID+"/diff?path="+tt.path, "")
			want, _, _, _ := workspace.UnifiedDiff(tt.path, tt.old, tt.new)
			if rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Fatalf("diff = %d/%q, want %q", rec.Code, rec.Body.String(), want)
			}
			for key, value := range map[string]string{
				"Content-Type": "text/plain; charset=utf-8", "X-Content-Type-Options": "nosniff", "Cache-Control": "private, no-store",
			} {
				if rec.Header().Get(key) != value {
					t.Errorf("%s = %q", key, rec.Header().Get(key))
				}
			}
		})
	}
}

func TestSessionChangeDiffErrors(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	setRunChanges(t, store, meta.ID, session.RunChange{TurnID: "turn-1", Files: []session.FileChange{
		{Path: "missing-blob.txt", Kind: "modified"}, {Path: "nul.txt", Kind: "added"}, {Path: "ok.txt", Kind: "added"},
	}})
	writeBeforeSnapshot(t, store, meta.ID, "turn-1", nil)
	writeSessionFile(t, store, meta, "nul.txt", []byte("a\x00b"))
	writeSessionFile(t, store, meta, "ok.txt", []byte("ok"))
	base := "/api/v1/agent/sessions/" + meta.ID + "/changes/turn-1/diff?path="
	for _, tt := range []struct {
		name, url string
		status    int
	}{
		{"非最近一轮", "/api/v1/agent/sessions/" + meta.ID + "/changes/turn-1/diff?path=ok.txt", http.StatusGone},
		{"路径不在记录", base + "other.txt", http.StatusNotFound},
		{"非法路径", base + "../secret", http.StatusBadRequest},
		{"blob 缺失", base + "missing-blob.txt", http.StatusNotFound},
		{"NUL", base + "nul.txt", http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "非最近一轮" {
				writeBeforeSnapshot(t, store, meta.ID, "turn-2", nil)
				defer writeBeforeSnapshot(t, store, meta.ID, "turn-1", nil)
			}
			rec := doJSONAgent(t, handler, http.MethodGet, tt.url, "")
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}
