package agentctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/site"
	"xlyra/server/internal/store"
	"xlyra/server/internal/usage"
)

func (e *Engine) dispatchResource(ctx context.Context, _ Caller, spec OpSpec, req CallRequest) (callResult, *callError) {
	switch spec.Resource {
	case "proxy":
		return e.handleProxy(ctx, spec.Action, req)
	case "site":
		return e.handleSite(ctx, spec.Action, req)
	case "site-group":
		return e.handleSiteGroup(ctx, spec.Action, req)
	case "site-key":
		return e.handleSiteKey(ctx, spec.Action, req)
	case "model":
		return e.handleModel(ctx, spec.Action, req)
	case "site-model":
		return e.handleSiteModel(ctx, spec.Action, req)
	case "pricing":
		return e.handlePricing(ctx, spec.Action, req)
	case "api-key":
		return e.handleAPIKey(ctx, spec.Action, req)
	case "route":
		return e.handleRoute(ctx, spec.Action, req)
	case "cooldown":
		return e.handleCooldown(ctx, spec.Action, req)
	case "agents-md":
		return e.handleAgentsMD()
	case "skill":
		return e.handleSkill(spec.Action, req)
	case "backup":
		return e.handleBackupSections()
	case "cloud":
		return e.handleCloudStatus(ctx)
	case "dashboard":
		return e.handleDashboard(ctx, spec.Action)
	case "request":
		return e.handleRequestLogs(ctx, spec.Action, req)
	case "usage":
		return e.handleUsage(ctx, spec.Action, req)
	case "plugin":
		return e.handlePlugin(ctx, spec.Action, req)
	case "plugin-market":
		return e.handlePluginMarket(ctx, spec.Action, req)
	case "trusted-key":
		return e.handleTrustedKeyList(ctx)
	case "audit":
		return e.handleAuditList(ctx, req)
	default:
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown resource "+spec.Resource)
	}
}

// --- proxy ---

func (e *Engine) handleProxy(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	deps := e.Deps()
	cfg := httpclient.ReadNetworkConfig(deps.ConfFile)
	switch action {
	case "list":
		items := make([]map[string]any, 0, len(cfg.Proxies))
		for _, p := range cfg.Proxies {
			items = append(items, map[string]any{
				"id":   p.ID,
				"name": p.Name,
				"type": p.Type,
				"url":  RedactURL(p.URL),
			})
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil
	case "get":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "proxy id is required").withField("target")
		}
		for _, p := range cfg.Proxies {
			if p.ID == target || p.Name == target {
				return callResult{Data: map[string]any{
					"id": p.ID, "name": p.Name, "type": p.Type, "url": RedactURL(p.URL),
				}}, nil
			}
		}
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("proxy %q not found", target))
	case "create", "update", "delete":
		if deps.ConfFile == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "config file is unavailable")
		}
		_, rev := e.buildConfigSnapshot(ctx)
		if strings.TrimSpace(req.Options.IfMatch) != "" && strings.TrimSpace(req.Options.IfMatch) != rev {
			return callResult{}, newCallError(http.StatusPreconditionFailed, ErrCodePreconditionFailed,
				fmt.Sprintf("config revision mismatch: got %s, current %s", req.Options.IfMatch, rev))
		}
		return e.planProxyMutation(action, req, cfg, rev, deps)
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown proxy action")
}

