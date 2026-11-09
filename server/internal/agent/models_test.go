package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
)

// fakeModelCatalog 按预设返回目录数据,不接触数据库。
type fakeModelCatalog struct {
	sites    []store.Site
	models   map[uuid.UUID][]store.SiteModel
	canon    []store.CanonicalModel
	scope    ModelScope
	def      string
	err      error
	scopeErr error
	defErr   error
}

func (f fakeModelCatalog) Sites(context.Context) ([]store.Site, error) {
	return f.sites, f.err
}

func (f fakeModelCatalog) SiteModels(context.Context) (map[uuid.UUID][]store.SiteModel, error) {
	return f.models, f.err
}

func (f fakeModelCatalog) CanonicalModels(context.Context) ([]store.CanonicalModel, error) {
	return f.canon, f.err
}

func (f fakeModelCatalog) Scope(context.Context) (ModelScope, error) {
	return f.scope, f.scopeErr
}

func (f fakeModelCatalog) DefaultModel(context.Context) (string, error) {
	return f.def, f.defErr
}

func TestBuildAgentModelsFiltersSortsAndDedupes(t *testing.T) {
	enabled := uuid.New()
	disabled := uuid.New()
	other := uuid.New()
	chatID := uuid.New()
	imageID := uuid.New()
	audioID := uuid.New()
	embedID := uuid.New()
	plainID := uuid.New()

	sites := []store.Site{
		{ID: enabled, Name: "启用", Enabled: true},
		{ID: disabled, Name: "停用", Enabled: false},
		{ID: other, Name: "另一启用", Enabled: true},
	}
	models := map[uuid.UUID][]store.SiteModel{
		enabled: {
			siteModel(uuid.New(), enabled, chatID, "gpt-x-upstream", "GPT X", "active"),
			siteModel(uuid.New(), enabled, imageID, "img-1", "绘图", "active"),
			siteModel(uuid.New(), enabled, audioID, "aud-1", "语音", "active"),
			siteModel(uuid.New(), enabled, embedID, "emb-1", "向量", "active"),
			siteModel(uuid.New(), enabled, plainID, "zeta-raw", "", "disabled"),
			siteModel(uuid.New(), enabled, uuid.Nil, "  ", "空白", "active"),
		},
		disabled: {
			siteModel(uuid.New(), disabled, chatID, "gpt-x-disabled-site", "不该出现", "active"),
		},
		other: {
			// 同 id 后出现的一项被去重丢掉,展示名保留先出现的。
			siteModel(uuid.New(), other, chatID, "gpt-x-other", "GPT X 重复", "active"),
			siteModel(uuid.New(), other, uuid.Nil, "Alpha-Raw", "", "active"),
			siteModel(uuid.New(), other, plainID, "zeta-raw", "Zeta", "active"),
		},
	}
	canonical := []store.CanonicalModel{
		{ID: chatID, ModelKey: "claude-opus-4-6", DisplayName: "规范名", Category: "chat"},
		{ID: imageID, ModelKey: "img", Category: "image"},
		{ID: audioID, ModelKey: "aud", Category: "audio"},
		{ID: embedID, ModelKey: "emb", Category: "embedding"},
		{ID: plainID, ModelKey: "", Category: ""},
	}

	got := buildAgentModels(sites, models, canonical, ModelScope{SitePolicy: "allow_all", ModelPolicy: "allow_all"})

	wantIDs := []string{"Alpha-Raw", "claude-opus-4-6", "zeta-raw"}
	if len(got) != len(wantIDs) {
		t.Fatalf("models = %+v, want ids %v", got, wantIDs)
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("models[%d].id = %q, want %q (full %+v)", i, got[i].ID, id, got)
		}
	}
	if got[0].DisplayName != "Alpha-Raw" || got[0].Category != "chat" || got[0].Reasoning != nil {
		t.Fatalf("unmapped model = %+v", got[0])
	}
	if got[1].DisplayName != "GPT X" || got[1].Category != "chat" {
		t.Fatalf("canonical model = %+v", got[1])
	}
	if got[1].Reasoning == nil || strings.Join(got[1].Reasoning.Levels, ",") != "low,medium,high,max" || got[1].Reasoning.Default != "high" {
		t.Fatalf("claude-opus-4-6 reasoning = %+v, want low/medium/high/max default high", got[1].Reasoning)
	}
	if got[2].DisplayName != "Zeta" || got[2].Category != "chat" || got[2].Reasoning != nil {
		t.Fatalf("keyless model = %+v, want upstream id and no reasoning", got[2])
	}
}

