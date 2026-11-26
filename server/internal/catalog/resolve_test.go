package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xlyra/server/internal/store"
)

type fakeCatalogState struct {
	models     []store.CanonicalModel
	aliases    []store.CanonicalModelAlias
	siteModels []store.SiteModel
	sites      []store.Site
	restored   []uuid.UUID
	archived   []uuid.UUID
	created    []store.CanonicalModelAlias
	deleted    int
}

func (s *fakeCatalogState) install(t *testing.T, db *gorm.DB) {
	t.Helper()
	replaceCatalogQueryCallback(t, db, func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *[]store.CanonicalModel:
			*dest = append([]store.CanonicalModel(nil), s.models...)
			tx.Statement.RowsAffected = int64(len(*dest))
		case *store.CanonicalModel:
			id, ok := extractUUIDClause(tx, "id")
			if !ok {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			for _, m := range s.models {
				if m.ID == id {
					*dest = m
					tx.Statement.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *store.CanonicalModelAlias:
			key, ok := extractStringClause(tx, "normalized_alias")
			if !ok {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			for _, a := range s.aliases {
				if a.NormalizedAlias == key {
					*dest = a
					tx.Statement.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *[]store.CanonicalModelAlias:
			if cid, ok := extractUUIDClause(tx, "canonical_model_id"); ok {
				filtered := make([]store.CanonicalModelAlias, 0)
				for _, a := range s.aliases {
					if a.CanonicalModelID == cid {
						filtered = append(filtered, a)
					}
				}
				*dest = filtered
			} else {
				*dest = append([]store.CanonicalModelAlias(nil), s.aliases...)
			}
			tx.Statement.RowsAffected = int64(len(*dest))
		case *[]store.SiteModel:
			if cid, ok := extractUUIDClause(tx, "canonical_model_id"); ok {
				filtered := make([]store.SiteModel, 0)
				for _, sm := range s.siteModels {
					if sm.CanonicalID.Valid && sm.CanonicalID.UUID == cid {
						filtered = append(filtered, sm)
					}
				}
				*dest = filtered
			} else {
				*dest = append([]store.SiteModel(nil), s.siteModels...)
			}
			tx.Statement.RowsAffected = int64(len(*dest))
		case *store.SiteModel:
			id, ok := extractUUIDClause(tx, "id")
			if !ok {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			for _, sm := range s.siteModels {
				if sm.ID == id {
					*dest = sm
					tx.Statement.RowsAffected = 1
					return
				}
			}
			tx.AddError(gorm.ErrRecordNotFound)
		case *[]store.Site:
			*dest = append([]store.Site(nil), s.sites...)
			tx.Statement.RowsAffected = int64(len(*dest))
		default:
			tx.AddError(errors.New("unexpected fake catalog query destination"))
		}
	})
	replaceCatalogCreateCallback(t, db, func(tx *gorm.DB) {
		switch item := tx.Statement.Dest.(type) {
		case *store.CanonicalModelAlias:
			if item.ID == uuid.Nil {
				item.ID = uuid.New()
			}
			s.aliases = append(s.aliases, *item)
			s.created = append(s.created, *item)
			tx.Statement.RowsAffected = 1
		case *store.CanonicalModel:
			if item.ID == uuid.Nil {
				item.ID = uuid.New()
			}
			s.models = append(s.models, *item)
			tx.Statement.RowsAffected = 1
		default:
			tx.Statement.RowsAffected = 1
		}
	})
	replaceCatalogUpdateCallback(t, db, func(tx *gorm.DB) {
		switch item := tx.Statement.Dest.(type) {
		case *store.CanonicalModel:
			for i := range s.models {
				if s.models[i].ID == item.ID {
					prev := s.models[i].Status
					s.models[i] = *item
					if prev == "archived" && item.Status == "active" {
						s.restored = append(s.restored, item.ID)
					}
					if item.Status == "archived" {
						s.archived = append(s.archived, item.ID)
					}
					break
				}
			}
			tx.Statement.RowsAffected = 1
		case *store.SiteModel:
			for i := range s.siteModels {
				if s.siteModels[i].ID == item.ID {
					s.siteModels[i] = *item
					break
				}
			}
			tx.Statement.RowsAffected = 1
		case *store.CanonicalModelAlias:
			for i := range s.aliases {
				if s.aliases[i].ID == item.ID {
					s.aliases[i] = *item
					break
				}
			}
			tx.Statement.RowsAffected = 1
		default:
			tx.Statement.RowsAffected = 1
		}
	})
	if err := db.Callback().Delete().Replace("gorm:delete", func(tx *gorm.DB) {
		s.deleted++
		if id, ok := extractUUIDClause(tx, "id"); ok {
			next := s.aliases[:0]
			for _, a := range s.aliases {
				if a.ID != id {
					next = append(next, a)
				}
			}
			s.aliases = next
		}
		tx.Statement.RowsAffected = 1
	}); err != nil {
		t.Fatalf("replace delete callback: %v", err)
	}
}

func extractStringClause(tx *gorm.DB, column string) (string, bool) {
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

func extractUUIDClause(tx *gorm.DB, column string) (uuid.UUID, bool) {
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

func TestResolveCanonicalModelFallsBackWhenKeyArchivedOrEmptyAndPrioritizesManualAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	siteID := uuid.New()
	flashID := uuid.New()
	v41ID := uuid.New()
	v4ID := uuid.New()

	t.Run("archived exact key does not shadow active alias", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "archived"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "catalog"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-v4.1-flash")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != flashID {
			t.Fatalf("resolved = %s (%s), want deepseek-flash (%s)", got.ModelKey, got.ID, flashID)
		}
	})

	t.Run("active empty shell falls back to active catalog alias", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v4ID, ModelKey: "deepseek-v4-flash", Status: "active"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4-flash", NormalizedAlias: "deepseek-v4-flash", Source: "catalog"},
			},
			sites: []store.Site{{ID: siteID, Status: "active", Enabled: true}},
			siteModels: []store.SiteModel{
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: flashID, Valid: true}, Status: "active"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-v4-flash")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != flashID {
			t.Fatalf("resolved = %s (%s), want deepseek-flash (%s)", got.ModelKey, got.ID, flashID)
		}
	})

	t.Run("manual alias overrides active exact key even when exact key has channels", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: v41ID, Alias: "deepseek-flash", NormalizedAlias: "deepseek-flash", Source: "manual"},
			},
			sites: []store.Site{{ID: siteID, Status: "active", Enabled: true}},
			siteModels: []store.SiteModel{
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: flashID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: v41ID, Valid: true}, Status: "active"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-flash")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != v41ID {
			t.Fatalf("resolved = %s (%s), want manual alias target deepseek-v4.1-flash (%s)", got.ModelKey, got.ID, v41ID)
		}
	})

	t.Run("catalog alias does not hijack active exact key that has channels", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "catalog"},
			},
			sites: []store.Site{{ID: siteID, Status: "active", Enabled: true}},
			siteModels: []store.SiteModel{
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: flashID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: v41ID, Valid: true}, Status: "active"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-v4.1-flash")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != v41ID {
			t.Fatalf("resolved = %s (%s), want exact key deepseek-v4.1-flash (%s)", got.ModelKey, got.ID, v41ID)
		}
	})

	t.Run("manual alias pointing to empty model falls back to exact key with channels", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "manual"},
			},
			sites: []store.Site{{ID: siteID, Status: "active", Enabled: true}},
			siteModels: []store.SiteModel{
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: v41ID, Valid: true}, Status: "active"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-v4.1-flash")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != v41ID {
			t.Fatalf("resolved = %s (%s), want fallback deepseek-v4.1-flash (%s)", got.ModelKey, got.ID, v41ID)
		}
	})

	t.Run("raw normalized alias matches before suffix trimming", func(t *testing.T) {
		t.Parallel()
		db := catalogPostgresGorm(t)
		state := &fakeCatalogState{
			models: []store.CanonicalModel{
				{ID: flashID, ModelKey: "deepseek-flash", Status: "active"},
				{ID: v41ID, ModelKey: "deepseek-v4.1-flash", Status: "active"},
			},
			aliases: []store.CanonicalModelAlias{
				{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash-free", NormalizedAlias: "deepseek-v4.1-flash-free", Source: "manual"},
			},
			sites: []store.Site{{ID: siteID, Status: "active", Enabled: true}},
			siteModels: []store.SiteModel{
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: flashID, Valid: true}, Status: "active"},
				{ID: uuid.New(), SiteID: siteID, CanonicalID: uuid.NullUUID{UUID: v41ID, Valid: true}, Status: "active"},
			},
		}
		state.install(t, db)

		got, err := ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db), "deepseek-v4.1-flash-free")
		if err != nil {
			t.Fatalf("ResolveCanonicalModel returned error: %v", err)
		}
		if got.ID != flashID {
			t.Fatalf("resolved = %s (%s), want raw alias target deepseek-flash (%s)", got.ModelKey, got.ID, flashID)
		}
	})
}

