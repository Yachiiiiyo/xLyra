package jsplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"xlyra/server/internal/store"
)

func TestValidateIconBytes(t *testing.T) {
	t.Parallel()
	white := color.NRGBA{255, 255, 255, 255}
	if err := ValidateIconBytes(solidPNG(t, 64, 64, white)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIconBytes(solidPNG(t, 1024, 1024, white)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateIconBytes([]byte("not a png")); err == nil || err.Error() != "icon 不是合法的 PNG" {
		t.Fatalf("not png: %v", err)
	}
	if err := ValidateIconBytes(bytes.Repeat([]byte{0x89}, maxSiteProviderIconBytes+1)); err == nil || err.Error() != "icon 不能超过 65536 字节" {
		t.Fatalf("too big: %v", err)
	}
	if err := ValidateIconBytes(solidPNG(t, 300, 200, white)); err == nil || err.Error() != "icon 必须是方形，现在是 300×200" {
		t.Fatalf("not square: %v", err)
	}
	if err := ValidateIconBytes(solidPNG(t, 63, 63, white)); err == nil || err.Error() != "icon 边长必须在 64–1024 之间，现在是 63×63" {
		t.Fatalf("too small: %v", err)
	}
	if err := ValidateIconBytes(solidPNG(t, 1025, 1025, color.NRGBA{200, 200, 200, 255})); err == nil || err.Error() != "icon 边长必须在 64–1024 之间，现在是 1025×1025" {
		t.Fatalf("too large: %v", err)
	}
}

func TestPluginIconStructure(t *testing.T) {
	t.Parallel()
	white := solidPNG(t, 64, 64, color.NRGBA{255, 255, 255, 255})
	cases := []struct {
		name string
		path string
		body []byte
		add  bool
		want string
	}{
		{name: "missing", path: "assets/icon.png", want: `icon "assets/icon.png" 不在包内`},
		{name: "extension", path: "assets/icon.webp", body: white, add: true, want: "icon 只能是 .png 文件"},
		{name: "not png", path: "assets/icon.png", body: []byte("not a png"), add: true, want: "icon 不是合法的 PNG"},
		{name: "too big", path: "assets/icon.png", body: bytes.Repeat([]byte{0x89}, maxSiteProviderIconBytes+1), add: true, want: "icon 不能超过 65536 字节"},
		{name: "not square", path: "assets/icon.png", body: solidPNG(t, 300, 200, color.NRGBA{255, 255, 255, 255}), add: true, want: "icon 必须是方形，现在是 300×200"},
		{name: "too small", path: "assets/icon.png", body: solidPNG(t, 63, 63, color.NRGBA{255, 255, 255, 255}), add: true, want: "icon 边长必须在 64–1024 之间，现在是 63×63"},
		{name: "too large", path: "assets/icon.png", body: solidPNG(t, 1025, 1025, color.NRGBA{200, 200, 200, 255}), add: true, want: "icon 边长必须在 64–1024 之间，现在是 1025×1025"},
		{name: "dotdot", path: "../icon.png", want: "必须是包内相对路径"},
		{name: "dotdot nested", path: "assets/../../icon.png", want: "必须是包内相对路径"},
		{name: "absolute", path: "/assets/icon.png", want: "必须是包内相对路径"},
		{name: "backslash", path: `assets\icon.png`, want: "必须是包内相对路径"},
		{name: "colon", path: "C:/icon.png", want: "必须是包内相对路径"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var extra map[string][]byte
			if tc.add {
				extra = map[string][]byte{tc.path: tc.body}
			}
			err := readErr(quotaIconPackage(t, "acme.probe", "1.0.0", tc.path, extra))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	for _, side := range []int{64, 1024} {
		raw := quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
			"assets/icon.png": solidPNG(t, side, side, color.NRGBA{255, 255, 255, 255}),
		})
		if err := readErr(raw); err != nil {
			t.Fatalf("side %d: %v", side, err)
		}
	}
}

func TestSiteProviderDisplayIconIgnoresNewRules(t *testing.T) {
	t.Parallel()
	source := meta("acme.example", KindSiteProvider)
	manifest := siteProviderManifest(source)
	manifest.SiteProvider.Display.Icon = "assets/icon.png"
	raw := siteProviderZip(t, manifest, source, map[string][]byte{
		"assets/icon.png": solidPNG(t, 8, 8, color.NRGBA{0, 0, 0, 255}),
	})
	if err := readErr(raw); err != nil {
		t.Fatal(err)
	}
	report, err := CheckIconLegibility(raw, manifest)
	if err != nil || report.Path != "" {
		t.Fatalf("display icon was checked: %+v %v", report, err)
	}
}

func TestCheckIconLegibility(t *testing.T) {
	t.Parallel()
	darkMsg := "图标有透明背景且图形偏暗，在 xLyra 的深色界面上看不清（平均亮度 0.00，要求不低于 0.13）。请改用不透明底色的方形图标，或使用浅色/明亮的图形"

	opaqueDark := iconZip(t, solidPNG(t, 64, 64, color.NRGBA{0, 0, 0, 255}))
	report, err := CheckIconLegibility(opaqueDark, iconManifest())
	if err != nil {
		t.Fatal(err)
	}
	if report.Path != "assets/icon.png" || report.Width != 64 || report.Height != 64 || report.OpaqueRatio != 1 || report.Luminance != 0 {
		t.Fatalf("opaque dark = %+v", report)
	}

	whiteGlyph := iconZip(t, glyphPNG(t, color.NRGBA{255, 255, 255, 255}))
	report, err = CheckIconLegibility(whiteGlyph, iconManifest())
	if err != nil {
		t.Fatal(err)
	}
	if report.OpaqueRatio >= 0.98 || report.Luminance < minIconLuminance {
		t.Fatalf("white glyph = %+v", report)
	}

	blackGlyph := iconZip(t, glyphPNG(t, color.NRGBA{0, 0, 0, 255}))
	if _, err := CheckIconLegibility(blackGlyph, iconManifest()); err == nil || err.Error() != darkMsg {
		t.Fatalf("black glyph err = %v", err)
	}

	blank := iconZip(t, solidPNG(t, 64, 64, color.NRGBA{0, 0, 0, 0}))
	if _, err := CheckIconLegibility(blank, iconManifest()); err == nil || err.Error() != "图标是空的" {
		t.Fatalf("blank err = %v", err)
	}

	exact := iconZip(t, mixedPNG(t, 100, 100, 9800, color.NRGBA{0, 0, 0, 255}, color.NRGBA{0, 0, 0, 0}))
	report, err = CheckIconLegibility(exact, iconManifest())
	if err != nil {
		t.Fatal(err)
	}
	if report.OpaqueRatio != 0.98 {
		t.Fatalf("opaque ratio = %v", report.OpaqueRatio)
	}

	under := iconZip(t, mixedPNG(t, 100, 100, 9799, color.NRGBA{0, 0, 0, 255}, color.NRGBA{0, 0, 0, 0}))
	if _, err := CheckIconLegibility(under, iconManifest()); err == nil || !strings.Contains(err.Error(), "要求不低于 0.13") {
		t.Fatalf("just under 0.98 err = %v", err)
	}

	var failed, passed uint8
	var failedLum, passedLum float64
	for v := 0; v <= 255; v++ {
		raw := iconZip(t, solidPNG(t, 64, 64, color.NRGBA{uint8(v), uint8(v), uint8(v), 200}))
		got, err := CheckIconLegibility(raw, iconManifest())
		if err != nil {
			failed = uint8(v)
			failedLum = got.Luminance
			continue
		}
		passed = uint8(v)
		passedLum = got.Luminance
		break
	}
	if failedLum >= minIconLuminance || passedLum < minIconLuminance || passed != failed+1 {
		t.Fatalf("luminance boundary fail=%d (%.4f) pass=%d (%.4f)", failed, failedLum, passed, passedLum)
	}

	opaqueEdge := iconZip(t, solidPNG(t, 64, 64, color.NRGBA{0, 0, 0, 250}))
	if _, err := CheckIconLegibility(opaqueEdge, iconManifest()); err != nil {
		t.Fatalf("alpha 250: %v", err)
	}
	translucent := iconZip(t, solidPNG(t, 64, 64, color.NRGBA{0, 0, 0, 249}))
	if _, err := CheckIconLegibility(translucent, iconManifest()); err == nil {
		t.Fatal("alpha 249 dark icon was accepted")
	}

	raw := quotaProbeIconPackage(t)
	pkg, err := ReadPackage(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	report, err = CheckIconLegibility(raw, pkg.Manifest)
	if err != nil || report != (IconReport{}) {
		t.Fatalf("old package %+v %v", report, err)
	}
}

func TestUploadRejectsBadIcon(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	dark := quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
		"assets/icon.png": glyphPNG(t, color.NRGBA{0, 0, 0, 255}),
	})
	_, err := m.Upload(context.Background(), "", dark)
	var invalid *InvalidPackageError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "请改用不透明底色的方形图标，或使用浅色/明亮的图形") {
		t.Fatalf("legibility err = %v", err)
	}

	wide := quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
		"assets/icon.png": solidPNG(t, 300, 200, color.NRGBA{255, 255, 255, 255}),
	})
	_, err = m.Upload(context.Background(), "", wide)
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "icon 必须是方形，现在是 300×200") {
		t.Fatalf("structure err = %v", err)
	}
}

