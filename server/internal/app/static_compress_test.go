package app

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func newStaticCompressionTestServer(t *testing.T) (*compressedFileServer, string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	files := map[string][]byte{
		"assets/app.js":     bytes.Repeat([]byte("console.log('xlyra static compression');\n"), 128),
		"assets/app.css":    bytes.Repeat([]byte(".app { color: #123456; }\n"), 128),
		"assets/small.js":   []byte("console.log('small');"),
		"assets/font.woff2": bytes.Repeat([]byte("already-compressed-font"), 128),
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newCompressedFileServer(dir, http.FileServer(http.Dir(dir))), dir, files
}

func requestStatic(handler http.Handler, method, path, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodedStaticBody(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	var reader io.Reader = bytes.NewReader(rec.Body.Bytes())
	switch rec.Header().Get("Content-Encoding") {
	case "br":
		reader = brotli.NewReader(reader)
	case "gzip":
		gzipReader, err := gzip.NewReader(reader)
		if err != nil {
			t.Fatalf("create gzip reader: %v", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "":
	default:
		t.Fatalf("unexpected Content-Encoding %q", rec.Header().Get("Content-Encoding"))
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestCompressedFileServerBrotliAndGzip(t *testing.T) {
	t.Parallel()
	handler, _, files := newStaticCompressionTestServer(t)

	br := requestStatic(handler, http.MethodGet, "/assets/app.js", "br, gzip")
	if br.Code != http.StatusOK {
		t.Fatalf("brotli status = %d", br.Code)
	}
	if got := br.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("brotli Content-Encoding = %q", got)
	}
	if got := br.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if !strings.Contains(br.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("Content-Type = %q, want JavaScript", br.Header().Get("Content-Type"))
	}
	if got := br.Header().Get("Content-Length"); got != strconv.Itoa(br.Body.Len()) {
		t.Fatalf("Content-Length = %q, body length = %d", got, br.Body.Len())
	}
	if body := decodedStaticBody(t, br); !bytes.Equal(body, files["assets/app.js"]) {
		t.Fatal("brotli response does not match source")
	}

	gzipResponse := requestStatic(handler, http.MethodGet, "/assets/app.js", "gzip")
	if got := gzipResponse.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("gzip Content-Encoding = %q", got)
	}
	if body := decodedStaticBody(t, gzipResponse); !bytes.Equal(body, files["assets/app.js"]) {
		t.Fatal("gzip response does not match source")
	}
	if br.Header().Get("ETag") == gzipResponse.Header().Get("ETag") {
		t.Fatal("brotli and gzip responses must not share an ETag")
	}
	for _, etag := range []string{br.Header().Get("ETag"), gzipResponse.Header().Get("ETag")} {
		if !strings.HasPrefix(etag, `W/"`) {
			t.Fatalf("ETag = %q, want weak ETag", etag)
		}
	}

	css := requestStatic(handler, http.MethodGet, "/assets/app.css", "gzip")
	if got := css.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("css Content-Encoding = %q", got)
	}
	if !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css Content-Type = %q", css.Header().Get("Content-Type"))
	}
	if body := decodedStaticBody(t, css); !bytes.Equal(body, files["assets/app.css"]) {
		t.Fatal("css response does not match source")
	}
}

func TestCompressedFileServerIdentity(t *testing.T) {
	t.Parallel()
	handler, _, files := newStaticCompressionTestServer(t)
	for _, acceptEncoding := range []string{"", "identity", "br;q=0, gzip;q=0, identity"} {
		rec := requestStatic(handler, http.MethodGet, "/assets/app.js", acceptEncoding)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d", acceptEncoding, rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("%q: Content-Encoding = %q", acceptEncoding, got)
		}
		if !bytes.Equal(rec.Body.Bytes(), files["assets/app.js"]) {
			t.Fatalf("%q: response does not match source", acceptEncoding)
		}
	}
}

func TestCompressedFileServerEncodingQuality(t *testing.T) {
	t.Parallel()
	handler, _, _ := newStaticCompressionTestServer(t)
	cases := []struct {
		acceptEncoding string
		want           string
	}{
		{acceptEncoding: "gzip;q=0, br;q=0.5", want: "br"},
		{acceptEncoding: "br;q=0.2, gzip;q=0.8", want: "gzip"},
		{acceptEncoding: "identity;q=1, br;q=0.5", want: ""},
		{acceptEncoding: "identity;q=0, br;q=0.5", want: "br"},
		{acceptEncoding: "*;q=0.5", want: "br"},
		{acceptEncoding: "br;q=invalid, gzip;q=0.5", want: "gzip"},
	}
	for _, tc := range cases {
		rec := requestStatic(handler, http.MethodGet, "/assets/app.js", tc.acceptEncoding)
		if got := rec.Header().Get("Content-Encoding"); got != tc.want {
			t.Errorf("%q: Content-Encoding = %q, want %q", tc.acceptEncoding, got, tc.want)
		}
		if body := decodedStaticBody(t, rec); len(body) == 0 {
			t.Errorf("%q: empty response", tc.acceptEncoding)
		}
	}
}

func TestCompressedFileServerSkipsSmallAndAlreadyCompressedFiles(t *testing.T) {
	t.Parallel()
	handler, _, files := newStaticCompressionTestServer(t)
	for _, name := range []string{"assets/small.js", "assets/font.woff2"} {
		rec := requestStatic(handler, http.MethodGet, "/"+filepath.ToSlash(name), "br, gzip")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", name, rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("%s: Content-Encoding = %q", name, got)
		}
		if !bytes.Equal(rec.Body.Bytes(), files[name]) {
			t.Fatalf("%s: response does not match source", name)
		}
	}
}