func (e *Engine) planProxyMutation(action string, req CallRequest, cfg httpclient.NetworkConfig, rev string, deps Deps) (callResult, *callError) {
	next := httpclient.NetworkConfig{Proxies: slices.Clone(cfg.Proxies)}
	tier := Tier2Managed
	var diffs []DiffItem
	var summary string

	switch action {
	case "create":
		var in httpclient.ProxyProfile
		if ce := decodePayloadInto(req.Payload, &in); ce != nil {
			return callResult{}, ce
		}
		if strings.TrimSpace(in.ID) == "" {
			in.ID = uuid.NewString()
		}
		if hasURLPassword(in.URL) {
			return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
				"代理 URL 中禁止通过 CLI 提交密码，请在 Web 控制台设置含认证信息的代理").withTier(Tier0Forbidden)
		}
		next.Proxies = append(next.Proxies, in)
		next = httpclient.NormalizeNetworkConfig(next)
		if err := httpclient.ValidateNetworkConfig(next); err != nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
		}
		summary = fmt.Sprintf("新增网络代理 %s (%s)", in.Name, in.ID)
		diffs = []DiffItem{{
			Path:   "network.proxies." + in.ID,
			Before: nil,
			After:  map[string]any{"id": in.ID, "name": in.Name, "type": in.Type, "url": RedactURL(in.URL)},
		}}
	case "update":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "proxy id is required").withField("target")
		}
		var in map[string]any
		if ce := decodePayloadInto(req.Payload, &in); ce != nil {
			return callResult{}, ce
		}
		idx := slices.IndexFunc(next.Proxies, func(p httpclient.ProxyProfile) bool { return p.ID == target })
		if idx < 0 {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("proxy %q not found", target))
		}
		before := next.Proxies[idx]
		updated := before
		if v, ok := in["name"].(string); ok {
			updated.Name = v
		}
		if v, ok := in["type"].(string); ok {
			updated.Type = v
		}
		if v, ok := in["url"].(string); ok {
			if hasURLPassword(v) {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
					"代理 URL 中禁止通过 CLI 提交密码").withTier(Tier0Forbidden)
			}
			updated.URL = v
		}
		next.Proxies[idx] = updated
		next = httpclient.NormalizeNetworkConfig(next)
		if err := httpclient.ValidateNetworkConfig(next); err != nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
		}
		summary = fmt.Sprintf("更新网络代理 %s", updated.ID)
		diffs = []DiffItem{{
			Path:   "network.proxies." + updated.ID,
			Before: before,
			After:  updated,
		}}
	case "delete":
		tier = Tier3HighRisk
		target := strings.TrimSpace(req.Target)
		if target == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "proxy id is required").withField("target")
		}
		idx := slices.IndexFunc(next.Proxies, func(p httpclient.ProxyProfile) bool { return p.ID == target })
		if idx < 0 {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("proxy %q not found", target))
		}
		removed := next.Proxies[idx]
		next.Proxies = slices.Delete(next.Proxies, idx, idx+1)
		next = httpclient.NormalizeNetworkConfig(next)
		summary = fmt.Sprintf("删除网络代理 %s (%s)", removed.Name, removed.ID)
		diffs = []DiffItem{{
			Path:   "network.proxies." + removed.ID,
			Before: removed,
			After:  nil,
		}}
	}

	return callResult{
		Plan: &mutationPlan{
			Tier:           tier,
			Revision:       rev,
			Summary:        summary,
			Diff:           diffs,
			AppliedEffects: []string{"network.proxies." + action},
			Commit: func(commitCtx context.Context) (any, error) {
				if err := deps.ConfFile.Set("network", next); err != nil {
					return nil, err
				}
				_, nextRev := e.buildConfigSnapshot(commitCtx)
				return map[string]any{"updated": true, "revision": nextRev, "proxy_count": len(next.Proxies)}, nil
			},
		},
	}, nil
}

func hasURLPassword(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return false
	}
	pw, ok := u.User.Password()
	return ok && pw != ""
}

// --- site ---