func TestPluginIconSourcesAndURL(t *testing.T) {
	t.Parallel()
	pngBytes := solidPNG(t, 64, 64, color.NRGBA{255, 255, 255, 255})
	webp := []byte("RIFF\x00\x00\x00\x00WEBP")

	probe := quotaIconPackage(t, "acme.probe", "1.0.0", "assets/icon.png", map[string][]byte{
		"assets/icon.png": pngBytes,
	})
	row := store.JSPluginVersion{PluginID: "acme.probe", Version: "1.0.0", Package: probe, Status: store.JSPluginStatusVerified}
	got, err := pluginIconFromVersions([]store.JSPluginVersion{row})
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/png" || !bytes.Equal(got.Bytes, pngBytes) {
		t.Fatalf("quota probe icon = %q %d", got.ContentType, len(got.Bytes))
	}

	source := meta("acme.example", KindSiteProvider)
	both := siteProviderManifest(source)
	both.SiteProvider.Display.Icon = "mark.webp"
	both.Icon = "assets/icon.png"
	bothRaw := siteProviderZip(t, both, source, map[string][]byte{
		"mark.webp":       webp,
		"assets/icon.png": pngBytes,
	})
	got, err = pluginIconFromVersions([]store.JSPluginVersion{{
		Package: bothRaw, Status: store.JSPluginStatusEnabled,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/webp" || !bytes.Equal(got.Bytes, webp) {
		t.Fatalf("display icon did not win: %q %d", got.ContentType, len(got.Bytes))
	}

	onlyTop := siteProviderManifest(source)
	onlyTop.Icon = "assets/icon.png"
	onlyRaw := siteProviderZip(t, onlyTop, source, map[string][]byte{"assets/icon.png": pngBytes})
	got, err = pluginIconFromVersions([]store.JSPluginVersion{{Package: onlyRaw, Status: store.JSPluginStatusVerified}})
	if err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Bytes, pngBytes) {
		t.Fatalf("site provider top-level = %q %v", got.ContentType, err)
	}

	sha := strings.Repeat("ab", 32)
	withIcon := store.JSPluginVersion{
		PackageSHA256: sha,
		Manifest:      store.JSON(`{"kind":"quota_probe","icon":"assets/icon.png"}`),
	}
	if got := PluginIconURL("acme.probe", withIcon); got != "/api/v1/plugin-icons/acme.probe?v="+sha[:16] {
		t.Fatalf("url = %q", got)
	}
	display := store.JSPluginVersion{
		PackageSHA256: sha,
		Manifest:      store.JSON(`{"kind":"site_provider","siteProvider":{"display":{"icon":"mark.webp"}}}`),
	}
	if PluginIconURL("acme.example", display) == "" {
		t.Fatal("display icon did not produce a url")
	}
	plain := store.JSPluginVersion{PackageSHA256: sha, Manifest: store.JSON(`{"kind":"quota_probe"}`)}
	if PluginIconURL("acme.probe", plain) != "" {
		t.Fatal("plain manifest produced a url")
	}
	if _, err := pluginIconFromVersions([]store.JSPluginVersion{{Package: quotaProbeIconPackage(t)}}); !errors.Is(err, ErrPluginIconNotFound) {
		t.Fatalf("missing icon err = %v", err)
	}
}

