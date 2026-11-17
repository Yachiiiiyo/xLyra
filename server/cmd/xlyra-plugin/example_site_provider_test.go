package main

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The checked-in site_provider example must build, verify, and pass its fixtures.
func TestSiteProviderExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "site-provider-minimal")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "site-provider-minimal")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{
		"example.minimal@1.0.0",
		"nonstandard model list",
		"vendor rate limit",
		"other error",
		"drop user",
		"hook=listModels",
		"hook=classify",
		"hook=rewritePayload",
		"4 passed, 0 failed",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}

	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, pkg, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	for _, needle := range []string{"example.minimal@1.0.0", "4 fixtures", "hook=listModels", "hook=rewritePayload"} {
		if !strings.Contains(verifyOut, needle) {
			t.Fatalf("verify output missing %q:\n%s", needle, verifyOut)
		}
	}

	section := pkg.Manifest.SiteProvider
	if section.SiteType != "example.minimal/chat" {
		t.Fatalf("siteType = %q", section.SiteType)
	}
	parsed, err := url.Parse(section.Protocol.OfficialBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.ToLower(parsed.Hostname())
	if len(section.Hosts) != 1 || section.Hosts[0] != host {
		t.Fatalf("hosts = %v, official host = %q", section.Hosts, host)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

// TestSiteProviderExampleTypesMatchTemplate keeps the checked-in example's
// xlyra.d.ts identical to the scaffold template.
func TestSiteProviderExampleTypesMatchTemplate(t *testing.T) {
	root := repoRoot(t)
	templatePath := filepath.Join(root, "server", "cmd", "xlyra-plugin", "templates", "types", "xlyra.d.ts")
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, exampleDir := range []string{"site-provider-minimal", "stage1-primitives-demo", "hooks-overlay-demo", "request-overlay-demo", "transport-overlay-demo", "schedule-http-demo"} {
		examplePath := filepath.Join(root, "docs", "plugins", "examples", exampleDir, "types", "xlyra.d.ts")
		example, err := os.ReadFile(examplePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(example) != string(template) {
			t.Fatalf("%s: 运行 go -C server generate ./cmd/xlyra-plugin 后把模板里的 xlyra.d.ts 复制到示例包", exampleDir)
		}
	}
}

func TestStage1PrimitivesExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "stage1-primitives-demo")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "stage1-primitives-demo")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{
		"example.primitives@1.0.0",
		"probe quota and increment storage counter",
		"rewrite payload with crypto and settings",
		"hook=probe",
		"hook=rewritePayload",
		"2 passed, 0 failed",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}

	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

func TestHooksOverlayExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "hooks-overlay-demo")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "hooks-overlay-demo")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{"example.hooks@1.0.0", "probe balance", "rate limit", "model list", "3 passed, 0 failed"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, pkg, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Kind != "" {
		t.Fatalf("kind = %q, want empty", pkg.Manifest.Kind)
	}
	if strings.Join(pkg.Manifest.Exports, ",") != "classifyError,listModels,probe" {
		t.Fatalf("exports = %v", pkg.Manifest.Exports)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	if !strings.Contains(verifyOut, "(hooks)") {
		t.Fatalf("verify kind label missing:\n%s", verifyOut)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

func TestRequestOverlayExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "request-overlay-demo")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "request-overlay-demo")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{"example.request@1.0.0", "rewrite a header", "1 passed, 0 failed"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, pkg, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Kind != "" || pkg.Manifest.DeclaresSiteType() {
		t.Fatalf("kind = %q site type = %v", pkg.Manifest.Kind, pkg.Manifest.DeclaresSiteType())
	}
	if strings.Join(pkg.Manifest.Exports, ",") != "signRequest" {
		t.Fatalf("exports = %v", pkg.Manifest.Exports)
	}
	if pkg.Manifest.Request.Headers["X-Trace"].Value != "{{random.hex:40}}" || pkg.Manifest.Request.Headers["X-Session"].Value != "{{session.key}}" {
		t.Fatalf("request = %#v", pkg.Manifest.Request.Headers)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

func TestTransportOverlayExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "transport-overlay-demo")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "transport-overlay-demo")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{"example.transport@1.0.0", "rewrite payload adds overlay metadata", "check credential ping", "classify overloaded error", "3 passed, 0 failed"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, pkg, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.Kind != "" || pkg.Manifest.DeclaresSiteType() {
		t.Fatalf("kind = %q site type = %v", pkg.Manifest.Kind, pkg.Manifest.DeclaresSiteType())
	}
	if pkg.Manifest.TransportProfile() != "claude_code" {
		t.Fatalf("transport profile = %q", pkg.Manifest.TransportProfile())
	}
	if strings.Join(pkg.Manifest.Exports, ",") != "check,classifyError,rewritePayload" {
		t.Fatalf("exports = %v", pkg.Manifest.Exports)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

func TestScheduleHTTPExamplePackage(t *testing.T) {
	root := repoRoot(t)
	src := filepath.Join(root, "docs", "plugins", "examples", "schedule-http-demo")
	before := dirStamp(t, src)
	dir := filepath.Join(t.TempDir(), "schedule-http-demo")
	copyDir(t, src, dir)

	var passed bool
	out := captureStdout(t, func() { passed = testOnce(dir) })
	if !passed {
		t.Fatalf("test failed:\n%s", out)
	}
	for _, needle := range []string{"example.schedule@1.0.0", "scheduled checkin increments storage", "hook=onSchedule", "1 passed, 0 failed"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("test output missing %q:\n%s", needle, out)
		}
	}
	built, err := buildProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, pkg, plugin, err := built.assemble()
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.EffectiveScheduleMinutes() != 60 {
		t.Fatalf("schedule minutes = %d, want 60", pkg.Manifest.EffectiveScheduleMinutes())
	}
	if strings.Join(pkg.Manifest.Exports, ",") != "onSchedule" {
		t.Fatalf("exports = %v", pkg.Manifest.Exports)
	}
	if err := plugin.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	verifyOut := captureStdout(t, func() { err = verifyPackage(raw) })
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, verifyOut)
	}
	if dirStamp(t, src) != before {
		t.Fatal("example package in the repo changed")
	}
}

// repoRoot is the repository root. go test uses the package directory as the
// working directory, the same relative climb as TestSDKTypesInSync.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "server", "go.mod")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func dirStamp(t *testing.T, root string) string {
	t.Helper()
	var parts []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", rel, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}
