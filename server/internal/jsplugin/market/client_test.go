package market

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/version"
)

func TestFetchUserAgentStatusAndLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "xLyra/"+version.Current().Version {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		for key := range r.Header {
			switch key {
			case "User-Agent", "If-None-Match", "Accept-Encoding":
			default:
				t.Errorf("unexpected header %s", key)
			}
		}
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte("0123456789"))
		case "/missing":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("ETag", `"v1"`)
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}

	body, etag, notModified, err := fetch(context.Background(), client, srv.URL+"/ok", "", 4)
	if err != nil || notModified || string(body) != "ok" || etag != `"v1"` {
		t.Fatalf("fetch() = %q, %q, %v, %v", body, etag, notModified, err)
	}
	body, etag, notModified, err = fetch(context.Background(), client, srv.URL+"/ok", `"v1"`, 4)
	if err != nil || !notModified || body != nil || etag != `"v1"` {
		t.Fatalf("conditional fetch() = %q, %q, %v, %v", body, etag, notModified, err)
	}
	if _, _, _, err := fetch(context.Background(), client, srv.URL+"/big", "", 4); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize err = %v", err)
	}
	if _, _, _, err := fetch(context.Background(), client, srv.URL+"/missing", "", 4); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("status err = %v", err)
	}
	if _, _, _, err := fetch(context.Background(), client, srv.URL+"/ok", "", marketproto.MaxRootBytes); err != nil {
		t.Fatal(err)
	}
}