func (e *Engine) handleSite(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	sites := e.Deps().Sites
	if sites == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	switch action {
	case "list":
		list, err := sites.List(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		states, _ := sites.SiteStates(ctx)
		q := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "q")))
		typeFilter := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "site_type")))
		if typeFilter == "" {
			typeFilter = strings.ToLower(strings.TrimSpace(stringParam(req.Params, "type")))
		}
		_, hasEnabledFilter := req.Params["enabled"]
		enabledFilter := boolParam(req.Params, "enabled", true)
		items := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if typeFilter != "" && strings.ToLower(item.SiteType) != typeFilter {
				continue
			}
			if hasEnabledFilter && item.Enabled != enabledFilter {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(item.Name+item.Slug+item.BaseURL), q) {
				continue
			}
			entry := map[string]any{
				"id":               item.ID,
				"name":             item.Name,
				"slug":             item.Slug,
				"site_type":        item.SiteType,
				"base_url":         RedactURL(item.BaseURL),
				"status":           item.Status,
				"enabled":          item.Enabled,
				"routing_priority": item.RoutingPriority,
				"updated_at":       item.UpdatedAt,
			}
			if st, ok := states[item.ID]; ok {
				entry["state"] = st
			}
			items = append(items, entry)
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil

	case "get":
		item, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var metaObj any
		if len(item.Meta) > 0 {
			_ = json.Unmarshal(item.Meta, &metaObj)
		}
		states, _ := sites.SiteStates(ctx)
		return callResult{Data: map[string]any{
			"id":               item.ID,
			"name":             item.Name,
			"slug":             item.Slug,
			"site_type":        item.SiteType,
			"base_url":         RedactURL(item.BaseURL),
			"status":           item.Status,
			"enabled":          item.Enabled,
			"routing_priority": item.RoutingPriority,
			"meta":             metaObj,
			"state":            states[item.ID],
			"created_at":       item.CreatedAt,
			"updated_at":       item.UpdatedAt,
		}}, nil

	case "health":
		if strings.TrimSpace(req.Target) == "" {
			states, err := sites.SiteHealthStates(ctx)
			if err != nil {
				return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
			}
			return callResult{Data: map[string]any{"items": states, "count": len(states)}}, nil
		}
		resolvedSite, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		res, err := sites.SiteHealth(ctx, resolvedSite.ID)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		return callResult{Data: res}, nil

	case "enable", "disable":
		cur, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		id := cur.ID
		want := action == "enable"
		diffs := []DiffItem{{Path: fmt.Sprintf("site.%s.enabled", id), Before: cur.Enabled, After: want}}
		return callResult{
			Plan: &mutationPlan{
				Tier:           Tier2Managed,
				Summary:        fmt.Sprintf("%s 站点 %s (%s)", map[bool]string{true: "启用", false: "停用"}[want], cur.Name, id),
				Diff:           diffs,
				NoOp:           cur.Enabled == want,
				AppliedEffects: []string{"site." + action},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := sites.SetEnabled(commitCtx, id, want)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return map[string]any{"id": updated.ID, "name": updated.Name, "enabled": updated.Enabled}, nil
				},
			},
		}, nil

	case "update":
		cur, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		id := cur.ID
		var body map[string]any
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		for _, forbidden := range []string{"credential", "credentials", "api_key", "secret", "request_headers"} {
			if _, exists := body[forbidden]; exists {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
					fmt.Sprintf("field %q cannot be modified via agent CLI", forbidden)).withTier(Tier0Forbidden).withField(forbidden)
			}
		}
		params := site.UpdateSiteParams{
			ID:       cur.ID,
			Name:     cur.Name,
			Slug:     cur.Slug,
			SiteType: cur.SiteType,
			BaseURL:  cur.BaseURL,
			Enabled:  cur.Enabled,
		}
		diffs := []DiffItem{}
		if v, ok := body["name"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Name {
			diffs = append(diffs, DiffItem{Path: "site.name", Before: cur.Name, After: strings.TrimSpace(v)})
			params.Name = strings.TrimSpace(v)
		}
		if v, ok := body["base_url"].(string); ok && strings.TrimSpace(v) != "" && v != cur.BaseURL {
			if hasURLPassword(v) {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied, "URL 中禁止包含密码").withTier(Tier0Forbidden)
			}
			diffs = append(diffs, DiffItem{Path: "site.base_url", Before: cur.BaseURL, After: strings.TrimSpace(v)})
			params.BaseURL = strings.TrimSpace(v)
		}
		if v, ok := body["enabled"].(bool); ok && v != cur.Enabled {
			diffs = append(diffs, DiffItem{Path: "site.enabled", Before: cur.Enabled, After: v})
			params.Enabled = v
		}
		if v, ok := body["routing_priority"].(float64); ok && v != cur.RoutingPriority {
			diffs = append(diffs, DiffItem{Path: "site.routing_priority", Before: cur.RoutingPriority, After: v})
			params.RoutingPriority = &v
		}
		if v, ok := body["proxy_id"].(string); ok {
			diffs = append(diffs, DiffItem{Path: "site.proxy_id", Before: nil, After: strings.TrimSpace(v)})
			trimmed := strings.TrimSpace(v)
			params.ProxyID = &trimmed
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:           Tier3HighRisk,
				Summary:        fmt.Sprintf("更新站点 %s (%s) 元数据", cur.Name, id),
				Diff:           diffs,
				NoOp:           len(diffs) == 0,
				AppliedEffects: []string{"site.updated"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, _, err := sites.Update(commitCtx, params)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return map[string]any{
						"id":               updated.ID,
						"name":             updated.Name,
						"base_url":         RedactURL(updated.BaseURL),
						"enabled":          updated.Enabled,
						"routing_priority": updated.RoutingPriority,
					}, nil
				},
			},
		}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown site action")
}

