package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	sitepkg "xlyra/server/internal/site"
	"xlyra/server/internal/store"
	"xlyra/server/internal/usage"
)

func TestListSitesLiteSkipsUsageAndAccountCounts(t *testing.T) {
	t.Parallel()

	siteID := uuid.New()
	usageQueries := 0
	accountQueries := 0
	db := adminGormWithCallbacks(t, adminGormCallbacks{query: func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]store.Site:
			*dest = []store.Site{{ID: siteID, Name: "Grok", SiteType: "grok", Enabled: true}}
			tx.RowsAffected = 1
		case *[]store.SiteState:
			*dest = []store.SiteState{{SiteID: siteID, ModelCount: 2, APIKeyCount: 1}}
			tx.RowsAffected = 1
		case *[]store.RequestUsageDailySummary:
			usageQueries++
			*dest = []store.RequestUsageDailySummary{{SiteID: uuid.NullUUID{UUID: siteID, Valid: true}, RequestCount: 3}}
			tx.RowsAffected = 1
		case *[]store.SiteCredential, *[]store.SiteAPIKeyState:
			accountQueries++
		default:
			tx.AddError(gorm.ErrInvalidData)
		}
	}})
	st := adminStoreWithGorm(db)
	handler := Handler{sites: sitepkg.NewService(st, adminTestMasterKey), usage: usage.NewService(st)}

	liteBody := adminDecodeJSON[struct {
		Items []map[string]any `json:"items"`
	}](t, adminPerform(handler.ListSites, adminTestRequest(http.MethodGet, "/api/v1/sites?lite=1", "")))
	if usageQueries != 0 || accountQueries != 0 {
		t.Fatalf("lite queries: usage = %d, account = %d, want zero", usageQueries, accountQueries)
	}
	if len(liteBody.Items) != 1 || liteBody.Items[0]["id"] != siteID.String() || liteBody.Items[0]["model_count"] != float64(2) {
		t.Fatalf("lite site response = %#v", liteBody.Items)
	}
	if _, ok := liteBody.Items[0]["sync_state"]; !ok {
		t.Fatalf("lite site state missing: %#v", liteBody.Items[0])
	}

	regularBody := adminDecodeJSON[struct {
		Items []map[string]any `json:"items"`
	}](t, adminPerform(handler.ListSites, adminTestRequest(http.MethodGet, "/api/v1/sites", "")))
	if usageQueries != 1 || accountQueries != 2 {
		t.Fatalf("regular queries: usage = %d, account = %d, want 1 and 2", usageQueries, accountQueries)
	}
	if _, ok := regularBody.Items[0]["usage"]; !ok {
		t.Fatalf("regular site usage missing: %#v", regularBody.Items[0])
	}
	if _, ok := regularBody.Items[0]["account_count"]; !ok {
		t.Fatalf("regular account counts missing: %#v", regularBody.Items[0])
	}
}

func TestListAllSiteModelsGroupsAndFilters(t *testing.T) {
	t.Parallel()

	firstSiteID := uuid.New()
	secondSiteID := uuid.New()
	firstModels := []store.SiteModel{
		{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-a", Status: "active"},
		{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-b", Status: "disabled"},
	}
	secondModels := []store.SiteModel{{ID: uuid.New(), SiteID: secondSiteID, UpstreamName: "claude-a", Status: "active"}}
	handler := Handler{sites: siteServiceForAdminModels(t, []store.SiteModel{
		secondModels[0],
		{ID: uuid.New(), SiteID: firstSiteID, UpstreamName: "gpt-hidden", Status: "unavailable"},
		firstModels[1],
		firstModels[0],
	})}

	rec := adminPerform(handler.ListAllSiteModels, adminTestRequest(http.MethodGet, "/api/v1/site-models", ""))
	adminAssertStatus(t, rec, http.StatusOK)
	body := adminDecodeJSON[struct {
		Items []struct {
			SiteID string           `json:"site_id"`
			Models []map[string]any `json:"models"`
		} `json:"items"`
		Meta map[string]any `json:"meta"`
	}](t, rec)
	if len(body.Items) != 2 || int(body.Meta["count"].(float64)) != 2 {
		t.Fatalf("site model groups = %#v", body)
	}
	// 响应按站点 ID 字符串升序,测试用的随机 UUID 顺序不固定,期望值同样按此排序
	expected := []struct {
		siteID uuid.UUID
		models []store.SiteModel
	}{{firstSiteID, firstModels}, {secondSiteID, secondModels}}
	if expected[0].siteID.String() > expected[1].siteID.String() {
		expected[0], expected[1] = expected[1], expected[0]
	}
	for i, want := range expected {
		if body.Items[i].SiteID != want.siteID.String() {
			t.Fatalf("site %d id = %q, want %q", i, body.Items[i].SiteID, want.siteID)
		}
		gotModels, _ := json.Marshal(body.Items[i].Models)
		wantModels, _ := json.Marshal(siteModelPayloads(want.models))
		if string(gotModels) != string(wantModels) {
			t.Fatalf("site %d models = %s, want %s", i, gotModels, wantModels)
		}
	}
}

func TestListAllSiteModelsEmptyAndEnabledOnly(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		handler := Handler{sites: siteServiceForAdminModels(t, nil)}
		rec := adminPerform(handler.ListAllSiteModels, adminTestRequest(http.MethodGet, "/api/v1/site-models", ""))
		adminAssertStatus(t, rec, http.StatusOK)
		body := adminDecodeJSON[struct {
			Items []map[string]any `json:"items"`
			Meta  map[string]any   `json:"meta"`
		}](t, rec)
		if body.Items == nil || len(body.Items) != 0 || int(body.Meta["count"].(float64)) != 0 {
			t.Fatalf("empty site model response = %#v", body)
		}
	})

	t.Run("enabled only", func(t *testing.T) {
		enabledID := uuid.New()
		disabledID := uuid.New()
		handler := Handler{sites: siteServiceForAdminModels(t, []store.SiteModel{
			{ID: uuid.New(), SiteID: enabledID, UpstreamName: "gpt-enabled", Status: "active"},
			{ID: uuid.New(), SiteID: disabledID, UpstreamName: "gpt-disabled", Status: "active"},
		})}
		rec := adminPerform(handler.ListAllSiteModels, adminTestRequest(http.MethodGet, "/api/v1/site-models?enabled_only=true", ""))
		adminAssertStatus(t, rec, http.StatusOK)
		body := adminDecodeJSON[struct {
			Items []struct {
				SiteID string `json:"site_id"`
			} `json:"items"`
		}](t, rec)
		if len(body.Items) != 1 || body.Items[0].SiteID != enabledID.String() {
			t.Fatalf("enabled-only site model response = %#v", body)
		}
	})
}

func siteServiceForAdminModels(t *testing.T, models []store.SiteModel) *sitepkg.Service {
	t.Helper()

	siteID := uuid.New()
	if len(models) > 0 {
		siteID = models[0].SiteID
	}
	st := adminStoreWithCallbacks(t, adminGormCallbacks{query: func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]store.SiteModel:
			*dest = append([]store.SiteModel(nil), models...)
			tx.RowsAffected = int64(len(models))
		case *[]store.Site:
			*dest = []store.Site{{ID: siteID, Enabled: true}}
			for _, model := range models {
				if model.SiteID != siteID {
					*dest = append(*dest, store.Site{ID: model.SiteID, Enabled: false})
				}
			}
			tx.RowsAffected = int64(len(*dest))
		default:
			tx.AddError(gorm.ErrInvalidData)
		}
	}})
	return sitepkg.NewService(st, adminTestMasterKey)
}
