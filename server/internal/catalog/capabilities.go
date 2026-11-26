package catalog

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"xlyra/server/internal/modelcapabilities"
	"xlyra/server/internal/store"
)

type capabilitySource struct{ db *store.Store }

func NewCapabilityService(db *store.Store) *modelcapabilities.Service {
	return modelcapabilities.NewWithConfig(modelcapabilities.Config{Catalog: capabilitySource{db: db}})
}

func (capabilitySource) Name() string { return "catalog" }

func (s capabilitySource) Lookup(ctx context.Context, input modelcapabilities.Input) (map[string]any, bool, error) {
	if s.db == nil || s.db.DB() == nil {
		return nil, false, nil
	}
	key := CanonicalModelKeyFromUpstream(input.ModelID)
	repo := store.NewCanonicalModelRepository(s.db.DB())
	model, err := repo.GetByKey(ctx, key)
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && model.Status == "archived") {
		if _, aliasModel, found, aliasErr := lookupCanonicalAlias(ctx, repo, NormalizeModelKey(input.ModelID), key); aliasErr != nil {
			return nil, false, aliasErr
		} else if found && aliasModel.Status != "archived" {
			model = aliasModel
			err = nil
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	values := map[string]any{}
	if err := json.Unmarshal(model.Capabilities, &values); err != nil {
		return nil, false, err
	}
	if values == nil {
		values = map[string]any{}
	}
	var endpoints []string
	if err := json.Unmarshal(model.SupportedEndpointTypes, &endpoints); err != nil {
		return nil, false, err
	}
	delete(values, "supported_endpoint_types")
	if len(endpoints) > 0 {
		values["supported_endpoint_types"] = endpoints
	}
	values["name"] = model.DisplayName
	values["category"] = model.Category
	if model.ContextWindow.Valid {
		values["context_window"] = model.ContextWindow.Int32
	}
	if model.MaxOutputTokens.Valid {
		values["max_output_tokens"] = model.MaxOutputTokens.Int32
	}
	return values, true, nil
}