// --- site-group ---

func (e *Engine) handleSiteGroup(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	sites := e.Deps().Sites
	if sites == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	switch action {
	case "list":
		groups, err := sites.ListSiteGroups(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: map[string]any{"items": groups, "count": len(groups)}}, nil
	case "get":
		id, ce := parseUUIDParam(req.Target, "group_id")
		if ce != nil {
			return callResult{}, ce
		}
		group, err := sites.GetSiteGroup(ctx, id)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		members, _ := sites.SiteGroupSites(ctx, id)
		return callResult{Data: map[string]any{"group": group, "sites": members}}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown site-group action")
}

// --- site-key ---

func (e *Engine) handleSiteKey(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	sites := e.Deps().Sites
	if sites == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	resolvedSite, ce := e.resolveSite(ctx, req.Target)
	if ce != nil {
		return callResult{}, ce
	}
	siteID := resolvedSite.ID
	switch action {
	case "list":
		keys, err := sites.APIKeys(ctx, siteID)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		items := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			items = append(items, sanitizeSiteAPIKey(k))
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil
	case "get":
		credID, ce := parseUUIDParam(req.SubTarget, "credential_id")
		if ce != nil {
			return callResult{}, ce
		}
		k, err := sites.APIKeyByCredentialID(ctx, siteID, credID)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		return callResult{Data: sanitizeSiteAPIKey(k)}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown site-key action")
}

func sanitizeSiteAPIKey(k site.APIKeyCredential) map[string]any {
	return map[string]any{
		"id":                       k.Credential.ID,
		"site_id":                  k.Credential.SiteID,
		"credential_type":          k.Credential.CredentialType,
		"name":                     k.Name,
		"display_name":             k.DisplayName,
		"upstream_id":              k.UpstreamID,
		"enabled":                  k.Enabled,
		"masked_secret":            k.MaskedSecret,
		"secret_missing":           k.SecretMissing,
		"routing_priority":         k.RoutingPriority,
		"upstream_cost_multiplier": k.UpstreamCostMultiplier,
		"cache_domain":             k.CacheDomain,
		"created_at":               k.Credential.CreatedAt,
		"updated_at":               k.Credential.UpdatedAt,
	}
}

// --- pricing ---

func (e *Engine) handlePricing(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	sites := e.Deps().Sites
	if sites == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	filters := site.ModelPriceFilters{
		Q:             stringParam(req.Params, "q"),
		SiteType:      stringParam(req.Params, "site_type"),
		Provider:      stringParam(req.Params, "provider"),
		ModelKey:      stringParam(req.Params, "model_key"),
		BillingType:   stringParam(req.Params, "billing_type"),
		PricingStatus: stringParam(req.Params, "pricing_status"),
	}
	if siteFilter := strings.TrimSpace(stringParam(req.Params, "site")); siteFilter != "" {
		resolved, ce := e.resolveSite(ctx, siteFilter)
		if ce != nil {
			return callResult{}, ce
		}
		filters.SiteID = resolved.ID
	}
	if action == "get" && strings.TrimSpace(req.Target) != "" {
		if filters.ModelKey == "" {
			filters.ModelKey = strings.TrimSpace(req.Target)
		}
	}
	res, err := sites.ListModelPrices(ctx, filters)
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	return callResult{Data: res}, nil
}

// --- api-key ---

func (e *Engine) handleAPIKey(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	authSvc := e.Deps().Auth
	if authSvc == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "auth service is unavailable")
	}
	switch action {
	case "list":
		details, err := authSvc.ListAPIKeyDetails(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		items := make([]map[string]any, 0, len(details))
		for _, item := range details {
			if item.APIKey.KeyKind == store.APIKeyKindAgentInternal {
				continue
			}
			items = append(items, map[string]any{
				"id":                     item.APIKey.ID,
				"name":                   item.APIKey.Name,
				"key_prefix":             item.APIKey.KeyPrefix,
				"masked_key":             item.APIKey.MaskedKey,
				"key_kind":               item.APIKey.KeyKind,
				"scope":                  item.APIKey.Scope,
				"status":                 item.APIKey.Status,
				"model_policy":           item.APIKey.ModelPolicy,
				"site_policy":            item.APIKey.SitePolicy,
				"billing_multiplier":     item.APIKey.BillingMultiplier,
				"quota_unlimited":        item.APIKey.QuotaUnlimited,
				"quota_used":             item.APIKey.QuotaUsed,
				"quota_total_used":       item.APIKey.QuotaTotalUsed,
				"quota_daily_unlimited":  item.APIKey.QuotaDailyUnlimited,
				"quota_daily_used":       item.APIKey.QuotaDailyUsed,
				"quota_weekly_unlimited": item.APIKey.QuotaWeeklyUnlimited,
				"quota_weekly_used":      item.APIKey.QuotaWeeklyUsed,
				"rate_limit":             item.RateLimit,
				"expires_at":             item.APIKey.ExpiresAt,
				"last_used_at":           item.APIKey.LastUsedAt,
			})
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil

	case "get":
		id, ce := e.resolveAPIKeyID(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		item, err := authSvc.GetAPIKey(ctx, id)
		if err != nil || item.KeyKind == store.APIKeyKindAgentInternal {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "api key not found")
		}
		return callResult{Data: map[string]any{
			"id":                 item.ID,
			"name":               item.Name,
			"key_prefix":         item.KeyPrefix,
			"masked_key":         item.MaskedKey,
			"key_kind":           item.KeyKind,
			"scope":              item.Scope,
			"status":             item.Status,
			"model_policy":       item.ModelPolicy,
			"site_policy":        item.SitePolicy,
			"billing_multiplier": item.BillingMultiplier,
			"quota_unlimited":    item.QuotaUnlimited,
			"quota_used":         item.QuotaUsed,
			"quota_total_used":   item.QuotaTotalUsed,
			"expires_at":         item.ExpiresAt,
			"last_used_at":       item.LastUsedAt,
			"created_at":         item.CreatedAt,
			"updated_at":         item.UpdatedAt,
		}}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown api-key action")
}

// --- capabilities / backup / cloud ---

func (e *Engine) handleAgentsMD() (callResult, *callError) {
	caps := e.capsStore()
	if caps == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "capabilities store is unavailable")
	}
	content, exists, err := caps.GetAgentsMD()
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	return callResult{Data: map[string]any{"exists": exists, "content": content}}, nil
}

func (e *Engine) handleSkill(action string, req CallRequest) (callResult, *callError) {
	caps := e.capsStore()
	if caps == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "capabilities store is unavailable")
	}
	switch action {
	case "list":
		info, err := caps.ListSkills()
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: info}, nil
	case "get":
		name := strings.TrimSpace(req.Target)
		if name == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "skill name is required").withField("target")
		}
		detail, err := caps.GetSkill(name)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		return callResult{Data: detail}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown skill action")
}

