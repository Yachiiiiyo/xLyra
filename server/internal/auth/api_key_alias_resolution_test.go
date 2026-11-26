package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xlyra/server/internal/store"
)

func TestResolveAPIKeyRouteAccessResolvesAliasOverEmptyOrArchivedCanonicalModel(t *testing.T) {
	t.Parallel()

	apiKeyID := uuid.New()
	siteID := uuid.New()
	flashID := uuid.New()
	v41ID := uuid.New()
	v4ID := uuid.New()
	flashSiteModelID := uuid.New()

	models := []store.CanonicalModel{
		{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
		{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
		{ID: v4ID, ModelKey: "deepseek-v4-flash", Status: "archived"},
	}
	aliases := []store.CanonicalModelAlias{
		{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "manual"},
		{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4-flash", NormalizedAlias: "deepseek-v4-flash", Source: "catalog"},
	}
	sites := []store.Site{{ID: siteID, Status: "active", Enabled: true}}
	siteModels := []store.SiteModel{
		{ID: flashSiteModelID, SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: flashID, Valid: true}, Status: "active"},
	}

	service := authServiceWithGormCallbacks(t, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *store.APIKey:
			*dest = store.APIKey{ID: apiKeyID, Name: "alias-key", Status: "active", ModelPolicy: "allow_list", SitePolicy: "allow_all"}
			tx.Statement.RowsAffected = 1
		case *[]store.CanonicalModel:
			*dest = append([]store.CanonicalModel(nil), models...)
			tx.Statement.RowsAffected = int64(len(*dest))
		case *store.CanonicalModel:
			id, ok := authExtractUUIDClause(tx, "id")
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
			key, ok := authExtractStringClause(tx, "normalized_alias")
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
			if cid, ok := authExtractUUIDClause(tx, "canonical_model_id"); ok {
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
		case *[]store.APIKeySiteModelPermission:
			*dest = []store.APIKeySiteModelPermission{{ID: uuid.New(), APIKeyID: apiKeyID, SiteModelID: flashSiteModelID, Enabled: true}}
			tx.Statement.RowsAffected = 1
		default:
			tx.AddError(gorm.ErrRecordNotFound)
		}
	}, nil, nil)

	for _, requested := range []string{"deepseek-flash", "deepseek-v4.1-flash", "deepseek-v4-flash"} {
		access, err := service.ResolveAPIKeyRouteAccess(context.Background(), apiKeyID, requested)
		if err != nil {
			t.Fatalf("ResolveAPIKeyRouteAccess(%q) returned error: %v", requested, err)
		}
		if !access.Allowed || access.CanonicalModel.ID != flashID || access.ModelKey != "deepseek-flash" || len(access.AllowedSiteModelIDs) != 1 || access.AllowedSiteModelIDs[0] != flashSiteModelID {
			t.Fatalf("ResolveAPIKeyRouteAccess(%q) = %#v, want allowed deepseek-flash access", requested, access)
		}
	}
}

func authExtractUUIDClause(tx *gorm.DB, column string) (uuid.UUID, bool) {
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

func authExtractStringClause(tx *gorm.DB, column string) (string, bool) {
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
