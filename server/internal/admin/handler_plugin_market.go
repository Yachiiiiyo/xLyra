package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"xlyra/server/internal/httpx"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/store"
)

func RegisterPluginMarket(r chi.Router, h Handler) {
	r.Get("/plugin-market/status", h.GetPluginMarketStatus)
	r.Post("/plugin-market/sync", h.SyncPluginMarket)
	r.Get("/plugin-market/icons/{sha256}", h.GetPluginMarketIcon)
	r.Get("/plugin-market", h.ListPluginMarket)
	r.Post("/plugin-market/{id}/install", h.InstallPluginMarket)
	r.Get("/plugin-market/{id}", h.GetPluginMarketPlugin)
}

func (h Handler) SyncPluginMarket(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "plugin_market_unavailable", "plugin market is not available")
		return
	}
	status, err := h.pluginMarket.SyncOnce(r.Context(), true)
	if err != nil {
		h.recordJSPluginAudit(r, "js_plugin_market.sync", "", false, "plugin_market_sync_failed", map[string]any{"error": err.Error()})
		h.writeError(w, r, http.StatusBadGateway, "plugin_market_sync_failed", err.Error())
		return
	}
	success := status.Error == ""
	errorCode := ""
	if !success {
		errorCode = "plugin_market_sync_failed"
	}
	h.recordJSPluginAudit(r, "js_plugin_market.sync", "", success, errorCode, map[string]any{
		"source_url": status.SourceURL,
		"error":      status.Error,
	})
	httpx.JSON(w, http.StatusOK, status)
}

func (h Handler) ListPluginMarket(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "plugin_market_unavailable", "plugin market is not available")
		return
	}
	catalog, err := h.pluginMarket.List(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "plugin_market_list_failed", "failed to list plugin market")
		return
	}
	httpx.JSON(w, http.StatusOK, catalog)
}

func (h Handler) GetPluginMarketIcon(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "plugin_market_unavailable", "plugin market is not available")
		return
	}
	sum := strings.TrimSpace(chi.URLParam(r, "sha256"))
	body, err := h.pluginMarket.Icon(r.Context(), sum)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, market.ErrIconNotFound) {
			h.writeError(w, r, http.StatusNotFound, "js_plugin_market_not_found", "plugin not found")
			return
		}
		h.writeError(w, r, http.StatusBadGateway, "js_plugin_market_icon_failed", "failed to load plugin market icon")
		return
	}
	etag := `"` + sum + `"`
	header := w.Header()
	header.Set("Content-Type", "image/png")
	header.Set("ETag", etag)
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'")
	if ifNoneMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h Handler) GetPluginMarketPlugin(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "plugin_market_unavailable", "plugin market is not available")
		return
	}
	item, err := h.pluginMarket.Get(r.Context(), strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, market.ErrNotFound) {
			h.writeError(w, r, http.StatusNotFound, "js_plugin_market_not_found", "plugin not found")
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "plugin_market_get_failed", "failed to read plugin market plugin")
		return
	}
	httpx.JSON(w, http.StatusOK, item)
}

func (h Handler) InstallPluginMarket(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_market_unavailable", "plugin market is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	version, err := decodeInstallVersion(r)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	before := map[string]struct{}{}
	if h.trafficDB != nil {
		rows, listErr := store.NewJSPluginRepository(h.trafficDB.DB()).ListVersions(r.Context(), pluginID)
		if listErr == nil {
			for _, row := range rows {
				before[row.Version] = struct{}{}
			}
		}
	}
	row, err := h.pluginMarket.Install(r.Context(), adminIDFromContext(r.Context()), pluginID, version)
	if err != nil {
		status, code, message, fields := installHTTPError(err)
		meta := map[string]any{}
		if version != "" {
			meta["version"] = version
		}
		for key, value := range fields {
			meta[key] = value
		}
		h.recordJSPluginAudit(r, "js_plugin_market.install", pluginID, false, code, meta)
		if len(fields) > 0 {
			h.writeErrorFields(w, r, status, code, message, fields)
			return
		}
		h.writeError(w, r, status, code, message)
		return
	}
	_, reused := before[row.Version]
	h.recordJSPluginAudit(r, "js_plugin_market.install", row.PluginID, true, "", map[string]any{
		"version":        row.Version,
		"package_digest": row.PackageSHA256,
		"reused":         reused,
	})
	httpx.JSON(w, http.StatusCreated, h.enrichJSPluginVersion(r.Context(), row))
}

func decodeInstallVersion(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	return body.Version, nil
}