func TestBuildAgentModelsAllowListFiltersSitesAndModels(t *testing.T) {
	allowedSite := uuid.New()
	blockedSite := uuid.New()
	kept := uuid.New()
	dropped := uuid.New()
	chatID := uuid.New()

	sites := []store.Site{{ID: allowedSite, Enabled: true}, {ID: blockedSite, Enabled: true}}
	models := map[uuid.UUID][]store.SiteModel{
		allowedSite: {
			siteModel(kept, allowedSite, chatID, "kept-upstream", "保留", "active"),
			siteModel(dropped, allowedSite, chatID, "dropped-upstream", "过滤", "active"),
		},
		blockedSite: {
			siteModel(uuid.New(), blockedSite, uuid.Nil, "blocked-site", "站点外", "active"),
		},
	}
	canonical := []store.CanonicalModel{{ID: chatID, ModelKey: "", Category: "chat"}}

	scope := ModelScope{
		SitePolicy:          "allow_list",
		ModelPolicy:         "allow_list",
		AllowedSiteIDs:      []string{allowedSite.String()},
		AllowedSiteModelIDs: []string{kept.String()},
	}
	got := buildAgentModels(sites, models, canonical, scope)
	if len(got) != 1 || got[0].ID != "kept-upstream" {
		t.Fatalf("allow_list models = %+v, want only kept-upstream", got)
	}

	// 非 allow_list 的策略值按 allow_all 处理,空名单不再额外过滤。
	scope.SitePolicy, scope.ModelPolicy = "allow_all", "deny_list"
	got = buildAgentModels(sites, models, canonical, scope)
	if len(got) != 3 {
		t.Fatalf("non allow_list models = %+v, want all three active models", got)
	}
}

func TestModelsHandlerResponse(t *testing.T) {
	siteID := uuid.New()
	withReason := uuid.New()
	withoutReason := uuid.New()
	catalog := fakeModelCatalog{
		sites: []store.Site{{ID: siteID, Enabled: true}},
		models: map[uuid.UUID][]store.SiteModel{
			siteID: {
				siteModel(uuid.New(), siteID, withReason, "up-a", "有推理", "active"),
				siteModel(uuid.New(), siteID, withoutReason, "plain-model", "无推理", "active"),
			},
		},
		canon: []store.CanonicalModel{
			{ID: withReason, ModelKey: "gpt-6", Category: "chat"},
			{ID: withoutReason, ModelKey: "some-unheard-of-model", Category: "chat"},
		},
		scope: ModelScope{SitePolicy: "allow_all", ModelPolicy: "allow_all"},
		def:   "gpt-6",
	}
	handler, _, _ := newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.Models = catalog })

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Models []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				Category    string `json:"category"`
				Reasoning   *struct {
					Levels            []string `json:"levels"`
					Default           string   `json:"default"`
					ThinkingMandatory bool     `json:"thinking_mandatory"`
				} `json:"reasoning"`
			} `json:"models"`
			DefaultModel string `json:"default_model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.DefaultModel != "gpt-6" || len(body.Data.Models) != 2 {
		t.Fatalf("body = %+v", body.Data)
	}
	reasoned, plain := body.Data.Models[0], body.Data.Models[1]
	if reasoned.ID != "gpt-6" || reasoned.Reasoning == nil || !reasoned.Reasoning.ThinkingMandatory || reasoned.Reasoning.Default != "medium" {
		t.Fatalf("reasoned model = %+v", reasoned)
	}
	if plain.ID != "some-unheard-of-model" || plain.DisplayName != "无推理" || plain.Reasoning != nil {
		t.Fatalf("plain model = %+v", plain)
	}
	if !json.Valid(rec.Body.Bytes()) || jsonContainsKey(t, rec.Body.Bytes(), plain.ID, "reasoning") {
		t.Fatalf("plain model should omit reasoning: %s", rec.Body.String())
	}
}

func TestModelsHandlerOmitsDefaultOutsideListAndReturnsEmpty(t *testing.T) {
	catalog := fakeModelCatalog{
		scope: ModelScope{SitePolicy: "allow_all", ModelPolicy: "allow_all"},
		def:   "missing-model",
	}
	handler, _, _ := newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.Models = catalog })

	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Models       []agentModel `json:"models"`
			DefaultModel string       `json:"default_model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Models == nil || len(body.Data.Models) != 0 || body.Data.DefaultModel != "" {
		t.Fatalf("body = %+v, raw %s", body.Data, rec.Body.String())
	}
	if jsonContainsKey(t, rec.Body.Bytes(), "", "default_model") {
		t.Fatalf("default_model should be omitted: %s", rec.Body.String())
	}
}

func TestModelsHandlerRequiresCatalog(t *testing.T) {
	handler, _, _ := newTestHandler(t, &fakeRunner{})
	rec := doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/models", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	failing := fakeModelCatalog{err: errors.New("db down")}
	handler, _, _ = newTestHandler(t, &fakeRunner{}, func(d *Deps) { d.Models = failing })
	rec = doJSONAgent(t, handler, http.MethodGet, "/api/v1/agent/models", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("error status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func siteModel(id, siteID uuid.UUID, canonicalID uuid.UUID, upstream, display, status string) store.SiteModel {
	canonical := uuid.NullUUID{}
	if canonicalID != uuid.Nil {
		canonical = uuid.NullUUID{UUID: canonicalID, Valid: true}
	}
	return store.SiteModel{
		ID:           id,
		SiteID:       siteID,
		CanonicalID:  canonical,
		UpstreamName: upstream,
		DisplayName:  display,
		Status:       status,
	}
}

// jsonContainsKey 判断 models 数组里指定 id 的对象是否带某个字段;
// id 为空时检查 data 对象本身。
func jsonContainsKey(t *testing.T, raw []byte, id, key string) bool {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	data, _ := payload["data"].(map[string]any)
	if id == "" {
		_, ok := data[key]
		return ok
	}
	items, _ := data["models"].([]any)
	for _, item := range items {
		object, _ := item.(map[string]any)
		if object["id"] == id {
			_, ok := object[key]
			return ok
		}
	}
	t.Fatalf("model %q not in %s", id, raw)
	return false
}
