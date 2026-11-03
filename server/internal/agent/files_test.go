package agent

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"xlyra/server/internal/agent/session"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n图片字节")

func newImageSession(t *testing.T, handler *Handler, store *session.Store) session.Meta {
	t.Helper()
	meta, err := store.Create("m1", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.WorkDir(meta.ID), "images"), 0o750); err != nil {
		t.Fatal(err)
	}
	return meta
}

func sessionFileURL(id, name string) string {
	return "/api/v1/agent/sessions/" + id + "/files?path=images/" + name
}

func TestSessionFileImages(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	tests := []struct {
		name string
		data []byte
		mime string
	}{
		{"image.png", testPNG, "image/png"},
		{"photo.dat", []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2}, "image/jpeg"},
		{"image.webp", []byte("RIFF\x04\x00\x00\x00WEBPpayload"), "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(store.WorkDir(meta.ID), "images", tt.name), tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := doJSONAgent(t, handler, http.MethodGet, sessionFileURL(meta.ID, tt.name), "")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tt.data) {
				t.Fatalf("status/body = %d/%v", rec.Code, rec.Body.Bytes())
			}
			if got := rec.Header().Get("Content-Type"); got != tt.mime {
				t.Fatalf("Content-Type = %q, want %q", got, tt.mime)
			}
			for key, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Cache-Control":          "private, max-age=3600",
				"Content-Disposition":    "inline",
				"Content-Length":         strconv.Itoa(len(tt.data)),
			} {
				if got := rec.Header().Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestSessionFileDownloadAndHead(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	name := "01-20261103-153012.png"
	if err := os.WriteFile(filepath.Join(store.WorkDir(meta.ID), "images", name), testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	url := sessionFileURL(meta.ID, name)
	rec := doJSONAgent(t, handler, http.MethodGet, url+"&download=1", "")
	if got, want := rec.Header().Get("Content-Disposition"), fmt.Sprintf("attachment; filename=%q", name); got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
	rec = doJSONAgent(t, handler, http.MethodHead, url, "")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status/body = %d/%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Length") != strconv.Itoa(len(testPNG)) {
		t.Fatalf("HEAD headers = %v", rec.Header())
	}
}

func TestSessionFileRejectsUnsafeFiles(t *testing.T) {
	t.Run("文本伪装图片", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		writeSessionImage(t, store, meta, "fake.png", []byte("not an image"))
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "fake.png"), http.StatusNotFound)
	})
	t.Run("文件符号链接", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		outside := filepath.Join(t.TempDir(), "outside.png")
		if err := os.WriteFile(outside, testPNG, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(store.WorkDir(meta.ID), "images", "link.png")); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "link.png"), http.StatusNotFound)
	})
	t.Run("images 目录符号链接", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta, err := store.Create("m1", "ask")
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "outside.png"), testPNG, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(store.WorkDir(meta.ID), "images")); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "outside.png"), http.StatusNotFound)
	})
	t.Run("硬链接", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		outside := filepath.Join(t.TempDir(), "outside.png")
		if err := os.WriteFile(outside, testPNG, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(outside, filepath.Join(store.WorkDir(meta.ID), "images", "hard.png")); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "hard.png"), http.StatusNotFound)
	})
	t.Run("超过大小上限", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		path := filepath.Join(store.WorkDir(meta.ID), "images", "large.png")
		if err := os.WriteFile(path, testPNG, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(path, sessionImageMaxBytes+1); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "large.png"), http.StatusNotFound)
	})
}

func TestSessionFilePathAndExistenceErrors(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	badPaths := []string{
		"", "images/", "images/sub/a.png", "images/../a.png", "/etc/passwd", `images\a.png`,
		"other/a.png", "images/a%00.png", "images/" + string(bytes.Repeat([]byte{'a'}, 201)) + ".png",
	}
	for _, path := range badPaths {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			url := "/api/v1/agent/sessions/" + meta.ID + "/files?path=" + path
			assertSessionFileStatus(t, handler, url, http.StatusBadRequest)
		})
	}
	assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "missing.png"), http.StatusNotFound)
	assertSessionFileStatus(t, handler, sessionFileURL("00000000-0000-4000-8000-00000000dead", "a.png"), http.StatusNotFound)
}

func writeSessionImage(t *testing.T, store *session.Store, meta session.Meta, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(store.WorkDir(meta.ID), "images", name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertSessionFileStatus(t *testing.T, handler *Handler, url string, status int) {
	t.Helper()
	rec := doJSONAgent(t, handler, http.MethodGet, url, "")
	if rec.Code != status {
		t.Fatalf("GET %s status = %d, want %d; body = %s", url, rec.Code, status, rec.Body.String())
	}
}
