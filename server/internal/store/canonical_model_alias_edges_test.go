package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestCanonicalModelRepositoryCreateAliasCatalogPreservesManualAliasOffline(t *testing.T) {
	t.Parallel()

	manualCanonicalID := uuid.New()
	catalogCanonicalID := uuid.New()
	existingAliasID := uuid.New()
	db := storeRepositoryOfflineGorm(t)
	created := 0
	updated := 0
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		if dest, ok := tx.Statement.Dest.(*CanonicalModelAlias); ok {
			*dest = CanonicalModelAlias{
				ID:               existingAliasID,
				CanonicalModelID: manualCanonicalID,
				Alias:            "deepseek-v4.1-flash",
				NormalizedAlias:  "deepseek-v4.1-flash",
				Source:           "manual",
			}
			tx.Statement.RowsAffected = 1
			return
		}
		tx.AddError(errors.New("unexpected query destination"))
	})
	storeReplaceCreateCallback(t, db, func(tx *gorm.DB) {
		created++
		tx.Statement.RowsAffected = 1
	})
	storeReplaceUpdateCallback(t, db, func(tx *gorm.DB) {
		updated++
		tx.Statement.RowsAffected = 1
	})

	repo := NewCanonicalModelRepository(db)
	got, err := repo.CreateAlias(context.Background(), CreateCanonicalModelAliasParams{
		CanonicalModelID: catalogCanonicalID,
		Alias:            "deepseek-v4.1-flash",
		NormalizedAlias:  "deepseek-v4.1-flash",
		Source:           "catalog",
	})
	if err != nil {
		t.Fatalf("CreateAlias(catalog) returned error: %v", err)
	}
	if got.ID != existingAliasID || got.CanonicalModelID != manualCanonicalID || got.Source != "manual" {
		t.Fatalf("CreateAlias(catalog) = %#v, want preserved manual alias", got)
	}
	if created != 0 || updated != 0 {
		t.Fatalf("created=%d updated=%d, want no overwrite of manual alias", created, updated)
	}
}

func TestCanonicalModelRepositoryGetAliasWithModelRejectsNilCanonicalIDOffline(t *testing.T) {
	t.Parallel()

	db := storeRepositoryOfflineGorm(t)
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		if dest, ok := tx.Statement.Dest.(*CanonicalModelAlias); ok {
			*dest = CanonicalModelAlias{NormalizedAlias: "empty"}
			tx.Statement.RowsAffected = 0
			return
		}
		tx.AddError(errors.New("unexpected query after nil canonical id"))
	})

	_, _, err := NewCanonicalModelRepository(db).GetAliasWithModel(context.Background(), "empty")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetAliasWithModel error = %v, want gorm.ErrRecordNotFound", err)
	}
}

func TestCanonicalModelRepositoryRoutableSiteModelCountFiltersDisabledAndUnavailableOffline(t *testing.T) {
	t.Parallel()

	canonicalID := uuid.New()
	activeSiteID := uuid.New()
	disabledSiteID := uuid.New()
	inactiveSiteID := uuid.New()
	deletedSiteID := uuid.New()

	db := storeRepositoryOfflineGorm(t)
	storeReplaceQueryCallback(t, db, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]SiteModel:
			*dest = []SiteModel{
				{ID: uuid.New(), SiteID: activeSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: activeSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "disabled"},
				{ID: uuid.New(), SiteID: activeSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "unavailable"},
				{ID: uuid.New(), SiteID: disabledSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: inactiveSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: deletedSiteID, CanonicalID: uuid.NullUUID{UUID: canonicalID, Valid: true}, Status: "active"},
			}
			tx.Statement.RowsAffected = int64(len(*dest))
		case *[]Site:
			*dest = []Site{
				{ID: activeSiteID, Status: "active", Enabled: true},
				{ID: disabledSiteID, Status: "active", Enabled: false},
				{ID: inactiveSiteID, Status: "disabled", Enabled: true},
				{ID: deletedSiteID, Status: SiteStatusDeleted, Enabled: true},
			}
			tx.Statement.RowsAffected = int64(len(*dest))
		default:
			tx.AddError(errors.New("unexpected routable count query destination"))
		}
	})

	count, err := NewCanonicalModelRepository(db).RoutableSiteModelCount(context.Background(), canonicalID)
	if err != nil {
		t.Fatalf("RoutableSiteModelCount returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("RoutableSiteModelCount = %d, want 1", count)
	}
}
