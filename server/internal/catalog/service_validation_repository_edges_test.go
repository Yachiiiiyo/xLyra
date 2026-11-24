package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

func TestServiceValidationRejectsInvalidModelKeyBeforeRepository(t *testing.T) {
	t.Parallel()

	service := NewService(nil)
	ctx := context.Background()

	_, err := service.Create(ctx, UpsertCanonicalModelInput{ModelKey: " !!! "})
	assertCatalogErrorContains(t, "Create", err, "model_key is required")

	_, err = service.Update(ctx, uuid.New(), UpsertCanonicalModelInput{ModelKey: " \t "})
	assertCatalogErrorContains(t, "Update", err, "model_key is required")
}

func TestArchiveAllowsEmptyAutoCreatedCanonicalAndRejectsLinkedModels(t *testing.T) {
	t.Parallel()

	canonicalID := uuid.New()
	siteID := uuid.New()
	var archivedModel store.CanonicalModel

	db := catalogPostgresGorm(t)
	replaceCatalogQueryCallback(t, db, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *store.CanonicalModel:
			*dest = store.CanonicalModel{
				ID:           canonicalID,
				ModelKey:     "deepseek-v4.1-flash",
				DisplayName:  "DeepSeek V4.1 Flash",
				Provider:     "deepseek",
				Category:     "chat",
				Capabilities: store.JSON(`{"auto_created":true,"source":"catalog_match"}`),
				Status:       "active",
			}
			tx.Statement.RowsAffected = 1
		case *[]store.SiteModel:
			*dest = nil
			tx.Statement.RowsAffected = 0
		case *[]store.Site:
			*dest = []store.Site{{ID: siteID, Status: "active", Enabled: true}}
			tx.Statement.RowsAffected = 1
		default:
			tx.AddError(errors.New("unexpected repository query during empty auto-created archive"))
		}
	})
	replaceCatalogUpdateCallback(t, db, func(tx *gorm.DB) {
		if model, ok := tx.Statement.Dest.(*store.CanonicalModel); ok {
			archivedModel = *model
		}
		tx.Statement.RowsAffected = 1
	})

	archived, err := (&Service{db: catalogStoreWithGorm(t, db)}).Archive(context.Background(), canonicalID)
	if err != nil {
		t.Fatalf("Archive empty auto-created canonical returned error: %v", err)
	}
	if archived.Status != "archived" || archivedModel.Status != "archived" {
		t.Fatalf("archived=%#v saved=%#v, want status=archived", archived, archivedModel)
	}

	linkedDB := catalogPostgresGorm(t)
	replaceCatalogQueryCallback(t, linkedDB, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *store.CanonicalModel:
			*dest = store.CanonicalModel{
				ID:           canonicalID,
				ModelKey:     "deepseek-v4.1-flash",
				Capabilities: store.JSON(`{"auto_created":true,"source":"catalog_match"}`),
				Status:       "active",
			}
			tx.Statement.RowsAffected = 1
		case *[]store.SiteModel:
			*dest = []store.SiteModel{{
				ID:          uuid.New(),
				SiteID:      siteID,
				CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true},
				Status:      "active",
			}}
			tx.Statement.RowsAffected = 1
		case *[]store.Site:
			*dest = []store.Site{{ID: siteID, Status: "active", Enabled: true}}
			tx.Statement.RowsAffected = 1
		default:
			tx.AddError(errors.New("unexpected repository query during linked archive guard"))
		}
	})

	_, err = (&Service{db: catalogStoreWithGorm(t, linkedDB)}).Archive(context.Background(), canonicalID)
	assertCatalogErrorContains(t, "Archive", err, "canonical model still has linked site models")
}

func TestBindSiteModelPropagatesUnbindUpdateError(t *testing.T) {
	t.Parallel()

	siteModelID := uuid.New()
	updateErr := errors.New("site model unbind update stopped")
	db := catalogTransactionPostgresGorm(t)
	replaceCatalogQueryCallback(t, db, func(tx *gorm.DB) {
		if dest, ok := tx.Statement.Dest.(*store.SiteModel); ok {
			*dest = store.SiteModel{
				ID:           siteModelID,
				SiteID:       uuid.New(),
				UpstreamName: "openai/gpt-4o",
				Status:       "available",
			}
			return
		}
		tx.AddError(errors.New("unexpected repository query during unbind"))
	})
	replaceCatalogUpdateCallback(t, db, func(tx *gorm.DB) {
		tx.AddError(updateErr)
	})

	_, err := (&Service{db: catalogStoreWithGorm(t, db)}).BindSiteModel(context.Background(), siteModelID, nil)
	assertCatalogErrorIs(t, "BindSiteModel", err, updateErr)
}