func (e *Engine) handleBackupSections() (callResult, *callError) {
	items := []map[string]any{
		{"id": "settings", "group": "system", "description": "全局常规、门户与外观配置"},
		{"id": "gateway_sites", "group": "gateway", "description": "站点、凭证、模型、价格与网络代理配置"},
		{"id": "gateway_keys", "group": "gateway", "description": "外部 API Key 权限与限流规则"},
		{"id": "gateway_logs", "group": "gateway", "description": "代理请求日志与用量汇总"},
		{"id": "plugins", "group": "gateway", "description": "JS 插件与市场绑定"},
		{"id": "agent_config", "group": "agent", "description": "内置 Agent 配置"},
		{"id": "agent_capabilities", "group": "agent", "description": "AGENTS.md 与自定义 Skills"},
		{"id": "agent_sessions", "group": "agent", "description": "Agent 会话与历史消息"},
		{"id": "agent_workspace", "group": "agent", "description": "Agent 共享工作区文件"},
	}
	return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil
}

func (e *Engine) handleCloudStatus(ctx context.Context) (callResult, *callError) {
	cloud := e.Deps().Cloud
	if cloud == nil {
		return callResult{Data: map[string]any{"available": false, "status": "unbound"}}, nil
	}
	st, err := cloud.Status(ctx)
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	return callResult{Data: st}, nil
}

