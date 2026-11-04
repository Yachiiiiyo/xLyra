package agent

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		{"image.gif", []byte("GIF89a图片字节"), "image/gif"},
		{"image87.gif", []byte("GIF87a图片字节"), "image/gif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				"X-Xlyra-File-Kind":      "image",
			} {
				if got := rec.Header().Get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestSessionFileNestedText(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	tests := []struct {
		path string
		body string
	}{
		{"docs/nested/readme.md", "# 标题"},
		{"docs/nested/page.html", "<script>alert(1)</script>"},
		{"docs/nested/vector.svg", "<svg xmlns=\"http://www.w3.org/2000/svg\"/>"},
		{"docs/nested/data.json", `{"ok":true}`},
		{"docs/nested/Makefile", "all:\n\techo ok\n"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			path := filepath.Join(store.WorkDir(meta.ID), filepath.FromSlash(tt.path))
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/files?path="+tt.path, "")
			if rec.Code != http.StatusOK || rec.Body.String() != tt.body {
				t.Fatalf("status/body = %d/%q", rec.Code, rec.Body.String())
			}
			for key, want := range map[string]string{
				"Content-Type":            "text/plain; charset=utf-8",
				"X-Content-Type-Options":  "nosniff",
				"Content-Security-Policy": "sandbox",
				"Cache-Control":           "private, no-store",
				"Content-Disposition":     "inline",
				"Content-Length":          strconv.Itoa(len(tt.body)),
				"X-Xlyra-File-Kind":       "text",
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
	wantDisposition := fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, name, name)
	if got := rec.Header().Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisposition)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("download Content-Type = %q", got)
	}
	rec = doJSONAgent(t, handler, http.MethodHead, url, "")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status/body = %d/%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Content-Length") != strconv.Itoa(len(testPNG)) {
		t.Fatalf("HEAD headers = %v", rec.Header())
	}
}

func TestSessionFileTextHeadMatchesGet(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	writeSessionFile(t, store, meta, "notes/readme.txt", []byte("你好"))
	url := "/api/v1/agent/sessions/" + meta.ID + "/files?path=notes/readme.txt"
	get := doJSONAgent(t, handler, http.MethodGet, url, "")
	head := doJSONAgent(t, handler, http.MethodHead, url, "")
	if head.Code != get.Code || head.Body.Len() != 0 {
		t.Fatalf("HEAD status/body = %d/%q", head.Code, head.Body.String())
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Content-Security-Policy", "Cache-Control", "X-Xlyra-File-Kind"} {
		if head.Header().Get(key) != get.Header().Get(key) {
			t.Errorf("HEAD %s = %q, GET = %q", key, head.Header().Get(key), get.Header().Get(key))
		}
	}
}

func TestSessionFileRejectsUnsafeFiles(t *testing.T) {
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
	t.Run("中间目录符号链接", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "outside.md"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(store.WorkDir(meta.ID), "linked")); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, "/api/v1/agent/sessions/"+meta.ID+"/files?path=linked/outside.md", http.StatusNotFound)
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
	t.Run("FIFO", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		path := filepath.Join(store.WorkDir(meta.ID), "images", "pipe")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "pipe"), http.StatusNotFound)
	})
	t.Run("目录", func(t *testing.T) {
		handler, store, _ := newTestHandler(t, &fakeRunner{})
		meta := newImageSession(t, handler, store)
		assertSessionFileStatus(t, handler, sessionFileURL(meta.ID, "."), http.StatusBadRequest)
		if err := os.Mkdir(filepath.Join(store.WorkDir(meta.ID), "folder"), 0o750); err != nil {
			t.Fatal(err)
		}
		assertSessionFileStatus(t, handler, "/api/v1/agent/sessions/"+meta.ID+"/files?path=folder", http.StatusNotFound)
	})
	t.Run("图片超过大小上限", func(t *testing.T) {
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
		"", "images/", "images/../a.png", "../a.png", "/etc/passwd", `images\a.png`,
		"images//a.png", "./images/a.png", "images/a%00.png", "images/a%0a.png",
		"images/" + string(bytes.Repeat([]byte{'a'}, 401)) + ".png",
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

func TestSessionFileTextPreviewLimit(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	data := bytes.Repeat([]byte{'a'}, sessionPreviewMaxBytes+1)
	writeSessionFile(t, store, meta, "large.txt", data)
	url := "/api/v1/agent/sessions/" + meta.ID + "/files?path=large.txt"
	rec := doJSONAgent(t, handler, http.MethodGet, url, "")
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "文件过大,请下载查看") {
		t.Fatalf("preview status/body = %d/%s", rec.Code, rec.Body.String())
	}
	rec = doJSONAgent(t, handler, http.MethodGet, url+"&download=1", "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("download status/body length = %d/%d", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("X-Xlyra-File-Kind") != "text" || rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download headers = %v", rec.Header())
	}
}

func TestSessionFileBinary(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	for _, tt := range []struct {
		path string
		data []byte
	}{
		{"nul.txt", []byte("before\x00after")},
		{"archive.bin", []byte("plain but unknown")},
	} {
		t.Run(tt.path, func(t *testing.T) {
			writeSessionFile(t, store, meta, tt.path, tt.data)
			url := "/api/v1/agent/sessions/" + meta.ID + "/files?path=" + tt.path
			assertSessionFileStatus(t, handler, url, http.StatusUnsupportedMediaType)
			rec := doJSONAgent(t, handler, http.MethodGet, url+"&download=1", "")
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tt.data) {
				t.Fatalf("download status/body = %d/%v", rec.Code, rec.Body.Bytes())
			}
			if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("X-Xlyra-File-Kind") != "binary" {
				t.Fatalf("download headers = %v", rec.Header())
			}
		})
	}
}

func TestSessionFileDownloadUTF8Name(t *testing.T) {
	handler, store, _ := newTestHandler(t, &fakeRunner{})
	meta := newImageSession(t, handler, store)
	writeSessionFile(t, store, meta, "产物/报告.md", []byte("内容"))
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/sessions/"+meta.ID+"/files?path=产物/报告.md&download=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, `filename="__.md"`) || !strings.Contains(disposition, `filename*=UTF-8''%E6%8A%A5%E5%91%8A.md`) {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
}

func assertSessionFileStatus(t *testing.T, handler *Handler, url string, status int) {
	t.Helper()
	rec := doJSONAgent(t, handler, http.MethodGet, url, "")
	if rec.Code != status {
		t.Fatalf("GET %s status = %d, want %d; body = %s", url, rec.Code, status, rec.Body.String())
	}
}

func writeSessionFile(t *testing.T, store *session.Store, meta session.Meta, name string, data []byte) {
	t.Helper()
	path := filepath.Join(store.WorkDir(meta.ID), filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
