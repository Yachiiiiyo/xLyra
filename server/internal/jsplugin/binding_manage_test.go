package jsplugin

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
)

func pipelineTestKey(t *testing.T, st *store.Store, name string) store.APIKey {
	t.Helper()
	key := store.APIKey{
		Name: name, KeyPrefix: name, KeyHash: uuid.NewString(), MaskedKey: name, KeyKind: "generated",
		Scope: "gateway", Status: "active", ModelPolicy: "allow_all", SitePolicy: "allow_all", BillingMultiplier: 1,
		QuotaUnlimited: true, QuotaDailyUnlimited: true, QuotaWeeklyUnlimited: true,
	}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return key
}

// installPipelinePlugin uploads a signed pipeline package, trusts its key and enables it.
func installPipelinePlugin(t *testing.T, mgr *Manager, version string, mutate func(*Manifest)) {
	t.Helper()
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	source := pipelineTestSource("// " + version + "\n")
	manifest := pipelineTestManifest(source, mutate)
	manifest.Version = version
	signed, err := AttachSignature(siteProviderZip(t, manifest, source, nil), priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Upload(ctx, "", signed); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := mgr.AddTrustedKey(ctx, "", "acme-"+version, base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Enable(ctx, manifest.ID, version, EnableOptions{GrantPermissions: manifest.Permissions}); err != nil {
		t.Fatalf("enable: %v", err)
	}
}

func TestPipelineBindingNeedsTrustedSignature(t *testing.T) {
	st := openTestStore(t)
	mgr := NewManager(st, &Catalog{})
	ctx := context.Background()
	source := pipelineTestSource("")
	manifest := pipelineTestManifest(source, nil)
	if _, err := mgr.Upload(ctx, "", siteProviderZip(t, manifest, source, nil)); err != nil {
		t.Fatal(err)
	}
	err := mgr.Enable(ctx, manifest.ID, manifest.Version, EnableOptions{ConfirmUntrusted: true, GrantPermissions: manifest.Permissions})
	var untrusted *SiteProviderUntrustedError
	if !errors.As(err, &untrusted) || untrusted.Permission != PermissionPayloadInspectRaw {
		t.Fatalf("unsigned payload.inspect_raw: %v", err)
	}
}

func TestPipelineBindingsAreExclusiveAndFailClosed(t *testing.T) {
	st := openTestStore(t)
	mgr := NewManager(st, &Catalog{})
	ctx := context.Background()
	if err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	installPipelinePlugin(t, mgr, "1.0.0", nil)
	first, second := pipelineTestKey(t, st, "first"), pipelineTestKey(t, st, "second")

	created, err := mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{
		Name:        "客服",
		Inputs:      map[string]any{"keys": []any{first.ID.String()}, "dictionary": "项目代号"},
		HostOptions: map[string]any{"scanToolDefinitions": true},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{Inputs: map[string]any{"keys": []any{first.ID.String(), second.ID.String()}}})
	var inputErr *AutomationInputError
	if !errors.As(err, &inputErr) || !strings.Contains(inputErr.Message, "already bound") || !strings.Contains(inputErr.Message, "客服") {
		t.Fatalf("taken key: %v", err)
	}
	other, err := mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{Inputs: map[string]any{"keys": []any{second.ID.String()}}})
	if err != nil {
		t.Fatalf("free key: %v", err)
	}
	if err := mgr.UpdatePipelineBinding(ctx, pipelineTestID, other.ID, PipelineBindingInput{Inputs: map[string]any{"keys": []any{first.ID.String()}}}); err == nil {
		t.Fatal("update onto a taken key must fail")
	}

	options, err := mgr.PipelineInputOptions(ctx, pipelineTestID, "keys", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, option := range options {
		reasons[option.ID] = option.ReasonCode
	}
	if reasons[first.ID.String()] != ReasonAlreadyBound || reasons[second.ID.String()] != "" {
		t.Fatalf("options = %#v", reasons)
	}

	snap := mgr.Catalog().Current()
	resolved, ok := snap.PipelineByAPIKey(first.ID)
	if !ok || resolved.Status != PipelineBindingActive || resolved.BindingID != created.ID || resolved.Plugin == nil || !resolved.HostOptions.ScanToolDefinitions || !resolved.HostOptions.PropagateSameValue {
		t.Fatalf("active binding = %#v, %v", resolved, ok)
	}
	if resolved.Params["dictionary"] != "项目代号" || resolved.Params["secret"] != true || resolved.ConfigHash == "" {
		t.Fatalf("params = %#v hash = %q", resolved.Params, resolved.ConfigHash)
	}
	if _, ok := snap.PipelineByAPIKey(uuid.New()); ok {
		t.Fatal("an unbound key must not be in the index")
	}
	if resolved.Matcher == nil {
		t.Fatal("an active binding carries its compiled matcher")
	}
	sample := "项目代号 13812345678"
	var found []string
	for _, span := range resolved.Matcher.Match(sample) {
		found = append(found, span.Type+":"+sample[span.Start:span.End])
	}
	if strings.Join(found, ",") != "PHONE:13812345678,CUSTOM:项目代号" && strings.Join(found, ",") != "CUSTOM:项目代号,PHONE:13812345678" {
		t.Fatalf("matcher findings = %q", found)
	}
	if _, err := mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{
		Inputs: map[string]any{"keys": []any{uuid.NewString()}, "dictionary": "x\tNOPE"},
	}); err == nil {
		t.Fatal("a binding with an unknown key must fail")
	}
	third := pipelineTestKey(t, st, "third")
	_, err = mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{Inputs: map[string]any{"keys": []any{third.ID.String()}, "dictionary": "x\tNOPE"}})
	if !errors.As(err, &inputErr) || !strings.Contains(inputErr.Message, "not declared") {
		t.Fatalf("a dictionary with an undeclared type must fail on save: %v", err)
	}

	views, err := mgr.ListPipelineBindings(ctx, pipelineTestID)
	if err != nil || len(views) != 2 || views[0].Name != "客服" || views[1].Name != "Pipeline" || views[0].Policy.FailurePolicy != PipelineFailClosed || views[0].Status != string(PipelineBindingActive) {
		t.Fatalf("views = %#v, %v", views, err)
	}

	if err := mgr.Disable(ctx, pipelineTestID); err != nil {
		t.Fatal(err)
	}
	resolved, ok = mgr.Catalog().Current().PipelineByAPIKey(first.ID)
	if !ok || resolved.Status != PipelineBindingUnavailable || resolved.UnavailableReason != PipelineReasonDisabled || resolved.Plugin != nil {
		t.Fatalf("disabled fail_closed binding = %#v, %v", resolved, ok)
	}
	summary, ok := mgr.Catalog().Current().PipelineSummaryForAPIKey(second.ID)
	if !ok || summary.Status != string(PipelineBindingUnavailable) || summary.PluginName != "Pipeline" {
		t.Fatalf("summary = %#v", summary)
	}

	if err := mgr.Enable(ctx, pipelineTestID, "1.0.0", EnableOptions{GrantPermissions: []string{PermissionPayloadInspectRaw}}); err != nil {
		t.Fatal(err)
	}
	mgr.onBreakerTrip(pipelineTestID, "1.0.0", "protocol_error_rate")
	resolved, ok = mgr.Catalog().Current().PipelineByAPIKey(first.ID)
	if !ok || resolved.Status != PipelineBindingUnavailable || resolved.UnavailableReason != PipelineReasonCircuitOpen {
		t.Fatalf("tripped binding = %#v, %v", resolved, ok)
	}

	var inUse *PipelineBindingInUseError
	if err := mgr.DeleteVersion(ctx, pipelineTestID, "1.0.0"); !errors.As(err, &inUse) || inUse.Count != 2 {
		t.Fatalf("delete bound version: %v", err)
	}
	if err := mgr.DeletePlugin(ctx, pipelineTestID); !errors.As(err, &inUse) {
		t.Fatalf("delete plugin: %v", err)
	}

	// A manager that never loaded the catalog still blocks fail_closed keys.
	fresh := NewManager(st, &Catalog{})
	fresh.blockPipelinesWithoutIndex(ctx)
	resolved, ok = fresh.Catalog().Current().PipelineByAPIKey(first.ID)
	if !ok || resolved.Status != PipelineBindingUnavailable || resolved.UnavailableReason != PipelineReasonReloadFailed {
		t.Fatalf("index without a reload = %#v, %v", resolved, ok)
	}

	for _, id := range []uuid.UUID{created.ID, other.ID} {
		if err := mgr.DeletePipelineBinding(ctx, pipelineTestID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := mgr.Catalog().Current().PipelineByAPIKey(first.ID); ok {
		t.Fatal("an unbound key must pass")
	}
	if err := mgr.DeletePlugin(ctx, pipelineTestID); err != nil {
		t.Fatalf("delete released plugin: %v", err)
	}
}

func TestPipelineFailOpenBindingIsBypassedWhenDisabled(t *testing.T) {
	st := openTestStore(t)
	mgr := NewManager(st, &Catalog{})
	ctx := context.Background()
	installPipelinePlugin(t, mgr, "1.0.0", func(m *Manifest) { m.GatewayPipeline.FailurePolicy = PipelineFailOpen })
	key := pipelineTestKey(t, st, "open")
	if _, err := mgr.CreatePipelineBinding(ctx, pipelineTestID, PipelineBindingInput{Inputs: map[string]any{"keys": key.ID.String()}}); err != nil {
		t.Fatal(err)
	}
	if resolved, ok := mgr.Catalog().Current().PipelineByAPIKey(key.ID); !ok || resolved.Status != PipelineBindingActive || resolved.FailClosed() {
		t.Fatalf("active fail_open = %#v", resolved)
	}
	if err := mgr.Disable(ctx, pipelineTestID); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Catalog().Current().PipelineByAPIKey(key.ID); ok {
		t.Fatal("a disabled fail_open binding must not be in the hot index")
	}
	if summary, ok := mgr.Catalog().Current().PipelineSummaryForAPIKey(key.ID); !ok || summary.Status != string(PipelineBindingBypassed) {
		t.Fatalf("summary = %#v", summary)
	}
	if err := mgr.DeletePlugin(ctx, pipelineTestID); err != nil {
		t.Fatalf("fail_open bindings do not block uninstall: %v", err)
	}
	if _, ok := mgr.Catalog().Current().PipelineSummaryForAPIKey(key.ID); ok {
		t.Fatal("uninstall removes fail_open bindings")
	}
}
