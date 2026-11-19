package site

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"xlyra/server/internal/store"
)

// AttachGatewayPlugin appends pluginID to the site's mounted plugin list when
// it is not already there. Writing the list clears the legacy plugins map and
// a plugin: quota probe; a built-in quota probe is left as it is.
func (s *Service) AttachGatewayPlugin(ctx context.Context, siteID uuid.UUID, pluginID string) (store.Site, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" || strings.ContainsAny(pluginID, " \t\r\n") {
		return store.Site{}, fmt.Errorf("plugin id is required")
	}
	return s.patchGatewayPluginIDs(ctx, siteID, func(ids []string) []string {
		for _, id := range ids {
			if id == pluginID {
				return ids
			}
		}
		return append(ids, pluginID)
	})
}

// SetGatewayPluginIDs replaces the site's mounted plugin list. ids are trimmed,
// rejected when blank or containing whitespace, and deduplicated while keeping
// the first occurrence. Everything else in the gateway config is left as it is.
func (s *Service) SetGatewayPluginIDs(ctx context.Context, siteID uuid.UUID, ids []string) (store.Site, error) {
	clean := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, " \t\r\n") {
			return store.Site{}, fmt.Errorf("plugin id is required")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return s.patchGatewayPluginIDs(ctx, siteID, func([]string) []string { return clean })
}

// DetachGatewayPlugin removes pluginID from the site's mounted plugin list.
// The list is written back even when that leaves it empty, so the legacy
// fields are cleared too.
func (s *Service) DetachGatewayPlugin(ctx context.Context, siteID uuid.UUID, pluginID string) (store.Site, error) {
	pluginID = strings.TrimSpace(pluginID)
	return s.patchGatewayPluginIDs(ctx, siteID, func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != pluginID {
				out = append(out, id)
			}
		}
		return out
	})
}

func (s *Service) patchGatewayPluginIDs(ctx context.Context, siteID uuid.UUID, edit func([]string) []string) (store.Site, error) {
	if s == nil || s.db == nil {
		return store.Site{}, fmt.Errorf("site service is not available")
	}
	if siteID == uuid.Nil {
		return store.Site{}, fmt.Errorf("site id is required")
	}
	var updated store.Site
	err := s.db.WithinTx(ctx, func(tx store.Tx) error {
		siteRepo := store.NewSiteRepository(tx)
		existing, err := siteRepo.GetByID(ctx, siteID)
		if err != nil {
			return err
		}
		ids := edit(append([]string(nil), SitePluginIDs(GatewayConfigFromSiteMeta(existing.Meta))...))
		if ids == nil {
			ids = []string{}
		}
		meta, err := MergeSiteGatewayConfig(existing.Meta, &GatewayConfig{PluginIDs: ids})
		if err != nil {
			return err
		}
		updated, err = siteRepo.Update(ctx, store.UpdateSiteParams{
			ID:              existing.ID,
			Name:            existing.Name,
			Slug:            existing.Slug,
			SiteType:        existing.SiteType,
			BaseURL:         existing.BaseURL,
			Status:          existing.Status,
			Enabled:         existing.Enabled,
			RoutingPriority: existing.RoutingPriority,
			Meta:            meta,
		})
		return err
	})
	return updated, err
}

// PatchGatewayPluginSettings writes one plugin's per-site setting overrides.
// An empty values map clears that plugin's overrides. Other plugins are left
// as they are. The plugin must already be mounted; this only stores settings.
func (s *Service) PatchGatewayPluginSettings(ctx context.Context, siteID uuid.UUID, pluginID string, values map[string]any) (store.Site, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" || strings.ContainsAny(pluginID, " \t\r\n") {
		return store.Site{}, fmt.Errorf("plugin id is required")
	}
	if values == nil {
		values = map[string]any{}
	}
	if s == nil || s.db == nil {
		return store.Site{}, fmt.Errorf("site service is not available")
	}
	if siteID == uuid.Nil {
		return store.Site{}, fmt.Errorf("site id is required")
	}
	var updated store.Site
	err := s.db.WithinTx(ctx, func(tx store.Tx) error {
		siteRepo := store.NewSiteRepository(tx)
		existing, err := siteRepo.GetByID(ctx, siteID)
		if err != nil {
			return err
		}
		meta, err := MergeSiteGatewayConfig(existing.Meta, &GatewayConfig{
			PluginSettings: map[string]map[string]any{pluginID: values},
		})
		if err != nil {
			return err
		}
		updated, err = siteRepo.Update(ctx, store.UpdateSiteParams{
			ID:              existing.ID,
			Name:            existing.Name,
			Slug:            existing.Slug,
			SiteType:        existing.SiteType,
			BaseURL:         existing.BaseURL,
			Status:          existing.Status,
			Enabled:         existing.Enabled,
			RoutingPriority: existing.RoutingPriority,
			Meta:            meta,
		})
		return err
	})
	return updated, err
}