func TestMatchOrCreateCanonicalDoesNotResurrectArchivedWhenActiveAliasExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	flashID := uuid.New()
	v41ID := uuid.New()

	db := catalogPostgresGorm(t)
	state := &fakeCatalogState{
		models: []store.CanonicalModel{
			{ID: flashID, ModelKey: "deepseek-flash", DisplayName: "DeepSeek Flash", Status: "active"},
			{ID: v41ID, ModelKey: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash", Status: "archived"},
		},
		aliases: []store.CanonicalModelAlias{
			{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "catalog"},
		},
	}
	state.install(t, db)

	got, confidence, err := (&Service{}).matchOrCreateCanonical(ctx, store.NewCanonicalModelRepository(db), store.SiteModel{
		ID:           uuid.New(),
		UpstreamName: "deepseek-v4.1-flash",
	})
	if err != nil {
		t.Fatalf("matchOrCreateCanonical returned error: %v", err)
	}
	if got.ID != flashID || confidence != 95 {
		t.Fatalf("match = (%s, %d), want (deepseek-flash, 95)", got.ModelKey, confidence)
	}
	if len(state.restored) != 0 {
		t.Fatalf("archived model should not have been restored: %#v", state.restored)
	}
}

func TestMatchOrCreateCanonicalManualAliasBeatsActiveExactKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	flashID := uuid.New()
	v41ID := uuid.New()

	db := catalogPostgresGorm(t)
	state := &fakeCatalogState{
		models: []store.CanonicalModel{
			{ID: flashID, ModelKey: "deepseek-flash", DisplayName: "DeepSeek Flash", Status: "active"},
			{ID: v41ID, ModelKey: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash", Status: "active"},
		},
		aliases: []store.CanonicalModelAlias{
			{ID: uuid.New(), CanonicalModelID: flashID, Alias: "deepseek-v4.1-flash", NormalizedAlias: "deepseek-v4.1-flash", Source: "manual"},
		},
	}
	state.install(t, db)

	got, confidence, err := (&Service{}).matchOrCreateCanonical(ctx, store.NewCanonicalModelRepository(db), store.SiteModel{
		ID:           uuid.New(),
		UpstreamName: "deepseek-v4.1-flash",
	})
	if err != nil {
		t.Fatalf("matchOrCreateCanonical returned error: %v", err)
	}
	if got.ID != flashID || confidence != 95 {
		t.Fatalf("match = (%s, %d), want manual alias target (deepseek-flash, 95)", got.ModelKey, confidence)
	}
}