func installHTTPError(err error) (status int, code, message string, fields map[string]any) {
	var installErr *market.InstallError
	if !errors.As(err, &installErr) || installErr == nil {
		return http.StatusInternalServerError, "js_plugin_market_install_failed", "failed to install plugin", nil
	}
	switch installErr.Code {
	case market.InstallCodeNotFound:
		return http.StatusNotFound, "js_plugin_market_not_found", "plugin not found", nil
	case market.InstallCodeVersionNotFound:
		return http.StatusNotFound, "js_plugin_market_version_not_found", "plugin version not found", nil
	case market.InstallCodeIncompatible:
		return http.StatusConflict, "js_plugin_market_incompatible", "plugin version is incompatible", map[string]any{"reason": installErr.Reason}
	case market.InstallCodeRevoked:
		return http.StatusConflict, "js_plugin_market_revoked", "plugin version is revoked", nil
	case market.InstallCodeRevocationsExpired:
		return http.StatusConflict, "js_plugin_market_revocations_expired", "revocation list has expired", nil
	case market.InstallCodeDeveloperMismatch:
		return http.StatusConflict, "js_plugin_market_developer_mismatch", "plugin signer does not match the market developer", nil
	case market.InstallCodeDownloadFailed:
		return http.StatusBadGateway, "js_plugin_market_download_failed", "failed to download plugin package", nil
	case market.InstallCodeVerifyFailed:
		return http.StatusUnprocessableEntity, "js_plugin_market_verify_failed", "plugin attestation verification failed", map[string]any{"reason": installErr.Proto}
	case market.InstallCodePackageInvalid:
		return http.StatusUnprocessableEntity, "js_plugin_package_invalid", "plugin package is invalid", nil
	case market.InstallCodeUnsigned:
		return http.StatusUnprocessableEntity, "js_plugin_market_unsigned", "plugin package is unsigned", nil
	case market.InstallCodeSelfTestFailed:
		return http.StatusUnprocessableEntity, "js_plugin_market_selftest_failed", "plugin self-test failed", nil
	case market.InstallCodeVersionConflict:
		return http.StatusConflict, "js_plugin_version_conflict", jsplugin.ErrVersionContentMismatch.Error(), nil
	case market.InstallCodeUnavailable:
		return http.StatusServiceUnavailable, "js_plugin_market_unavailable", "plugin market is not available", nil
	default:
		return http.StatusInternalServerError, "js_plugin_market_install_failed", "failed to install plugin", nil
	}
}

func (h Handler) UpdateJSPluginSubscription(w http.ResponseWriter, r *http.Request) {
	if h.trafficDB == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "js_plugin_unavailable", "database is not available")
		return
	}
	pluginID := strings.TrimSpace(chi.URLParam(r, "id"))
	var body struct {
		UpdatePolicy string `json:"update_policy"`
	}
	if err := httpx.DecodeJSONBody(r, &body); err != nil {
		h.writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	policy := strings.TrimSpace(body.UpdatePolicy)
	switch policy {
	case store.JSPluginUpdatePolicyOff, store.JSPluginUpdatePolicyNotify, store.JSPluginUpdatePolicyAuto:
	default:
		h.writeError(w, r, http.StatusBadRequest, "js_plugin_subscription_invalid", "update_policy must be off, notify, or auto")
		return
	}
	repo := store.NewJSPluginRepository(h.trafficDB.DB())
	versions, err := repo.ListVersions(r.Context(), pluginID)
	if err != nil || len(versions) == 0 {
		h.writeError(w, r, http.StatusNotFound, "not_found", "plugin not found")
		return
	}
	if err := repo.SetMarketPolicy(r.Context(), pluginID, policy); err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "js_plugin_subscription_failed", "failed to update plugin subscription")
		return
	}
	h.recordJSPluginAudit(r, "js_plugin.subscription", pluginID, true, "", map[string]any{"update_policy": policy})
	httpx.JSON(w, http.StatusOK, map[string]any{
		"plugin_id":     pluginID,
		"update_policy": policy,
		"paused_reason": "",
	})
}

func (h Handler) installedMarketState(ctx context.Context) (market.InstalledState, error) {
	if h.pluginMarket == nil {
		return market.InstalledState{}, nil
	}
	return h.pluginMarket.InstalledState(ctx)
}

func applyPluginMarketState(item map[string]any, state market.InstalledState, pluginID string, versions []store.JSPluginVersion) {
	view, ok := state.Plugins[pluginID]
	if ok && view.Policy != "" {
		item["update_policy"] = view.Policy
		item["paused_reason"] = view.PausedReason
		item["update_available"] = view.UpdateAvailable
		if view.UpdateVersion != "" {
			item["update_version"] = view.UpdateVersion
		}
		if view.BlockReason != "" {
			item["auto_block_reason"] = view.BlockReason
		}
		if view.BlockDetail != "" {
			item["auto_block_detail"] = view.BlockDetail
		}
		if view.SignerRevoked {
			item["signer_revoked"] = true
		}
		if view.AutoRolledBack {
			item["auto_rolled_back"] = true
		}
	}
	for _, row := range versions {
		mark, marked := state.Versions[pluginID+"@"+row.Version]
		if !marked {
			continue
		}
		if mark.SignerRevoked {
			item["signer_revoked"] = true
		}
		if mark.AutoRolledBack {
			item["auto_rolled_back"] = true
		}
		if !mark.Revoked || row.Status != store.JSPluginStatusEnabled {
			continue
		}
		item["revocation_level"] = mark.Level
		item["revocation_reason"] = mark.Reason
		if mark.Level == "deprecated" {
			item["deprecated"] = true
		}
	}
}

func applyVersionMarketMark(item map[string]any, state market.InstalledState, row store.JSPluginVersion) {
	mark, ok := state.Versions[row.PluginID+"@"+row.Version]
	if !ok {
		return
	}
	if mark.Revoked {
		item["revocation"] = map[string]any{"revoked": true, "level": mark.Level, "reason": mark.Reason}
	}
	if mark.SignerRevoked {
		item["signer_revoked"] = true
		if mark.SignerReason != "" {
			item["signer_revoked_reason"] = mark.SignerReason
		}
	}
	if mark.AutoRolledBack {
		item["auto_rolled_back"] = true
	}
}

func (h Handler) GetPluginMarketStatus(w http.ResponseWriter, r *http.Request) {
	if h.pluginMarket == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "plugin_market_unavailable", "plugin market is not available")
		return
	}
	status, err := h.pluginMarket.CacheStatus(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, r, http.StatusInternalServerError, "plugin_market_status_failed", "failed to read plugin market status")
		return
	}
	httpx.JSON(w, http.StatusOK, status)
}
