package market

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
	"xlyra/server/internal/store"
)

func TestIconCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newIconCache()
	for i := 0; i < marketIconCacheLimit; i++ {
		cache.put(fmt.Sprintf("%064x", i), []byte{byte(i)})
	}
	if _, ok := cache.get(fmt.Sprintf("%064x", 0)); !ok {
		t.Fatal("oldest entry missing before overflow")
	}
	cache.put(fmt.Sprintf("%064x", marketIconCacheLimit), []byte{9})
	if _, ok := cache.get(fmt.Sprintf("%064x", 1)); ok {
		t.Fatal("least recently used entry was kept")
	}
	if _, ok := cache.get(fmt.Sprintf("%064x", 0)); !ok {
		t.Fatal("recently used entry was evicted")
	}
	over := bytes.Repeat([]byte{1}, maxMarketIconBytes+1)
	cache.put(strings.Repeat("ab", 32), over)
	if _, ok := cache.get(strings.Repeat("ab", 32)); ok {
		t.Fatal("oversized icon was cached")
	}
}

func TestMarketIcon(t *testing.T) {
	st := openMarketStore(t)
	repo := store.NewJSPluginRepository(st.DB())
	rootPriv, online := testKeys()
	issued, exp := freshWindow()

	goodPNG := marketSolidPNG(t, 64, 64)
	goodSum := marketproto.FileSHA256(goodPNG)
	swapPNG := marketSolidPNG(t, 96, 96)
	swapSum := marketproto.FileSHA256(swapPNG)
	widePNG := marketSolidPNG(t, 80, 40)
	wideSum := marketproto.FileSHA256(widePNG)
	raw := []byte("not a png")
	rawSum := marketproto.FileSHA256(raw)
	huge := bytes.Repeat([]byte{0x89}, maxMarketIconBytes+1)
	hugeSum := marketproto.FileSHA256(huge)

	plugin := func(id string, icon *marketproto.Icon) marketproto.Plugin {
		t.Helper()
		item := pluginDoc(id, jsplugin.KindQuotaProbe, []string{"fp"}, junkEntry(t, online, id, "fp", []byte("x"), issued))
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
	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, plugins, nil))
	cloud.put("icons/"+goodSum+".png", goodPNG)
	cloud.put("icons/"+swapSum+".png", []byte("different"))
	cloud.put("icons/"+wideSum+".png", widePNG)
	cloud.put("icons/"+rawSum+".png", raw)
	cloud.put("icons/"+hugeSum+".png", huge)
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, conf)
	if status, err := svc.SyncOnce(context.Background(), true); err != nil || status.Error != "" {
		t.Fatalf("sync = %+v %v", status, err)
	}

	cat, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]PluginView{}
	for _, item := range cat.Plugins {
		byID[item.ID] = item
	}
	if byID["acme.good"].IconURL != marketIconURLPrefix+goodSum {
		t.Fatalf("icon url = %q", byID["acme.good"].IconURL)
	}
	if byID["acme.plain"].IconURL != "" {
		t.Fatalf("plain = %q", byID["acme.plain"].IconURL)
	}

	host := srv.URL
	wantMiss := func(sum string, notFound bool) {
		t.Helper()
		_, err := svc.Icon(context.Background(), sum)
		target := ErrIconFailed
		if notFound {
			target = ErrIconNotFound
		}
		if !errors.Is(err, target) {
			t.Fatalf("icon %s err = %v", sum, err)
		}
		if strings.Contains(err.Error(), host) || strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "https://") {
			t.Fatalf("error leaks registry address: %v", err)
		}
	}
	wantMiss("nope", true)
	wantMiss(strings.ToUpper(goodSum), true)
	wantMiss(strings.Repeat("cd", 32), true)
	if cloud.hit("icons/"+goodSum+".png") != 0 {
		t.Fatal("rejected hashes touched the cloud")
	}

	goodName := "icons/" + goodSum + ".png"
	cloud.mu.Lock()
	cloud.blockPath = goodName
	cloud.pathEntered = make(chan struct{})
	cloud.pathRelease = make(chan struct{})
	cloud.mu.Unlock()
	const n = 8
	type iconResult struct {
		body []byte
		err  error
	}
	results := make(chan iconResult, n)
	for i := 0; i < n; i++ {
		go func() {
			body, err := svc.Icon(context.Background(), goodSum)
			results <- iconResult{body: body, err: err}
		}()
	}
	select {
	case <-cloud.pathEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the icon fetch")
	}
	close(cloud.pathRelease)
	for i := 0; i < n; i++ {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !bytes.Equal(got.body, goodPNG) {
			t.Fatalf("concurrent icon len = %d", len(got.body))
		}
		if strings.Contains(string(got.body), host) {
			t.Fatal("icon bytes contain the cloud address")
		}
	}
	if cloud.hit(goodName) != 1 {
		t.Fatalf("good hits = %d", cloud.hit(goodName))
	}
	again, err := svc.Icon(context.Background(), goodSum)
	if err != nil || !bytes.Equal(again, goodPNG) {
		t.Fatal(err)
	}
	if cloud.hit(goodName) != 1 {
		t.Fatalf("cache miss on the second read, hits = %d", cloud.hit(goodName))
	}

	swapName := "icons/" + swapSum + ".png"
	before := cloud.hit(swapName)
	wantMiss(swapSum, false)
	if cloud.hit(swapName) != before+1 {
		t.Fatalf("mismatch hits = %d, before %d", cloud.hit(swapName), before)
	}
	wantMiss(swapSum, false)
	if cloud.hit(swapName) != before+2 {
		t.Fatalf("mismatch was cached, hits = %d", cloud.hit(swapName))
	}
	cloud.put(swapName, swapPNG)
	swapped, err := svc.Icon(context.Background(), swapSum)
	if err != nil || !bytes.Equal(swapped, swapPNG) {
		t.Fatalf("swap retry = %v len %d", err, len(swapped))
	}
	if cloud.hit(swapName) != before+3 {
		t.Fatalf("swap fetch hits = %d", cloud.hit(swapName))
	}

	for _, sum := range []string{rawSum, wideSum, hugeSum} {
		name := "icons/" + sum + ".png"
		start := cloud.hit(name)
		wantMiss(sum, false)
		wantMiss(sum, false)
		if cloud.hit(name) != start+2 {
			t.Fatalf("%s hits = %d, start %d", name, cloud.hit(name), start)
		}
	}

	if err := conf.Set(config.PluginMarketConfigPath, config.PluginMarketConfigToMap(config.PluginMarketConfig{
		Enabled: false, IndexSyncHours: 6, SourceURL: base,
	})); err != nil {
		t.Fatal(err)
	}
	kept := cloud.hit(goodName)
	wantMiss(goodSum, true)
	if cloud.hit(goodName) != kept {
		t.Fatal("disabled market fetched an icon")
	}
}