func TestMergeConsolidatesSiteModelsAliasesAndArchivesSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sourceID := uuid.New()
	targetID := uuid.New()
	siteModelID := uuid.New()
	redundantAliasID := uuid.New()
	carryAliasID := uuid.New()

	db := catalogTransactionPostgresGorm(t)
	state := &fakeCatalogState{
		models: []store.CanonicalModel{
			{ID: sourceID, ModelKey: "deepseek-v4.1-flash", DisplayName: "DeepSeek V4.1 Flash", Status: "active", Category: "chat"},
			{ID: targetID, ModelKey: "deepseek-flash", DisplayName: "DeepSeek Flash", Status: "active", Category: "chat"},
		},
		aliases: []store.CanonicalModelAlias{
			{ID: redundantAliasID, CanonicalModelID: sourceID, Alias: "deepseek-flash", NormalizedAlias: "deepseek-flash", Source: "manual"},
			{ID: carryAliasID, CanonicalModelID: sourceID, Alias: "ds-v41-flash", NormalizedAlias: "ds-v41-flash", Source: "manual"},
		},
		siteModels: []store.SiteModel{
			{ID: siteModelID, SiteID: uuid.New(), CanonicalID: uuid.NullUUID{UUID: sourceID, Valid: true}, UpstreamName: "deepseek-v4.1-flash", Status: "active"},
		},
	}
	state.install(t, db)

	service := &Service{db: catalogStoreWithGorm(t, db)}
	if _, err := service.Merge(ctx, uuid.Nil, targetID); err == nil {
		t.Fatal("expected error for nil source id")
	}
	if _, err := service.Merge(ctx, sourceID, sourceID); err == nil {
		t.Fatal("expected error for self merge")
	}

	res, err := service.Merge(ctx, sourceID, targetID)
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if res.MovedSiteModels != 1 || res.MovedAliases != 2 || res.Source.Status != "archived" || res.Target.ID != targetID {
		t.Fatalf("Merge result = %#v, want moved_site_models=1 moved_aliases=2 archived source", res)
	}
	if !state.siteModels[0].CanonicalID.Valid || state.siteModels[0].CanonicalID.UUID != targetID {
		t.Fatalf("site model was not rebound to target: %#v", state.siteModels[0])
	}
	if state.deleted != 1 {
		t.Fatalf("redundant alias delete count = %d, want 1", state.deleted)
	}
	foundSourceKeyAlias := false
	foundCarryAlias := false
	for _, a := range state.aliases {
		if a.CanonicalModelID == targetID && a.NormalizedAlias == "deepseek-v4.1-flash" && a.Source == "manual" {
			foundSourceKeyAlias = true
		}
		if a.CanonicalModelID == targetID && a.NormalizedAlias == "ds-v41-flash" {
			foundCarryAlias = true
		}
	}
	if !foundSourceKeyAlias || !foundCarryAlias {
		t.Fatalf("aliases after merge = %#v, want source key and carried alias bound to target", state.aliases)
	}
}
