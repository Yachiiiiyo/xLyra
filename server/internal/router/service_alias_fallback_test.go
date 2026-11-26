package router

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xlyra/server/internal/store"
)

func routerServiceWithAliasRows(
	t *testing.T,
	models []store.CanonicalModel,
	aliases []store.CanonicalModelAlias,
	siteModels []store.SiteModel,
	sites []store.Site,
	credentials []store.SiteCredential,
) *Service {
	t.Helper()

	db := routerOfflinePostgresGorm(t)
	if err := db.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]store.CanonicalModel:
			*dest = append([]store.CanonicalModel(nil), models...)
			tx.Statement.RowsAffected = int64(len(*dest))
		case *store.CanonicalModel:
			id, ok := routerExtractUUIDClause(tx, "id")
			if !ok {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			for _, m := range models {
				if m.ID == id {
					*dest = m
					tx.Statement.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *store.CanonicalModelAlias:
			key, ok := routerExtractStringClause(tx, "normalized_alias")
			if !ok {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			for _, a := range aliases {
				if a.NormalizedAlias == key {
					*dest = a
					tx.Statement.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *[]store.SiteModel:
			if cid, ok := routerExtractUUIDClause(tx, "canonical_model_id"); ok {
				filtered := make([]store.SiteModel, 0)
				for _, sm := range siteModels {
					if sm.CanonicalID.Valid && sm.CanonicalID.UUID == cid {
						filtered = append(filtered, sm)
					}
				}
				*dest = filtered
			} else {
				*dest = append([]store.SiteModel(nil), siteModels...)
			}
			tx.Statement.RowsAffected = int64(len(*dest))
		case *[]store.Site:
			*dest = append([]store.Site(nil), sites...)
			tx.Statement.RowsAffected = int64(len(*dest))
		case *[]store.SiteCredential:
			*dest = append([]store.SiteCredential(nil), credentials...)
			tx.Statement.RowsAffected = int64(len(*dest))
		case *[]store.SiteHealthState:
			*dest = nil
		case *[]store.HealthSnapshot:
			*dest = nil
		case *[]store.SiteAPIKeyModel:
			*dest = nil
		case *[]store.SiteAPIKeyState:
			*dest = nil
		case *[]store.SiteModelPricing:
			*dest = nil
		case *[]store.SiteModelEndpointOverride:
			*dest = nil
		case *[]store.RouteCooldown:
			*dest = nil
		}
	}); err != nil {
		t.Fatalf("replace query callback: %v", err)
	}
	return NewService(routerStoreBackedByGorm(t, db))
}

func routerExtractUUIDClause(tx *gorm.DB, column string) (uuid.UUID, bool) {
	where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
	if !ok {
		return uuid.Nil, false
	}
	for _, expr := range where.Exprs {
		if eq, ok := expr.(clause.Eq); ok {
			if col, ok := eq.Column.(clause.Column); ok && col.Name == column {
				if v, ok := eq.Value.(uuid.UUID); ok {
					return v, true
				}
			}
		}
	}
	return uuid.Nil, false
}

func routerExtractStringClause(tx *gorm.DB, column string) (string, bool) {
	where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
	if !ok {
		return "", false
	}
	for _, expr := range where.Exprs {
		if eq, ok := expr.(clause.Eq); ok {
			if col, ok := eq.Column.(clause.Column); ok && col.Name == column {
				if v, ok := eq.Value.(string); ok {
					return v, true
				}
			}
		}
	}
	return "", false
}

func TestCandidatesFallbackToAliasWhenExactKeyEmptyOrArchivedOrUnroutable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	flashID := uuid.New()
	v41ID := uuid.New()
	v4ID := uuid.New()
	activeSiteID := uuid.New()
	noCredSiteID := uuid.New()

	models := []store.CanonicalModel{
		{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
		{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
		{ID: v4ID, ModelKey: "deepseek-v4-flash", Status: "archived"},
	}
	aliases := []store.CanonicalModelAlias{
		{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "catalog"},
		{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4-flash", NormalizedAlias: "deepseek-v4-flash", Source: "catalog"},
	}
	sites := []store.Site{
		{ID: activeSiteID, Name: "Active", Slug: "active", SiteType: "openai", BaseURL: "https://active.example", Status: "active", Enabled: true, RoutingPriority: 1},
		{ID: noCredSiteID, Name: "NoCred", Slug: "nocred", SiteType: "openai", BaseURL: "https://nocred.example", Status: "active", Enabled: true, RoutingPriority: 1},
	}
	siteModels := []store.SiteModel{
		{
			ID:           uuid.New(),
			SiteID:       activeSiteID,
			CanonicalID:  uuid.NullUUID{UUID: flashID, Valid: true},
			UpstreamName: "deepseek-flash",
			Status:       "active",
			Capabilities: store.JSON(`{"supported_endpoint_types":["openai"]}`),
		},
		// v41ID has an active site model on an active site, but no credentials -> 0 candidates, must fall back to alias!
		{
			ID:           uuid.New(),
			SiteID:       noCredSiteID,
			CanonicalID:  uuid.NullUUID{UUID: v41ID, Valid: true},
			UpstreamName: "deepseek-v4.1-flash",
			Status:       "active",
			Capabilities: store.JSON(`{"supported_endpoint_types":["openai"]}`),
		},
	}
	credentials := []store.SiteCredential{
		{ID: uuid.New(), SiteID: activeSiteID, CredentialType: "api_key"},
	}

	service := routerServiceWithAliasRows(t, models, aliases, siteModels, sites, credentials)

	for _, name := range []string{"deepseek-flash", "deepseek-v4.1-flash", "deepseek-v4-flash"} {
		list, err := service.Candidates(ctx, CandidateQuery{ModelKey: name, EndpointType: "openai"})
		if err != nil {
			t.Fatalf("Candidates(%q) returned error: %v", name, err)
		}
		if list.CanonicalModel.ID != flashID || len(list.Items) != 1 || list.Items[0].Site.ID != activeSiteID {
			t.Fatalf("Candidates(%q) = (canonical=%s, items=%d), want deepseek-flash with 1 candidate", name, list.CanonicalModel.ModelKey, len(list.Items))
		}
	}
}

func TestCandidatesPreferManualAliasOverExactKeyWithFallback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	flashID := uuid.New()
	v41ID := uuid.New()
	flashSiteID := uuid.New()
	v41SiteID := uuid.New()

	models := []store.CanonicalModel{
		{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
		{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
	}
	aliases := []store.CanonicalModelAlias{
		{ID: uuid.New(), CanonicalModelID: v41ID, Alias: "deepseek-flash", NormalizedAlias: "deepseek-flash", Source: "manual"},
	}
	sites := []store.Site{
		{ID: flashSiteID, Name: "FlashSite", Slug: "flash", SiteType: "openai", BaseURL: "https://flash.example", Status: "active", Enabled: true},
		{ID: v41SiteID, Name: "V41Site", Slug: "v41", SiteType: "openai", BaseURL: "https://v41.example", Status: "active", Enabled: true},
	}
	siteModels := []store.SiteModel{
		{
			ID:           uuid.New(),
			SiteID:       flashSiteID,
			CanonicalID:  uuid.NullUUID{UUID: flashID, Valid: true},
			UpstreamName: "deepseek-flash",
			Status:       "active",
			Capabilities: store.JSON(`{"supported_endpoint_types":["openai"]}`),
		},
		{
			ID:           uuid.New(),
			SiteID:       v41SiteID,
			CanonicalID:  uuid.NullUUID{UUID: v41ID, Valid: true},
			UpstreamName: "deepseek-v4.1-flash",
			Status:       "active",
			Capabilities: store.JSON(`{"supported_endpoint_types":["openai"]}`),
		},
	}
	credentials := []store.SiteCredential{
		{ID: uuid.New(), SiteID: flashSiteID, CredentialType: "api_key"},
		{ID: uuid.New(), SiteID: v41SiteID, CredentialType: "api_key"},
	}

	service := routerServiceWithAliasRows(t, models, aliases, siteModels, sites, credentials)
	list, err := service.Candidates(ctx, CandidateQuery{ModelKey: "deepseek-flash", EndpointType: "openai"})
	if err != nil {
		t.Fatalf("Candidates(deepseek-flash) returned error: %v", err)
	}
	if list.CanonicalModel.ID != v41ID || len(list.Items) != 1 || list.Items[0].Site.ID != v41SiteID {
		t.Fatalf("Candidates(deepseek-flash) = (canonical=%s, site=%v), want manual alias target deepseek-v4.1-flash", list.CanonicalModel.ModelKey, list.Items)
	}
}
