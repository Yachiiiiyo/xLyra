package jsplugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateScheduleManifestAndHook(t *testing.T) {
	t.Parallel()
	source := `export const meta = { apiVersion: 1, id: "acme.sched" };
export function onSchedule(ctx, steps) {
  return { result: { ok: true } };
}
`
	base := Manifest{
		ID:          "acme.sched",
		Name:        "acme.sched",
		Version:     "1.0.0",
		APIVersion:  1,
		HostAPI:     2,
		Permissions: []string{"schedule"},
		SHA256:      map[string]string{"plugin.js": HashSource(source)},
	}
	plugin, err := NewPlugin(base, source, nil, 1)
	if err != nil {
		t.Fatalf("default schedule manifest: %v", err)
	}
	if got := plugin.Manifest.EffectiveScheduleMinutes(); got != 60 {
		t.Fatalf("EffectiveScheduleMinutes = %d, want 60", got)
	}

	noPerm := base
	noPerm.Permissions = nil
	if _, err := NewPlugin(noPerm, source, nil, 1); err == nil || !strings.Contains(err.Error(), `"schedule" in permissions`) {
		t.Fatalf("missing schedule permission err = %v", err)
	}

	badInterval := base
	badInterval.Schedule = ScheduleSection{EveryMinutes: 2}
	if _, err := NewPlugin(badInterval, source, nil, 1); err == nil || !strings.Contains(err.Error(), "schedule.everyMinutes") {
		t.Fatalf("bad interval err = %v", err)
	}

	customInterval := base
	customInterval.Schedule = ScheduleSection{EveryMinutes: 30, Label: LocalizedText{"zh": "每日签到"}}
	customPlugin, err := NewPlugin(customInterval, source, nil, 1)
	if err != nil {
		t.Fatalf("custom interval: %v", err)
	}
	if got := customPlugin.Manifest.EffectiveScheduleMinutes(); got != 30 {
		t.Fatalf("EffectiveScheduleMinutes = %d, want 30", got)
	}
}

func TestDriveScheduleRequiresHTTPPermissionAndChecksNetHost(t *testing.T) {
	source := `export const meta = { apiVersion: 1, id: "acme.http" };
export function onSchedule(ctx, steps) {
  if (steps.length === 0) {
    return { request: { method: "POST", path: "/v1/ping", body: { ts: ctx.now } } };
  }
  return { result: { status: steps[0].response.status, echo: steps[0].response.json.echo } };
}
`
	noHTTPManifest := Manifest{
		ID:          "acme.http",
		Name:        "acme.http",
		Version:     "1.0.0",
		APIVersion:  1,
		HostAPI:     2,
		Permissions: []string{"schedule", "net:api.example.com"},
		SHA256:      map[string]string{"plugin.js": HashSource(source)},
	}
	noHTTPPlugin, err := NewPlugin(noHTTPManifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = noHTTPPlugin.DriveSchedule(context.Background(), nil, ScheduleContext{
		BaseURL: "https://api.example.com",
		Now:     1000,
	})
	if err == nil || !strings.Contains(err.Error(), ErrPermissionDenied.Error()) {
		t.Fatalf("expected permission_denied without http permission, got %v", err)
	}

	withHTTPManifest := noHTTPManifest
	withHTTPManifest.Permissions = []string{"schedule", "http", "net:api.example.com"}
	withHTTPPlugin, err := NewPlugin(withHTTPManifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = withHTTPPlugin.DriveSchedule(context.Background(), nil, ScheduleContext{
		BaseURL: "https://evil.example.com",
		Now:     1000,
	})
	if err == nil || !strings.Contains(err.Error(), KindURLRejected) {
		t.Fatalf("expected url_rejected for unallowed host, got %v", err)
	}

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusOK)
		fmt.Fprint(rec, `{"echo":"pong"}`)
		return rec.Result(), nil
	})
	res, err := withHTTPPlugin.DriveSchedule(context.Background(), &http.Client{Transport: transport}, ScheduleContext{
		BaseURL: "https://api.example.com",
		Now:     1000,
	})
	if err != nil {
		t.Fatalf("DriveSchedule with allowed host: %v", err)
	}
	if res["echo"] != "pong" {
		t.Fatalf("result = %#v", res)
	}
}

func TestAccountProviderRunScheduleUsesSecretBag(t *testing.T) {
	const secretToken = "sk-checkin-secret-998877"
	var sawAuth string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		sawAuth = req.Header.Get("Authorization")
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusOK)
		fmt.Fprintf(rec, `{"checkedIn":true,"echoSecret":%q}`, secretToken)
		return rec.Result(), nil
	})

	source := meta("acme.example", KindSiteProvider) + `
export function startLogin() { return { result: { kind: "account_login", status: "complete" } }; }
export function pollLogin() { return { result: { kind: "account_login", status: "complete" } }; }
export function refreshTokens() { return { result: { kind: "account_refresh", status: "ok" } }; }
export function summarizeAccount() { return { result: { kind: "account_summary", status: "ok", entries: [] } }; }
export function onSchedule(ctx, steps) {
  if (steps.length === 0) {
    return {
      request: {
        method: "POST",
        path: "/v1/checkin",
        headers: { Authorization: "Bearer {{secret.access_token}}" }
      }
    };
  }
  const body = String(steps[0].response.body || "");
  if (body.indexOf("sk-checkin-secret-998877") >= 0) {
    return { error: "secret leaked into JS response body" };
  }
  return { result: { ok: Boolean(steps[0].response.json && steps[0].response.json.checkedIn), domain: ctx.fields.domain } };
}
`
	manifest := poolManifest(source, poolSection())
	manifest.HostAPI = 2
	manifest.Permissions = []string{"schedule"}
	plugin, err := NewPlugin(manifest, source, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewAccountProviderRuntime(plugin, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	out, err := rt.RunSchedule(context.Background(), AccountSnapshot{
		BaseURL: "https://api.example.cn",
		Fields:  map[string]string{"access_token": secretToken, "domain": "acme.cn", "realm": "cn"},
	})
	if err != nil {
		t.Fatalf("RunSchedule: %v", err)
	}
	if sawAuth != "Bearer "+secretToken {
		t.Fatalf("sawAuth = %q, want Bearer %s", sawAuth, secretToken)
	}
	if out["ok"] != true || out["domain"] != "acme.cn" {
		t.Fatalf("result = %#v", out)
	}
}