// --- observability ---

func (e *Engine) handleDashboard(ctx context.Context, action string) (callResult, *callError) {
	dash := e.Deps().Dashboard
	if dash == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "dashboard service is unavailable")
	}
	now := time.Now()
	switch action {
	case "overview":
		res, err := dash.Usage(ctx, now)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: res}, nil
	case "insights":
		res, err := dash.Insights(ctx, now)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: res}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown dashboard action")
}

func (e *Engine) handleRequestLogs(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	usageSvc := e.Deps().Usage
	if usageSvc == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "usage service is unavailable")
	}
	query := buildRequestQuery(req.Params)
	switch action {
	case "list":
		res, err := usageSvc.ListRequestsPage(ctx, query)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: res}, nil
	case "get":
		id, ce := parseUUIDParam(req.Target, "request_id")
		if ce != nil {
			return callResult{}, ce
		}
		detail, err := usageSvc.GetRequest(ctx, id)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		return callResult{Data: detail}, nil
	case "summary":
		res, err := usageSvc.RequestSummary(ctx, query, time.Now())
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: res}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown request action")
}

func (e *Engine) handleUsage(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	usageSvc := e.Deps().Usage
	dash := e.Deps().Dashboard
	now := time.Now()
	switch action {
	case "summary", "query":
		if usageSvc == nil && dash == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "usage service is unavailable")
		}
		out := map[string]any{}
		if dash != nil {
			if ov, err := dash.Usage(ctx, now); err == nil {
				out["overview"] = ov
			}
		}
		if usageSvc != nil {
			if sum, err := usageSvc.RequestSummary(ctx, buildRequestQuery(req.Params), now); err == nil {
				out["request_summary"] = sum
			}
		}
		return callResult{Data: out}, nil

	case "timeseries":
		if usageSvc != nil {
			if rawKeyID := strings.TrimSpace(stringParam(req.Params, "api_key_id")); rawKeyID != "" {
				id, ce := parseUUIDParam(rawKeyID, "api_key_id")
				if ce != nil {
					return callResult{}, ce
				}
				days := 14
				if d, ok := toInt(req.Params["days"]); ok && d > 0 && d <= 90 {
					days = d
				}
				trend, err := usageSvc.KeyDailyTrend(ctx, id, days, now)
				if err != nil {
					return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
				}
				return callResult{Data: trend}, nil
			}
		}
		if dash == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "dashboard service is unavailable")
		}
		ov, err := dash.Usage(ctx, now)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: ov}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown usage action")
}

func buildRequestQuery(params map[string]any) usage.RequestQuery {
	page := 1
	if v, ok := toInt(params["page"]); ok && v > 0 {
		page = v
	}
	pageSize := 20
	if v, ok := toInt(params["page_size"]); ok && v > 0 {
		pageSize = v
	} else if v, ok := toInt(params["limit"]); ok && v > 0 {
		pageSize = v
	}
	if pageSize > 50 {
		pageSize = 50
	}
	q := usage.RequestQuery{
		Page:      page,
		PageSize:  pageSize,
		Search:    stringParam(params, "q"),
		ModelKey:  stringParam(params, "model"),
		ErrorType: stringParam(params, "error_type"),
		Endpoint:  stringParam(params, "endpoint"),
		RequestID: stringParam(params, "request_id"),
	}
	if rawSite := strings.TrimSpace(stringParam(params, "site_id")); rawSite != "" {
		if id, err := uuid.Parse(rawSite); err == nil && id != uuid.Nil {
			q.SiteID = &id
		}
	}
	if rawKey := strings.TrimSpace(stringParam(params, "api_key_id")); rawKey != "" {
		if id, err := uuid.Parse(rawKey); err == nil && id != uuid.Nil {
			q.APIKeyID = &id
		}
	}
	if rawSuccess, exists := params["success"]; exists && rawSuccess != nil {
		b := boolParam(params, "success", true)
		q.Success = &b
	}
	return q
}