func TestMarketIconRejectsMalformedIndexIcon(t *testing.T) {
	repo := store.NewJSPluginRepository(openMarketStore(t).DB())
	rootPriv, online := testKeys()
	issued, exp := freshWindow()
	goodSum := strings.Repeat("ab", 32)
	otherSum := strings.Repeat("cd", 32)
	upperSum := strings.ToUpper(goodSum)
	plugin := func(id string, icon marketproto.Icon) marketproto.Plugin {
		t.Helper()
		item := pluginDoc(id, jsplugin.KindQuotaProbe, []string{"fp"}, junkEntry(t, online, id, "fp", []byte("x"), issued))
		item.Icon = &icon
		return item
	}
	cases := []marketproto.Plugin{
		plugin("acme.remote", marketproto.Icon{Path: "https://evil.example/steal.png", SHA256: goodSum}),
		plugin("acme.mismatch", marketproto.Icon{Path: "icons/" + otherSum + ".png", SHA256: goodSum}),
		plugin("acme.upper", marketproto.Icon{Path: "icons/" + upperSum + ".png", SHA256: upperSum}),
	}

	cloud := newCloud(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, []marketproto.Plugin{}, nil))
	for _, item := range cases {
		cloud.put(item.Icon.Path, []byte("x"))
	}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	base := srv.URL + "/"
	conf := marketConf(t, config.PluginMarketConfig{Enabled: true, IndexSyncHours: 6, SourceURL: base, AutoUpdate: true})
	svc := newTestService(t, repo, []ed25519.PublicKey{rootPriv.Public().(ed25519.PublicKey)}, []string{base}, conf)
	ctx := context.Background()

	for _, item := range cases {
		cloud.use(signCatalog(t, rootPriv, online, 1, 1, 1, issued, exp, []marketproto.Plugin{item}, nil))
		status, err := svc.SyncOnce(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(status.Error, marketproto.CodeBadField) {
			t.Fatalf("%s status error = %q", item.ID, status.Error)
		}
		snap, err := svc.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Index != nil {
			t.Fatalf("cached index contains %d plugins", len(snap.Index.Plugins))
		}
		if cloud.hit(item.Icon.Path) != 0 {
			t.Fatalf("%s hits = %d", item.Icon.Path, cloud.hit(item.Icon.Path))
		}
	}
}

func marketSolidPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
