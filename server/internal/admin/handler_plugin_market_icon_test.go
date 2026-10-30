package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestPluginMarketIconUnavailable(t *testing.T) {
	r := chi.NewRouter()
	RegisterPluginMarket(r, Handler{})
	rec := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market/icons/"+strings.Repeat("ab", 32), ""))
	assertAdminErrorCode(t, rec, http.StatusServiceUnavailable, "plugin_market_unavailable")
}

func TestPluginMarketIconHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	rootPriv, online := adminFixedKey(0x11), adminFixedKey(0x33)
	issued := time.Now().UTC().Truncate(time.Second)
	expires := issued.Add(7 * 24 * time.Hour)

	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	goodPNG := adminSolidPNG(t, 64, 64, white)
	goodSum := marketproto.FileSHA256(goodPNG)
	swapPNG := adminSolidPNG(t, 96, 96, white)
	swapSum := marketproto.FileSHA256(swapPNG)
	widePNG := adminSolidPNG(t, 80, 40, white)
	wideSum := marketproto.FileSHA256(widePNG)
	raw := []byte("not a png")
	rawSum := marketproto.FileSHA256(raw)
	huge := bytes.Repeat([]byte{0x89}, 64<<10+1)
	hugeSum := marketproto.FileSHA256(huge)

	plugin := func(id string, icon *marketproto.Icon) marketproto.Plugin {
		t.Helper()
		item := adminPlugin(id, jsplugin.KindQuotaProbe, []string{"fp"}, adminJunkVersion(t, online, id, "fp", []byte("x"), issued))
		item.Icon = icon
		return item
	}
	plugins := []marketproto.Plugin{
		plugin("acme.good", &marketproto.Icon{Path: "icons/" + goodSum + ".png", SHA256: goodSum}),
		plugin("acme.swap", &marketproto.Icon{Path: "icons/" + swapSum + ".png", SHA256: swapSum}),
		plugin("acme.wide", &marketproto.Icon{Path: "icons/" + wideSum + ".png", SHA256: wideSum}),
		plugin("acme.raw", &marketproto.Icon{Path: "icons/" + rawSum + ".png", SHA256: rawSum}),
		plugin("acme.huge", &marketproto.Icon{Path: "icons/" + hugeSum + ".png", SHA256: hugeSum}),
		plugin("acme.plain", nil),
	}
	docs := adminSignCatalog(t, rootPriv, online, 1, 1, 1, issued, expires, plugins, nil)
	cloud := newAdminCloud()
	cloud.put("root.json", docs[0])
	cloud.put("index.json", docs[1])
	cloud.put("revocations.json", docs[2])
	cloud.put("icons/"+goodSum+".png", goodPNG)
	cloud.put("icons/"+swapSum+".png", []byte("different"))
	cloud.put("icons/"+wideSum+".png", widePNG)
	cloud.put("icons/"+rawSum+".png", raw)
	cloud.put("icons/"+hugeSum+".png", huge)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	clearAdminProxyEnv(t)
	svc, err := market.NewService(repo, httpclient.NewManager(nil), nil, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetConfig(config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	if status, err := svc.SyncOnce(context.Background(), true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v %v", status, err)
	}
	r := chi.NewRouter()
	RegisterPluginMarket(r, Handler{}.WithPluginMarket(svc))
	host := srv.URL

	list := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market", ""))
	adminAssertStatus(t, list, http.StatusOK)
	catalog := adminDecodeJSON[market.Catalog](t, list)
	byID := map[string]market.PluginView{}
	for _, item := range catalog.Plugins {
		byID[item.ID] = item
	}
	if byID["acme.good"].IconURL != "/api/v1/plugin-market/icons/"+goodSum || byID["acme.plain"].IconURL != "" {
		t.Fatalf("catalog icons good=%q plain=%q", byID["acme.good"].IconURL, byID["acme.plain"].IconURL)
	}
	detail := adminPerform(r.ServeHTTP, adminTestRequest(http.MethodGet, "/plugin-market/acme.good", ""))
	adminAssertStatus(t, detail, http.StatusOK)
	if adminDecodeJSON[market.PluginView](t, detail).IconURL != "/api/v1/plugin-market/icons/"+goodSum {
		t.Fatalf("detail = %s", detail.Body.String())
	}

	get := func(sum, match string) *httptest.ResponseRecorder {
		t.Helper()
		req := adminTestRequest(http.MethodGet, "/plugin-market/icons/"+sum, "")
		if match != "" {
			req.Header.Set("If-None-Match", match)
		}
		return adminPerform(r.ServeHTTP, req)
	}
	assertNoAddress := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(rec.Body.String(), host) {
			t.Fatalf("response contains a registry address: %s", rec.Body.String())
		}
	}

	bad := get("not-a-sha", "")
	assertAdminErrorCode(t, bad, http.StatusNotFound, "js_plugin_market_not_found")
	assertNoAddress(bad)
	upper := get(strings.ToUpper(goodSum), "")
	assertAdminErrorCode(t, upper, http.StatusNotFound, "js_plugin_market_not_found")
	missing := get(strings.Repeat("cd", 32), "")
	assertAdminErrorCode(t, missing, http.StatusNotFound, "js_plugin_market_not_found")
	goodName := "icons/" + goodSum + ".png"
	if cloud.hit(goodName) != 0 {
		t.Fatal("rejected hashes touched the cloud")
	}

	cloud.mu.Lock()
	cloud.blockName = goodName
	cloud.entered = make(chan struct{})
	cloud.release = make(chan struct{})
	cloud.mu.Unlock()
	const n = 8
	done := make(chan *httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		go func() {
			done <- get(goodSum, "")
		}()
	}
	select {
	case <-cloud.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the icon fetch")
	}
	close(cloud.release)
	for i := 0; i < n; i++ {
		rec := <-done
		adminAssertStatus(t, rec, http.StatusOK)
		assertMarketIconHeaders(t, rec, goodSum)
		if !bytes.Equal(rec.Body.Bytes(), goodPNG) {
			t.Fatalf("icon len = %d", rec.Body.Len())
		}
		assertNoAddress(rec)
	}
	if cloud.hit(goodName) != 1 {
		t.Fatalf("good hits = %d", cloud.hit(goodName))
	}
	cached := get(goodSum, "")
	adminAssertStatus(t, cached, http.StatusOK)
	if !bytes.Equal(cached.Body.Bytes(), goodPNG) || cloud.hit(goodName) != 1 {
		t.Fatalf("second request missed the cache, hits = %d", cloud.hit(goodName))
	}
	notModified := get(goodSum, `"`+goodSum+`"`)
	adminAssertStatus(t, notModified, http.StatusNotModified)
	assertMarketIconHeaders(t, notModified, goodSum)
	if notModified.Body.Len() != 0 || cloud.hit(goodName) != 1 {
		t.Fatalf("not modified body %d hits %d", notModified.Body.Len(), cloud.hit(goodName))
	}
	weak := get(goodSum, `W/"`+goodSum+`"`)
	adminAssertStatus(t, weak, http.StatusNotModified)
	if cloud.hit(goodName) != 1 {
		t.Fatal("weak etag fetched the icon")
	}

	swapName := "icons/" + swapSum + ".png"
	before := cloud.hit(swapName)
	mismatch := get(swapSum, "")
	assertAdminErrorCode(t, mismatch, http.StatusBadGateway, "js_plugin_market_icon_failed")
	assertNoAddress(mismatch)
	if cloud.hit(swapName) != before+1 {
		t.Fatalf("mismatch hits = %d", cloud.hit(swapName))
	}
	again := get(swapSum, "")
	assertAdminErrorCode(t, again, http.StatusBadGateway, "js_plugin_market_icon_failed")
	if cloud.hit(swapName) != before+2 {
		t.Fatalf("mismatch was cached, hits = %d", cloud.hit(swapName))
	}
	cloud.put(swapName, swapPNG)
	fixed := get(swapSum, "")
	adminAssertStatus(t, fixed, http.StatusOK)
	if !bytes.Equal(fixed.Body.Bytes(), swapPNG) || cloud.hit(swapName) != before+3 {
		t.Fatalf("swap retry hits = %d", cloud.hit(swapName))
	}

	for _, sum := range []string{rawSum, wideSum, hugeSum} {
		name := "icons/" + sum + ".png"
		start := cloud.hit(name)
		first := get(sum, "")
		assertAdminErrorCode(t, first, http.StatusBadGateway, "js_plugin_market_icon_failed")
		assertNoAddress(first)
		second := get(sum, "")
		assertAdminErrorCode(t, second, http.StatusBadGateway, "js_plugin_market_icon_failed")
		if cloud.hit(name) != start+2 {
			t.Fatalf("%s hits = %d, start %d", name, cloud.hit(name), start)
		}
	}

	svc.SetConfig(config.PluginMarketConfig{Enabled: false, IndexSyncHours: 6, SourceURL: base})
	kept := cloud.hit(goodName)
	closed := get(goodSum, "")
	assertAdminErrorCode(t, closed, http.StatusNotFound, "js_plugin_market_not_found")
	assertNoAddress(closed)
	if cloud.hit(goodName) != kept {
		t.Fatal("disabled market fetched an icon")
	}
}

func assertMarketIconHeaders(t *testing.T, rec *httptest.ResponseRecorder, sum string) {
	t.Helper()
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("ETag") != `"`+sum+`"` {
		t.Fatalf("etag = %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff = %q", rec.Header().Get("X-Content-Type-Options"))
	}
	if rec.Header().Get("Content-Security-Policy") != "default-src 'none'" {
		t.Fatalf("csp = %q", rec.Header().Get("Content-Security-Policy"))
	}
}