func TestCompressedFileServerCachesCompressedResponse(t *testing.T) {
	t.Parallel()
	handler, _, _ := newStaticCompressionTestServer(t)
	for i := 0; i < 2; i++ {
		rec := requestStatic(handler, http.MethodGet, "/assets/app.js", "br")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "br" {
			t.Fatalf("request %d: status = %d, Content-Encoding = %q", i+1, rec.Code, rec.Header().Get("Content-Encoding"))
		}
	}
	handler.mu.Lock()
	compressions := handler.compressions
	cacheEntries := len(handler.cache)
	handler.mu.Unlock()
	if compressions != 1 {
		t.Fatalf("compressions = %d, want 1", compressions)
	}
	if cacheEntries != 1 {
		t.Fatalf("cache entries = %d, want 1", cacheEntries)
	}

	requestStatic(handler, http.MethodGet, "/assets/app.js", "gzip")
	handler.mu.Lock()
	compressions = handler.compressions
	cacheEntries = len(handler.cache)
	handler.mu.Unlock()
	if compressions != 2 || cacheEntries != 2 {
		t.Fatalf("gzip variant: compressions = %d, cache entries = %d, want 2 and 2", compressions, cacheEntries)
	}
}

func TestCompressedFileServerHeadAndRange(t *testing.T) {
	t.Parallel()
	handler, _, _ := newStaticCompressionTestServer(t)
	get := requestStatic(handler, http.MethodGet, "/assets/app.js", "gzip")
	encoded := bytes.Clone(get.Body.Bytes())
	if len(encoded) < 10 {
		t.Fatalf("encoded response is too short: %d", len(encoded))
	}

	head := requestStatic(handler, http.MethodHead, "/assets/app.js", "gzip")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", head.Code)
	}
	if got := head.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("HEAD Content-Encoding = %q", got)
	}
	if got := head.Header().Get("Content-Length"); got != strconv.Itoa(len(encoded)) {
		t.Fatalf("HEAD Content-Length = %q, want %d", got, len(encoded))
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, want 0", head.Body.Len())
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("Range status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("Range Content-Length = %q, want 10", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), encoded[:10]) {
		t.Fatal("Range response does not match encoded representation")
	}
}

func TestCompressedFileServerCacheLimitFallsBackToOriginal(t *testing.T) {
	t.Parallel()
	handler, _, files := newStaticCompressionTestServer(t)
	handler.cacheLimit = 1
	rec := requestStatic(handler, http.MethodGet, "/assets/app.js", "br")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want original response", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), files["assets/app.js"]) {
		t.Fatal("response does not match source")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.cache) != 0 || handler.cachedBytes != 0 {
		t.Fatalf("cache entries = %d, cached bytes = %d, want 0 and 0", len(handler.cache), handler.cachedBytes)
	}
}

func TestSPAHandlerCompressedAssetKeepsCacheControl(t *testing.T) {
	t.Parallel()
	_, dir, files := newStaticCompressionTestServer(t)
	handler := newSPAHandler(dir, nil)
	rec := requestStatic(handler, http.MethodGet, "/assets/app.js", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if body := decodedStaticBody(t, rec); !bytes.Equal(body, files["assets/app.js"]) {
		t.Fatal("response does not match source")
	}
}