// --- plugins & audit ---

func (e *Engine) handlePlugin(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	deps := e.Deps()
	if deps.DB == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "plugin store is unavailable")
	}
	repo := store.NewJSPluginRepository(deps.DB.DB())
	switch action {
	case "list":
		plugins, err := repo.ListPlugins(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		headers, err := repo.ListAllVersionHeaders(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		byPlugin := map[string][]store.JSPluginVersion{}
		for _, h := range headers {
			byPlugin[h.PluginID] = append(byPlugin[h.PluginID], h)
		}
		items := make([]map[string]any, 0, len(plugins))
		for _, p := range plugins {
			versions := byPlugin[p.ID]
			enabledVer := ""
			for _, v := range versions {
				if v.Status == store.JSPluginStatusEnabled {
					enabledVer = v.Version
					break
				}
			}
			items = append(items, map[string]any{
				"id":              p.ID,
				"source":          p.Source,
				"enabled_version": enabledVer,
				"version_count":   len(versions),
			})
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil

	case "get":
		id := strings.TrimSpace(req.Target)
		if id == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "plugin id is required").withField("target")
		}
		headers, err := repo.ListAllVersionHeaders(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		versions := make([]store.JSPluginVersion, 0)
		for _, h := range headers {
			if h.PluginID == id {
				versions = append(versions, h)
			}
		}
		if len(versions) == 0 {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("plugin %q not found", id))
		}
		out := map[string]any{
			"id":       id,
			"versions": versions,
		}
		if deps.JSPlugins != nil {
			if specs, _, vals, err := deps.JSPlugins.GetPluginSettings(ctx, id); err == nil {
				out["settings_spec"] = specs
				out["settings_values"] = vals
			}
		}
		return callResult{Data: out}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown plugin action")
}

func (e *Engine) handlePluginMarket(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	mkt := e.Deps().PluginMarket
	if mkt == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "plugin market service is unavailable")
	}
	catalog, err := mkt.List(ctx)
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	if action == "search" {
		q := strings.ToLower(strings.TrimSpace(req.Target))
		if q == "" {
			q = strings.ToLower(strings.TrimSpace(stringParam(req.Params, "q")))
		}
		if q != "" {
			filtered := make([]any, 0)
			for _, item := range catalog.Plugins {
				if strings.Contains(strings.ToLower(item.ID+" "+item.Name+" "+item.Summary), q) {
					filtered = append(filtered, item)
				}
			}
			return callResult{Data: map[string]any{"query": q, "items": filtered, "count": len(filtered)}}, nil
		}
	}
	return callResult{Data: catalog}, nil
}

func (e *Engine) handleTrustedKeyList(ctx context.Context) (callResult, *callError) {
	js := e.Deps().JSPlugins
	if js == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "js plugin manager is unavailable")
	}
	keys, err := js.ListTrustedKeys(ctx)
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	return callResult{Data: map[string]any{"items": keys, "count": len(keys)}}, nil
}

func (e *Engine) handleAuditList(ctx context.Context, req CallRequest) (callResult, *callError) {
	authSvc := e.Deps().Auth
	if authSvc == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "auth service is unavailable")
	}
	page := 1
	if v, ok := toInt(req.Params["page"]); ok && v > 0 {
		page = v
	}
	pageSize := 20
	if v, ok := toInt(req.Params["page_size"]); ok && v > 0 {
		pageSize = v
	}
	if pageSize > 50 {
		pageSize = 50
	}
	filters := store.AdminAuditLogFilters{
		Action:    stringParam(req.Params, "action"),
		ActorType: stringParam(req.Params, "actor_type"),
		Page:      page,
		PageSize:  pageSize,
	}
	rows, total, err := authSvc.ListAuditLogs(ctx, filters)
	if err != nil {
		return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	return callResult{Data: map[string]any{
		"items":     rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}}, nil
}
