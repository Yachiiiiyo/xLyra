package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/store"
)

// ErrCanonicalModelNotFound indicates that neither a canonical model key nor an
// alias matched the requested model name.
var ErrCanonicalModelNotFound = errors.New("canonical model not found")

type canonicalModelNotFoundError struct {
	modelKey string
}

func (e canonicalModelNotFoundError) Error() string {
	return fmt.Sprintf("canonical model %q was not found", e.modelKey)
}

func (e canonicalModelNotFoundError) Is(target error) bool {
	return target == ErrCanonicalModelNotFound
}

// ResolveCanonicalModelCandidates returns ordered canonical model candidates for
// modelKey (1 or 2 deduplicated entries).
//
// Precedence when both an exact model_key row and a distinct alias target exist:
//   - An archived canonical model never shadows an active alias target.
//   - An active manual alias (user-configured in Model Matching) takes priority
//     over an active exact model_key, with the exact model_key kept as fallback.
//   - A non-manual (e.g. catalog-synced) alias is kept as fallback behind an
//     active exact model_key so empty/unroutable models fall through to the alias.
func ResolveCanonicalModelCandidates(ctx context.Context, repo store.CanonicalModelRepository, modelKey string) ([]store.CanonicalModel, error) {
	normalized := CanonicalModelKeyFromUpstream(modelKey)
	rawNormalized := NormalizeModelKey(modelKey)
	if normalized == "" {
		normalized = rawNormalized
	}
	if normalized == "" {
		return nil, fmt.Errorf("model_key is required")
	}

	keyModel, keyErr := repo.GetByKey(ctx, normalized)
	keyFound := keyErr == nil
	if keyErr != nil && !errors.Is(keyErr, gorm.ErrRecordNotFound) {
		return nil, keyErr
	}

	aliasRow, aliasModel, aliasFound, aliasErr := lookupCanonicalAlias(ctx, repo, rawNormalized, normalized)
	if aliasErr != nil {
		return nil, aliasErr
	}
	if keyFound && aliasFound && aliasModel.ID == keyModel.ID {
		aliasFound = false
	}

	if !keyFound && !aliasFound {
		return nil, canonicalModelNotFoundError{modelKey: modelKey}
	}
	if !keyFound {
		return []store.CanonicalModel{aliasModel}, nil
	}
	if !aliasFound {
		return []store.CanonicalModel{keyModel}, nil
	}

	keyActive := keyModel.Status == "active"
	aliasActive := aliasModel.Status == "active"
	switch {
	case !keyActive && aliasActive:
		return []store.CanonicalModel{aliasModel}, nil
	case keyActive && !aliasActive:
		return []store.CanonicalModel{keyModel}, nil
	case keyActive && aliasActive:
		if isManualAliasSource(aliasRow.Source) {
			return []store.CanonicalModel{aliasModel, keyModel}, nil
		}
		return []store.CanonicalModel{keyModel, aliasModel}, nil
	default:
		return []store.CanonicalModel{keyModel}, nil
	}
}

// ResolveCanonicalModel resolves modelKey to the single preferred canonical
// model. When both an exact key model and a distinct alias target are active,
// it picks the first candidate that has routable site models so empty shells
// fall back cleanly to the alias target.
func ResolveCanonicalModel(ctx context.Context, repo store.CanonicalModelRepository, modelKey string) (store.CanonicalModel, error) {
	candidates, err := ResolveCanonicalModelCandidates(ctx, repo, modelKey)
	if err != nil {
		return store.CanonicalModel{}, err
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	for _, candidate := range candidates {
		count, err := repo.RoutableSiteModelCount(ctx, candidate.ID)
		if err != nil {
			return store.CanonicalModel{}, err
		}
		if count > 0 {
			return candidate, nil
		}
	}
	return candidates[0], nil
}

func lookupCanonicalAlias(ctx context.Context, repo store.CanonicalModelRepository, rawNormalized string, normalized string) (store.CanonicalModelAlias, store.CanonicalModel, bool, error) {
	keys := make([]string, 0, 2)
	if rawNormalized != "" {
		keys = append(keys, rawNormalized)
	}
	if normalized != "" && normalized != rawNormalized {
		keys = append(keys, normalized)
	}
	for _, key := range keys {
		alias, model, err := repo.GetAliasWithModel(ctx, key)
		if err == nil {
			if model.ID == uuid.Nil {
				continue
			}
			return alias, model, true, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return store.CanonicalModelAlias{}, store.CanonicalModel{}, false, err
		}
	}
	return store.CanonicalModelAlias{}, store.CanonicalModel{}, false, nil
}

func isManualAliasSource(source string) bool {
	return strings.EqualFold(strings.TrimSpace(source), "manual")
}