func quotaIconPackage(t *testing.T, id, version, iconPath string, extra map[string][]byte) []byte {
	t.Helper()
	source := meta(id, KindQuotaProbe) + "export function probe() { return { error: \"empty\" }; }\n"
	manifest := Manifest{
		ID: id, Name: id, Version: version, APIVersion: 1, HostAPI: 1,
		Kind: KindQuotaProbe, Icon: iconPath,
		QuotaProbe: QuotaProbeSection{BaseURLMode: "as_is"},
		SHA256:     map[string]string{"plugin.js": hashSource(source)},
	}
	files := map[string][]byte{"fixtures/ok.json": []byte(`{"name":"ok"}`)}
	for name, body := range extra {
		files[name] = body
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files["manifest.json"] = encoded
	files["plugin.js"] = []byte(source)
	raw, err := BuildPackage(files)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func iconManifest() Manifest {
	return Manifest{Icon: "assets/icon.png"}
}

func iconZip(t *testing.T, pngBytes []byte) []byte {
	t.Helper()
	raw, err := BuildPackage(map[string][]byte{
		"manifest.json":   []byte(`{"icon":"assets/icon.png"}`),
		"plugin.js":       []byte("export {}\n"),
		"assets/icon.png": pngBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func solidPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fillNRGBA(img, c)
	return encodePNG(t, img)
}

func glyphPNG(t *testing.T, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	fillNRGBA(img, color.NRGBA{0, 0, 0, 0})
	for y := 16; y < 48; y++ {
		for x := 16; x < 48; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return encodePNG(t, img)
}

func mixedPNG(t *testing.T, w, h, n int, fg, bg color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fillNRGBA(img, bg)
	for i := 0; i < n; i++ {
		img.SetNRGBA(i%w, i/w, fg)
	}
	return encodePNG(t, img)
}

func fillNRGBA(img *image.NRGBA, c color.NRGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
