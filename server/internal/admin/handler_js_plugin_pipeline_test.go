package admin

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
)

func pipelineAdminManifest(source string) jsplugin.Manifest {
	return jsplugin.Manifest{
		ID: "acme.pipeline", Name: "Pipeline", Version: "1.0.0", APIVersion: 1, HostAPI: 2,
		Permissions: []string{jsplugin.PermissionPayloadInspectRaw},
		GatewayPipeline: &jsplugin.GatewayPipelineSection{
			Scope:         jsplugin.PipelineScopeAPIKey,
			Exclusive:     true,
			FailurePolicy: jsplugin.PipelineFailClosed,
			Transform:     jsplugin.PipelineTransformSpec{Mode: jsplugin.PipelineTransformTokenPlaceholder},
			Types:         []jsplugin.PipelineTypeSpec{{Type: "PHONE"}},
			Rules:         []jsplugin.PipelineRuleSpec{{Type: "PHONE", Pattern: `1[3-9]\d{9}`, Boundary: "digit"}},
			Inputs:        []jsplugin.AutomationInputSpec{{Name: "keys", Type: jsplugin.EntityAPIKey, Multiple: true}},
		},
		SHA256: map[string]string{"plugin.js": jsplugin.HashSource(source)},
	}
}

func pipelineCall(t *testing.T, handler func(http.ResponseWriter, *http.Request), method string, vars map[string]string, body string) *httptestResponse {
	t.Helper()
	req := adminRequestWithRouteVars(adminTestRequest(method, "/", body), vars)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := adminPerform(handler, req)
	return &httptestResponse{Code: rec.Code, Body: rec.Body}
}

func TestJSPluginPipelineBindingsHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	manager := jsplugin.NewManager(st, &jsplugin.Catalog{})
	h := Handler{}.WithJSPlugins(manager)
	ctx := context.Background()

	source := "export const meta = { apiVersion: 1, id: \"acme.pipeline\" };\n"
	manifest := pipelineAdminManifest(source)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jsplugin.AttachSignature(mustPluginPackage(t, manifest, source, nil), priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upload(ctx, "", signed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AddTrustedKey(ctx, "", "acme", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, manifest.ID, manifest.Version, jsplugin.EnableOptions{GrantPermissions: manifest.Permissions}); err != nil {
		t.Fatal(err)
	}
	key := store.APIKey{
		Name: "k", KeyPrefix: "k", KeyHash: uuid.NewString(), MaskedKey: "k", KeyKind: "generated",
		Scope: "gateway", Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", BillingMultiplier: 1,
		QuotaUnlimited: true, QuotaDailyUnlimited: true, QuotaWeeklyUnlimited: true,
	}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"id": manifest.ID}

	created := pipelineCall(t, h.CreateJSPluginBinding, http.MethodPost, vars, `{"name":"客服","inputs":{"keys":["`+key.ID.String()+`"]},"host_options":{"propagateSameValue":false}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil || createdBody.ID == "" {
		t.Fatalf("create body: %v %s", err, created.Body.String())
	}
	taken := pipelineCall(t, h.CreateJSPluginBinding, http.MethodPost, vars, `{"inputs":{"keys":["`+key.ID.String()+`"]}}`)
	if body := decodeLifecycleError(t, taken); taken.Code != http.StatusBadRequest || body.Error.Code != "js_plugin_binding_invalid" {
		t.Fatalf("taken: %d %#v", taken.Code, body.Error)
	}

	listed := pipelineCall(t, h.ListJSPluginBindings, http.MethodGet, vars, "")
	var listBody struct {
		Items       []jsplugin.PipelineBindingView      `json:"items"`
		HostOptions []jsplugin.PipelineHostOptionSpec `json:"host_options"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil || len(listBody.Items) != 1 || listBody.Items[0].Name != "客服" || listBody.Items[0].HostOptions.PropagateSameValue || len(listBody.HostOptions) != 3 {
		t.Fatalf("list: %v %s", err, listed.Body.String())
	}

	options := pipelineCall(t, h.ListJSPluginPipelineInputOptions, http.MethodGet, map[string]string{"id": manifest.ID, "name": "keys"}, "")
	var optionBody struct {
		Items []jsplugin.AutomationOption `json:"items"`
	}
	if err := json.Unmarshal(options.Body.Bytes(), &optionBody); err != nil || len(optionBody.Items) != 1 || optionBody.Items[0].ReasonCode != jsplugin.ReasonAlreadyBound {
		t.Fatalf("options: %v %s", err, options.Body.String())
	}

	summary, ok := h.pipelineBindingPayload(key.ID).(jsplugin.PipelineBindingSummary)
	if !ok || summary.ID != createdBody.ID || summary.Status != "active" || summary.PluginID != manifest.ID {
		t.Fatalf("api key summary = %#v", h.pipelineBindingPayload(key.ID))
	}

	if err := manager.Disable(ctx, manifest.ID); err != nil {
		t.Fatal(err)
	}
	blocked := deletePlugin(t, h, manifest.ID)
	if body := decodeLifecycleError(t, blocked); blocked.Code != http.StatusConflict || body.Error.Code != "js_plugin_in_use" {
		t.Fatalf("delete with fail_closed binding: %d %#v", blocked.Code, body.Error)
	}
	removed := pipelineCall(t, h.DeleteJSPluginBinding, http.MethodDelete, map[string]string{"id": manifest.ID, "binding_id": createdBody.ID}, "")
	if removed.Code != http.StatusOK {
		t.Fatalf("unbind while disabled: %d %s", removed.Code, removed.Body.String())
	}
	if deleted := deletePlugin(t, h, manifest.ID); deleted.Code != http.StatusOK {
		t.Fatalf("delete released plugin: %d %s", deleted.Code, deleted.Body.String())
	}
	if h.pipelineBindingPayload(key.ID) != nil {
		t.Fatal("the key has no binding any more")
	}
}

func TestDryRunPipelineBindingHTTP(t *testing.T) {
	st := openAdminJSPluginStore(t)
	manager := jsplugin.NewManager(st, &jsplugin.Catalog{})
	h := Handler{}.WithJSPlugins(manager)
	ctx := context.Background()

	source := "export const meta = { apiVersion: 1, id: \"acme.pipeline.dryrun\" };\n"
	manifest := pipelineAdminManifest(source)
	manifest.ID = "acme.pipeline.dryrun"
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jsplugin.AttachSignature(mustPluginPackage(t, manifest, source, nil), priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upload(ctx, "", signed); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AddTrustedKey(ctx, "", "acme2", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, manifest.ID, manifest.Version, jsplugin.EnableOptions{GrantPermissions: manifest.Permissions}); err != nil {
		t.Fatal(err)
	}
	key := store.APIKey{
		Name: "k2", KeyPrefix: "k2", KeyHash: uuid.NewString(), MaskedKey: "k2", KeyKind: "generated",
		Scope: "gateway", Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", BillingMultiplier: 1,
		QuotaUnlimited: true, QuotaDailyUnlimited: true, QuotaWeeklyUnlimited: true,
	}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}

	created := pipelineCall(t, h.CreateJSPluginBinding, http.MethodPost, map[string]string{"id": manifest.ID},
		`{"name":"dryrun-policy","inputs":{"keys":["`+key.ID.String()+`"]},"host_options":{}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create binding: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &createdBody)

	dry := pipelineCall(t, h.DryRunJSPluginBinding, http.MethodPost, map[string]string{"id": manifest.ID, "binding_id": createdBody.ID},
		`{"text":"联系电话 13812345678"}`)
	if dry.Code != http.StatusOK {
		t.Fatalf("dry-run: %d %s", dry.Code, dry.Body.String())
	}
	var dryOut struct {
		Outcome         string                          `json:"outcome"`
		Changed         bool                            `json:"changed"`
		TransformedText string                          `json:"transformed_text"`
		Findings        []jsPluginPipelineDryRunFinding `json:"findings"`
	}
	if err := json.Unmarshal(dry.Body.Bytes(), &dryOut); err != nil {
		t.Fatalf("decode dry-run: %v %s", err, dry.Body.String())
	}
	if dryOut.Outcome != "transformed" || !dryOut.Changed || !strings.Contains(dryOut.TransformedText, "[[PHONE_1]]") {
		t.Fatalf("dry-run result = %+v", dryOut)
	}
	if len(dryOut.Findings) != 1 {
		t.Fatalf("dry-run findings = %+v, want 1", dryOut.Findings)
	}
}
