package agentctl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"xlyra/server/internal/catalog"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/store"
	"xlyra/server/internal/usage"
)

// --- identifier resolvers ---

func (e *Engine) resolveSite(ctx context.Context, raw string) (store.Site, *callError) {
	sitesSvc := e.Deps().Sites
	if sitesSvc == nil {
		return store.Site{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	target := strings.TrimSpace(raw)
	if target == "" {
		return store.Site{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "site id or slug is required").withField("site")
	}
	if id, err := uuid.Parse(target); err == nil && id != uuid.Nil {
		item, err := sitesSvc.Get(ctx, id)
		if err != nil {
			return store.Site{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("site %q not found", target))
		}
		return item, nil
	}
	all, err := sitesSvc.List(ctx)
	if err != nil {
		return store.Site{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	var slugMatches []store.Site
	var nameMatches []store.Site
	for _, s := range all {
		if strings.EqualFold(strings.TrimSpace(s.Slug), target) {
			slugMatches = append(slugMatches, s)
		} else if strings.EqualFold(strings.TrimSpace(s.Name), target) {
			nameMatches = append(nameMatches, s)
		}
	}
	if len(slugMatches) == 1 {
		return slugMatches[0], nil
	}
	if len(slugMatches) > 1 {
		return store.Site{}, newCallError(http.StatusConflict, ErrCodeConflict, fmt.Sprintf("ambiguous site slug %q; please use UUID", target))
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], nil
	}
	if len(nameMatches) > 1 {
		return store.Site{}, newCallError(http.StatusConflict, ErrCodeConflict, fmt.Sprintf("ambiguous site name %q; please use UUID or slug", target))
	}
	return store.Site{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("site %q not found", target))
}

// resolveCanonicalModelItem resolves a canonical model for read/lookup operations
// (and merge/bind targets). Precedence:
//  1. Exact UUID match
//  2. Active exact model_key match (so an active empty shell can still be inspected by its own key)
//  3. Alias / routing resolution (so archived/merged keys or aliases resolve to the active model)
//  4. Archived exact model_key match
func (e *Engine) resolveCanonicalModelItem(ctx context.Context, raw string) (catalog.CanonicalModelItem, *callError) {
	return e.resolveCanonicalModelItemMode(ctx, raw, true)
}

// resolveCanonicalModelItemExact resolves a canonical model strictly by UUID or
// exact model_key without alias redirection. Used for mutating operations
// (update, delete, merge source, alias/pricing writes) so an alias can never
// silently redirect a destructive operation to a different model.
func (e *Engine) resolveCanonicalModelItemExact(ctx context.Context, raw string) (catalog.CanonicalModelItem, *callError) {
	return e.resolveCanonicalModelItemMode(ctx, raw, false)
}

func (e *Engine) resolveCanonicalModelItemMode(ctx context.Context, raw string, allowAlias bool) (catalog.CanonicalModelItem, *callError) {
	cat := e.Deps().Catalog
	if cat == nil {
		return catalog.CanonicalModelItem{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "catalog service is unavailable")
	}
	target := strings.TrimSpace(raw)
	if target == "" {
		return catalog.CanonicalModelItem{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "model id or key is required").withField("target")
	}
	items, err := cat.List(ctx)
	if err != nil {
		return catalog.CanonicalModelItem{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	if id, err := uuid.Parse(target); err == nil && id != uuid.Nil {
		for _, it := range items {
			if it.Model.ID == id {
				return it, nil
			}
		}
		return catalog.CanonicalModelItem{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("model %q not found", target))
	}

	normalized := catalog.NormalizeModelKey(target)
	matchExact := func(it catalog.CanonicalModelItem) bool {
		return strings.EqualFold(it.Model.ModelKey, target) || (normalized != "" && strings.EqualFold(it.Model.ModelKey, normalized))
	}

	// Active exact model_key match always wins over alias resolution.
	var archivedExact *catalog.CanonicalModelItem
	for i := range items {
		it := items[i]
		if !matchExact(it) {
			continue
		}
		if strings.EqualFold(it.Model.Status, "active") {
			return it, nil
		}
		if archivedExact == nil {
			cp := it
			archivedExact = &cp
		}
	}

	if allowAlias {
		if db := e.Deps().DB; db != nil {
			if resolved, err := catalog.ResolveCanonicalModel(ctx, store.NewCanonicalModelRepository(db.DB()), target); err == nil {
				for _, it := range items {
					if it.Model.ID == resolved.ID {
						return it, nil
					}
				}
			}
		}
		for _, it := range items {
			for _, a := range it.Aliases {
				if strings.EqualFold(a.Alias, target) || (normalized != "" && strings.EqualFold(a.NormalizedAlias, normalized)) {
					return it, nil
				}
			}
		}
	}

	if archivedExact != nil {
		return *archivedExact, nil
	}

	if !allowAlias {
		// Provide a helpful hint when the user passed an alias instead of the canonical model key.
		for _, it := range items {
			for _, a := range it.Aliases {
				if strings.EqualFold(a.Alias, target) || (normalized != "" && strings.EqualFold(a.NormalizedAlias, normalized)) {
					return catalog.CanonicalModelItem{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
						fmt.Sprintf("model %q not found by exact model_key (it is an alias of %s)", target, it.Model.ModelKey)).
						withHint(fmt.Sprintf("写操作不支持通过别名重定向，请直接使用规范模型标识 %s 或 UUID %s", it.Model.ModelKey, it.Model.ID))
				}
			}
		}
	}
	return catalog.CanonicalModelItem{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("model %q not found", target))
}

func (e *Engine) resolveAPIKeyID(ctx context.Context, raw string) (uuid.UUID, *callError) {
	authSvc := e.Deps().Auth
	if authSvc == nil {
		return uuid.Nil, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "auth service is unavailable")
	}
	target := strings.TrimSpace(raw)
	if target == "" {
		return uuid.Nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "api_key id or name is required").withField("target")
	}
	if id, err := uuid.Parse(target); err == nil && id != uuid.Nil {
		return id, nil
	}
	details, err := authSvc.ListAPIKeyDetails(ctx)
	if err != nil {
		return uuid.Nil, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
	}
	var matches []uuid.UUID
	for _, d := range details {
		if d.APIKey.KeyKind == store.APIKeyKindAgentInternal {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(d.APIKey.Name), target) {
			matches = append(matches, d.APIKey.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return uuid.Nil, newCallError(http.StatusConflict, ErrCodeConflict, fmt.Sprintf("ambiguous api key name %q; please use UUID", target))
	}
	return uuid.Nil, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("api key %q not found", target))
}

// --- model ---

func (e *Engine) handleModel(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	cat := e.Deps().Catalog
	if cat == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "catalog service is unavailable")
	}
	switch action {
	case "list":
		items, err := cat.List(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		q := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "search")))
		if q == "" {
			q = strings.ToLower(strings.TrimSpace(stringParam(req.Params, "q")))
		}
		providerFilter := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "provider")))
		categoryFilter := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "category")))
		statusFilter := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "status")))

		filtered := make([]map[string]any, 0, len(items))
		for _, it := range items {
			if providerFilter != "" && !strings.EqualFold(it.Model.Provider, providerFilter) {
				continue
			}
			if categoryFilter != "" && !strings.EqualFold(it.Model.Category, categoryFilter) {
				continue
			}
			if statusFilter != "" && !strings.EqualFold(it.Model.Status, statusFilter) {
				continue
			}
			if q != "" && !modelItemMatchesQuery(it, q) {
				continue
			}
			filtered = append(filtered, ctlCanonicalModelSummary(it))
		}
		page, pageSize := parsePagination(req.Params, 50, 200)
		paged, hasMore := slicePage(filtered, page, pageSize)
		var meta *Meta
		if hasMore {
			meta = &Meta{NextPage: page + 1}
		}
		return callResult{
			Data: map[string]any{
				"items":     paged,
				"count":     len(paged),
				"total":     len(filtered),
				"page":      page,
				"page_size": pageSize,
				"has_more":  hasMore,
			},
			Meta: meta,
		}, nil

	case "get":
		item, ce := e.resolveCanonicalModelItem(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		out := ctlCanonicalModelSummary(item)
		if routerSvc := e.Deps().Router; routerSvc != nil {
			if cand, err := routerSvc.Candidates(ctx, routeengine.CandidateQuery{ModelKey: item.Model.ModelKey}); err == nil {
				routableSites := map[uuid.UUID]struct{}{}
				for _, c := range cand.Items {
					routableSites[c.Site.ID] = struct{}{}
				}
				out["routable_candidate_count"] = len(cand.Items)
				out["routable_site_count"] = len(routableSites)
			}
		}
		if boolParam(req.Params, "matrix", false) {
			matrix, err := cat.Matrix(ctx, item.Model.ID)
			if err != nil {
				return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
			}
			rows := ctlGroupedMatrixItems(matrix.Rows)
			out["matrix"] = rows
			out["matrix_count"] = len(rows)
		}
		return callResult{Data: out}, nil

	case "matrix":
		item, ce := e.resolveCanonicalModelItem(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		matrix, err := cat.Matrix(ctx, item.Model.ID)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		rows := ctlGroupedMatrixItems(matrix.Rows)
		return callResult{Data: map[string]any{
			"canonical_model": ctlCanonicalModelSummary(item),
			"items":           rows,
			"count":           len(rows),
		}}, nil

	case "create":
		var in struct {
			ModelKey     string         `json:"model_key"`
			DisplayName  string         `json:"display_name"`
			Provider     string         `json:"provider"`
			Category     string         `json:"category"`
			Status       string         `json:"status"`
			Capabilities map[string]any `json:"capabilities"`
		}
		if ce := decodePayloadInto(req.Payload, &in); ce != nil {
			return callResult{}, ce
		}
		if strings.TrimSpace(in.ModelKey) == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "model_key is required").withField("model_key")
		}
		input := catalog.UpsertCanonicalModelInput{
			ModelKey:     in.ModelKey,
			DisplayName:  in.DisplayName,
			Provider:     in.Provider,
			Category:     in.Category,
			Status:       in.Status,
			Capabilities: in.Capabilities,
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("创建标准模型 %s", strings.TrimSpace(in.ModelKey)),
				Diff: []DiffItem{{
					Path:   "model." + strings.TrimSpace(in.ModelKey),
					Before: nil,
					After:  map[string]any{"model_key": in.ModelKey, "display_name": in.DisplayName, "provider": in.Provider, "category": in.Category},
				}},
				AppliedEffects: []string{"model.created"},
				Commit: func(commitCtx context.Context) (any, error) {
					created, err := cat.Create(commitCtx, input)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlCanonicalModelBase(created), nil
				},
			},
		}, nil

	case "update":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var body map[string]any
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		var existingCaps map[string]any
		if len(cur.Model.Capabilities) > 0 {
			_ = json.Unmarshal(cur.Model.Capabilities, &existingCaps)
		}
		next := catalog.UpsertCanonicalModelInput{
			ModelKey:     cur.Model.ModelKey,
			DisplayName:  cur.Model.DisplayName,
			Provider:     cur.Model.Provider,
			Category:     cur.Model.Category,
			Status:       cur.Model.Status,
			Capabilities: existingCaps,
		}
		diffs := []DiffItem{}
		if v, ok := body["model_key"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Model.ModelKey {
			diffs = append(diffs, DiffItem{Path: "model.model_key", Before: cur.Model.ModelKey, After: strings.TrimSpace(v)})
			next.ModelKey = strings.TrimSpace(v)
		}
		if v, ok := body["display_name"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Model.DisplayName {
			diffs = append(diffs, DiffItem{Path: "model.display_name", Before: cur.Model.DisplayName, After: strings.TrimSpace(v)})
			next.DisplayName = strings.TrimSpace(v)
		}
		if v, ok := body["provider"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Model.Provider {
			diffs = append(diffs, DiffItem{Path: "model.provider", Before: cur.Model.Provider, After: strings.TrimSpace(v)})
			next.Provider = strings.TrimSpace(v)
		}
		if v, ok := body["category"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Model.Category {
			diffs = append(diffs, DiffItem{Path: "model.category", Before: cur.Model.Category, After: strings.TrimSpace(v)})
			next.Category = strings.TrimSpace(v)
		}
		if v, ok := body["status"].(string); ok && strings.TrimSpace(v) != "" && v != cur.Model.Status {
			diffs = append(diffs, DiffItem{Path: "model.status", Before: cur.Model.Status, After: strings.TrimSpace(v)})
			next.Status = strings.TrimSpace(v)
		}
		if v, ok := body["capabilities"].(map[string]any); ok {
			diffs = append(diffs, DiffItem{Path: "model.capabilities", Before: existingCaps, After: v})
			next.Capabilities = v
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:           Tier2Managed,
				Summary:        fmt.Sprintf("更新标准模型 %s", cur.Model.ModelKey),
				Diff:           diffs,
				NoOp:           len(diffs) == 0,
				AppliedEffects: []string{"model.updated"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := cat.Update(commitCtx, cur.Model.ID, next)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlCanonicalModelBase(updated), nil
				},
			},
		}, nil

	case "delete":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		if cur.Model.SiteModelCount > 0 {
			return callResult{}, newCallError(http.StatusConflict, ErrCodeConflict,
				fmt.Sprintf("标准模型 %s 仍关联 %d 条站点模型，请先改绑或使用 `xlyra model merge` 合并", cur.Model.ModelKey, cur.Model.SiteModelCount))
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier3HighRisk,
				Summary: fmt.Sprintf("归档空标准模型 %s (%s)", cur.Model.ModelKey, cur.Model.ID),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("model.%s.status", cur.Model.ModelKey),
					Before: cur.Model.Status,
					After:  "archived",
				}},
				NoOp:           cur.Model.Status == "archived",
				AppliedEffects: []string{"model.archived"},
				Commit: func(commitCtx context.Context) (any, error) {
					archived, err := cat.Archive(commitCtx, cur.Model.ID)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlCanonicalModelBase(archived), nil
				},
			},
		}, nil

	case "merge":
		src, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var body struct {
			TargetModelID string `json:"target_model_id"`
			Target        string `json:"target"`
		}
		if req.Payload != nil {
			if ce := decodePayloadInto(req.Payload, &body); ce != nil {
				return callResult{}, ce
			}
		}
		targetRef := strings.TrimSpace(req.SubTarget)
		if targetRef == "" {
			targetRef = strings.TrimSpace(body.TargetModelID)
		}
		if targetRef == "" {
			targetRef = strings.TrimSpace(body.Target)
		}
		dst, ce := e.resolveCanonicalModelItem(ctx, targetRef)
		if ce != nil {
			return callResult{}, ce
		}
		if src.Model.ID == dst.Model.ID {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "source and target canonical models must be different")
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier3HighRisk,
				Summary: fmt.Sprintf("合并标准模型 %s -> %s（迁移 %d 条站点模型及 %d 个别名并归档源模型）", src.Model.ModelKey, dst.Model.ModelKey, src.Model.SiteModelCount, len(src.Aliases)),
				Diff: []DiffItem{
					{Path: fmt.Sprintf("model.%s.status", src.Model.ModelKey), Before: src.Model.Status, After: "archived"},
					{Path: fmt.Sprintf("model.%s.aliases", dst.Model.ModelKey), Before: nil, After: src.Model.ModelKey},
					{Path: fmt.Sprintf("model.%s.site_models", dst.Model.ModelKey), Before: dst.Model.SiteModelCount, After: dst.Model.SiteModelCount + src.Model.SiteModelCount},
				},
				AppliedEffects: []string{"model.merged"},
				Commit: func(commitCtx context.Context) (any, error) {
					res, err := cat.Merge(commitCtx, src.Model.ID, dst.Model.ID)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return map[string]any{
						"source_model":      ctlCanonicalModelBase(res.Source),
						"target_model":      ctlCanonicalModelBase(res.Target),
						"moved_site_models": res.MovedSiteModels,
						"moved_aliases":     res.MovedAliases,
					}, nil
				},
			},
		}, nil

	case "alias-add":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var body struct {
			Alias string `json:"alias"`
		}
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		alias := strings.TrimSpace(body.Alias)
		if alias == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "alias is required").withField("alias")
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("为标准模型 %s 添加别名 %s", cur.Model.ModelKey, alias),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("model.%s.aliases.%s", cur.Model.ModelKey, alias),
					Before: nil,
					After:  alias,
				}},
				AppliedEffects: []string{"model.alias_added"},
				Commit: func(commitCtx context.Context) (any, error) {
					created, err := cat.AddAlias(commitCtx, cur.Model.ID, alias)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlAliasPayload(created), nil
				},
			},
		}, nil

	case "alias-delete":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		aliasTarget := strings.TrimSpace(req.SubTarget)
		if aliasTarget == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "alias id or value is required").withField("sub_target")
		}
		var matched *store.CanonicalModelAlias
		for i := range cur.Aliases {
			a := cur.Aliases[i]
			if a.ID.String() == aliasTarget || strings.EqualFold(a.Alias, aliasTarget) || strings.EqualFold(a.NormalizedAlias, catalog.NormalizeModelKey(aliasTarget)) {
				matched = &a
				break
			}
		}
		if matched == nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("alias %q not found on model %s", aliasTarget, cur.Model.ModelKey))
		}
		aliasID := matched.ID
		aliasText := matched.Alias
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("删除标准模型 %s 的别名 %s", cur.Model.ModelKey, aliasText),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("model.%s.aliases.%s", cur.Model.ModelKey, aliasText),
					Before: aliasText,
					After:  nil,
				}},
				AppliedEffects: []string{"model.alias_deleted"},
				Commit: func(commitCtx context.Context) (any, error) {
					if err := cat.DeleteAlias(commitCtx, cur.Model.ID, aliasID); err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return map[string]any{"deleted": true, "model_id": cur.Model.ID.String(), "alias_id": aliasID.String(), "alias": aliasText}, nil
				},
			},
		}, nil

	case "pricing-set":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var body struct {
			InputPrice        *float64 `json:"input_price"`
			OutputPrice       *float64 `json:"output_price"`
			CacheReadRatio    *float64 `json:"cache_read_ratio"`
			CacheWriteRatio   *float64 `json:"cache_write_ratio"`
			CacheWrite1hRatio *float64 `json:"cache_write_1h_ratio"`
		}
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		if body.InputPrice == nil || body.OutputPrice == nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "input_price and output_price are required")
		}
		beforePricing := map[string]any{
			"input_price":  nullFloatVal(cur.Model.InputPrice),
			"output_price": nullFloatVal(cur.Model.OutputPrice),
		}
		afterPricing := map[string]any{
			"input_price":  *body.InputPrice,
			"output_price": *body.OutputPrice,
		}
		if body.CacheReadRatio != nil {
			beforePricing["cache_read_ratio"] = nullFloatVal(cur.Model.CacheReadRatio)
			afterPricing["cache_read_ratio"] = *body.CacheReadRatio
		}
		if body.CacheWriteRatio != nil {
			beforePricing["cache_write_ratio"] = nullFloatVal(cur.Model.CacheWriteRatio)
			afterPricing["cache_write_ratio"] = *body.CacheWriteRatio
		}
		if body.CacheWrite1hRatio != nil {
			beforePricing["cache_write_1h_ratio"] = nullFloatVal(cur.Model.CacheWrite1hRatio)
			afterPricing["cache_write_1h_ratio"] = *body.CacheWrite1hRatio
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("设置标准模型 %s 的参考定价", cur.Model.ModelKey),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("model.%s.pricing", cur.Model.ModelKey),
					Before: beforePricing,
					After:  afterPricing,
				}},
				AppliedEffects: []string{"model.pricing_updated"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := cat.UpdatePricing(commitCtx, cur.Model.ID, catalog.UpdateCanonicalPricingInput{
						InputPrice:        body.InputPrice,
						OutputPrice:       body.OutputPrice,
						CacheReadRatio:    body.CacheReadRatio,
						CacheWriteRatio:   body.CacheWriteRatio,
						CacheWrite1hRatio: body.CacheWrite1hRatio,
					})
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlCanonicalModelBase(updated), nil
				},
			},
		}, nil

	case "pricing-reset":
		cur, ce := e.resolveCanonicalModelItemExact(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("重置标准模型 %s 的手动参考定价", cur.Model.ModelKey),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("model.%s.pricing_source", cur.Model.ModelKey),
					Before: cur.Model.PricingSource,
					After:  "none",
				}},
				AppliedEffects: []string{"model.pricing_reset"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := cat.UpdatePricing(commitCtx, cur.Model.ID, catalog.UpdateCanonicalPricingInput{Reset: true})
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return ctlCanonicalModelBase(updated), nil
				},
			},
		}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown model action")
}

// --- site-model ---

func (e *Engine) handleSiteModel(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	sites := e.Deps().Sites
	if sites == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
	}
	switch action {
	case "list":
		siteFilterRaw := strings.TrimSpace(stringParam(req.Params, "site"))
		if siteFilterRaw == "" {
			siteFilterRaw = strings.TrimSpace(req.Target)
		}
		canonicalFilterRaw := strings.TrimSpace(stringParam(req.Params, "canonical"))
		if canonicalFilterRaw == "" {
			canonicalFilterRaw = strings.TrimSpace(stringParam(req.Params, "canonical_model"))
		}
		_, hasEnabledFilter := req.Params["enabled"]
		enabledFilter := boolParam(req.Params, "enabled", true)
		q := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "search")))
		if q == "" {
			q = strings.ToLower(strings.TrimSpace(stringParam(req.Params, "q")))
		}

		// Resolve optional site filter.
		var filterSiteID uuid.UUID
		siteMap := map[uuid.UUID]store.Site{}
		if siteFilterRaw != "" {
			resolvedSite, ce := e.resolveSite(ctx, siteFilterRaw)
			if ce != nil {
				return callResult{}, ce
			}
			filterSiteID = resolvedSite.ID
			siteMap[resolvedSite.ID] = resolvedSite
		} else {
			allSites, err := sites.List(ctx)
			if err != nil {
				return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
			}
			for _, s := range allSites {
				siteMap[s.ID] = s
			}
		}

		// Load canonical lookup map for enrichment and --canonical filtering.
		canonicalByID := map[uuid.UUID]store.CanonicalModel{}
		if cat := e.Deps().Catalog; cat != nil {
			if catItems, err := cat.List(ctx); err == nil {
				for _, it := range catItems {
					canonicalByID[it.Model.ID] = it.Model.CanonicalModel
				}
			}
		}
		filterUnmatched := false
		var filterCanonicalID uuid.UUID
		if canonicalFilterRaw != "" {
			if strings.EqualFold(canonicalFilterRaw, "none") || strings.EqualFold(canonicalFilterRaw, "unmatched") {
				filterUnmatched = true
			} else {
				resolvedModel, ce := e.resolveCanonicalModelItem(ctx, canonicalFilterRaw)
				if ce != nil {
					return callResult{}, ce
				}
				filterCanonicalID = resolvedModel.Model.ID
			}
		}

		var rawModels []store.SiteModel
		if filterSiteID != uuid.Nil {
			models, err := sites.ListModels(ctx, filterSiteID)
			if err != nil {
				return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
			}
			rawModels = models
		} else {
			bySite, err := sites.ListAllModelsBySite(ctx)
			if err != nil {
				return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
			}
			for siteID, models := range bySite {
				if _, ok := siteMap[siteID]; !ok {
					continue
				}
				rawModels = append(rawModels, models...)
			}
		}

		items := make([]map[string]any, 0, len(rawModels))
		for _, m := range rawModels {
			if filterUnmatched && m.CanonicalID.Valid {
				continue
			}
			if filterCanonicalID != uuid.Nil && (!m.CanonicalID.Valid || m.CanonicalID.UUID != filterCanonicalID) {
				continue
			}
			isEnabled := strings.EqualFold(m.Status, "active")
			if hasEnabledFilter && isEnabled != enabledFilter {
				continue
			}
			s := siteMap[m.SiteID]
			canonKey := ""
			var canonID any
			if m.CanonicalID.Valid {
				canonID = m.CanonicalID.UUID.String()
				if cm, ok := canonicalByID[m.CanonicalID.UUID]; ok {
					canonKey = cm.ModelKey
				}
			}
			if q != "" {
				haystack := strings.ToLower(m.UpstreamName + " " + m.DisplayName + " " + s.Name + " " + s.Slug + " " + canonKey)
				if !strings.Contains(haystack, q) {
					continue
				}
			}
			items = append(items, map[string]any{
				"id":                  m.ID.String(),
				"site_id":             m.SiteID.String(),
				"site_name":           s.Name,
				"site_slug":           s.Slug,
				"site_enabled":        s.Enabled,
				"canonical_model_id":  canonID,
				"canonical_model_key": canonKey,
				"upstream_model_name": m.UpstreamName,
				"display_name":        m.DisplayName,
				"status":              m.Status,
				"enabled":             isEnabled,
				"match_source":        m.MatchSource,
				"match_confidence":    m.MatchConfidence,
				"updated_at":          m.UpdatedAt,
			})
		}
		sort.SliceStable(items, func(i, j int) bool {
			si, _ := items[i]["site_name"].(string)
			sj, _ := items[j]["site_name"].(string)
			if si != sj {
				return si < sj
			}
			mi, _ := items[i]["upstream_model_name"].(string)
			mj, _ := items[j]["upstream_model_name"].(string)
			return mi < mj
		})

		page, pageSize := parsePagination(req.Params, 50, 200)
		paged, hasMore := slicePage(items, page, pageSize)
		var meta *Meta
		if hasMore {
			meta = &Meta{NextPage: page + 1}
		}
		return callResult{
			Data: map[string]any{
				"items":     paged,
				"count":     len(paged),
				"total":     len(items),
				"page":      page,
				"page_size": pageSize,
				"has_more":  hasMore,
			},
			Meta: meta,
		}, nil

	case "enable", "disable", "update":
		resolvedSite, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		modelID, ce := parseUUIDParam(req.SubTarget, "site_model_id")
		if ce != nil {
			return callResult{}, ce
		}
		want := action == "enable"
		if action == "update" {
			var body struct {
				Enabled *bool `json:"enabled"`
			}
			if ce := decodePayloadInto(req.Payload, &body); ce != nil {
				return callResult{}, ce
			}
			if body.Enabled == nil {
				return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "enabled is required for site-model update").withField("enabled")
			}
			want = *body.Enabled
		}
		models, err := sites.ListModels(ctx, resolvedSite.ID)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		var before *store.SiteModel
		for i := range models {
			if models[i].ID == modelID {
				before = &models[i]
				break
			}
		}
		if before == nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("site_model %s not found in site %s", modelID, resolvedSite.ID))
		}
		wasEnabled := strings.EqualFold(before.Status, "active")
		diffs := []DiffItem{{
			Path:   fmt.Sprintf("site.%s.model.%s.status", resolvedSite.ID, before.UpstreamName),
			Before: before.Status,
			After:  map[bool]string{true: "active", false: "disabled"}[want],
		}}
		return callResult{
			Plan: &mutationPlan{
				Tier:           Tier2Managed,
				Summary:        fmt.Sprintf("%s 站点模型 %s", map[bool]string{true: "启用", false: "停用"}[want], before.UpstreamName),
				Diff:           diffs,
				NoOp:           wasEnabled == want,
				AppliedEffects: []string{"site_model." + map[bool]string{true: "enable", false: "disable"}[want]},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := sites.SetModelEnabled(commitCtx, resolvedSite.ID, modelID, want)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return updated, nil
				},
			},
		}, nil

	case "batch-status":
		resolvedSite, ce := e.resolveSite(ctx, req.Target)
		if ce != nil {
			return callResult{}, ce
		}
		var body struct {
			IDs     []string `json:"ids"`
			Enabled *bool    `json:"enabled"`
		}
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		if len(body.IDs) == 0 || body.Enabled == nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "ids and enabled are required")
		}
		ids := make([]uuid.UUID, 0, len(body.IDs))
		for _, rawID := range body.IDs {
			id, ce := parseUUIDParam(rawID, "ids")
			if ce != nil {
				return callResult{}, ce
			}
			ids = append(ids, id)
		}
		want := *body.Enabled
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("批量%s站点 %s 的 %d 条站点模型", map[bool]string{true: "启用", false: "停用"}[want], resolvedSite.Name, len(ids)),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("site.%s.models.batch_status", resolvedSite.ID),
					Before: len(ids),
					After:  map[bool]string{true: "active", false: "disabled"}[want],
				}},
				AppliedEffects: []string{"site_model.batch_status"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := sites.SetModelsEnabled(commitCtx, resolvedSite.ID, ids, want)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					return map[string]any{"items": updated, "count": len(updated)}, nil
				},
			},
		}, nil

	case "bind-canonical":
		cat := e.Deps().Catalog
		if cat == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "catalog service is unavailable")
		}
		siteModelID, ce := parseUUIDParam(req.Target, "site_model_id")
		if ce != nil {
			return callResult{}, ce
		}
		var body struct {
			CanonicalModelID *string `json:"canonical_model_id"`
		}
		if req.Payload != nil {
			if ce := decodePayloadInto(req.Payload, &body); ce != nil {
				return callResult{}, ce
			}
		}
		var canonicalID *uuid.UUID
		afterLabel := "unmatched"
		if body.CanonicalModelID != nil && strings.TrimSpace(*body.CanonicalModelID) != "" && !strings.EqualFold(strings.TrimSpace(*body.CanonicalModelID), "none") {
			resolvedModel, ce := e.resolveCanonicalModelItem(ctx, *body.CanonicalModelID)
			if ce != nil {
				return callResult{}, ce
			}
			id := resolvedModel.Model.ID
			canonicalID = &id
			afterLabel = resolvedModel.Model.ModelKey + " (" + id.String() + ")"
		}

		var beforeVal any
		beforeModelName := siteModelID.String()
		if db := e.Deps().DB; db != nil {
			cur, err := store.NewSiteModelRepository(db.DB()).GetByID(ctx, siteModelID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, fmt.Sprintf("site_model %s not found", siteModelID))
				}
				return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
			}
			beforeModelName = cur.UpstreamName
			if cur.CanonicalID.Valid {
				beforeVal = cur.CanonicalID.UUID.String()
			}
		}
		noOp := false
		if canonicalID == nil && beforeVal == nil {
			noOp = true
		} else if canonicalID != nil && beforeVal == canonicalID.String() {
			noOp = true
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("改绑站点模型 %s -> %s", beforeModelName, afterLabel),
				Diff: []DiffItem{{
					Path:   fmt.Sprintf("site_model.%s.canonical_model_id", siteModelID),
					Before: beforeVal,
					After:  afterLabel,
				}},
				NoOp:           noOp,
				AppliedEffects: []string{"site_model.bind_canonical"},
				Commit: func(commitCtx context.Context) (any, error) {
					updated, err := cat.BindSiteModel(commitCtx, siteModelID, canonicalID)
					if err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
					var boundID any
					if updated.CanonicalID.Valid {
						boundID = updated.CanonicalID.UUID.String()
					}
					return map[string]any{
						"id":                 updated.ID.String(),
						"site_id":            updated.SiteID.String(),
						"upstream_model_name": updated.UpstreamName,
						"canonical_model_id": boundID,
						"match_source":       updated.MatchSource,
						"match_confidence":   updated.MatchConfidence,
						"status":             updated.Status,
					}, nil
				},
			},
		}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown site-model action")
}

// --- route & cooldown ---

func (e *Engine) handleRoute(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	routerSvc := e.Deps().Router
	if routerSvc == nil && action != "traces" {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "router service is unavailable")
	}
	switch action {
	case "overview", "list":
		windowHours := 24
		if h, ok := toInt(req.Params["window_hours"]); ok && h > 0 && h <= 24*30 {
			windowHours = h
		} else if h, ok := toInt(req.Params["hours"]); ok && h > 0 && h <= 24*30 {
			windowHours = h
		}
		rows, err := routerSvc.Overview(ctx, time.Now().Add(-time.Duration(windowHours)*time.Hour))
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		// Attach unified bound/active site_model stats from catalog so route overview
		// and model catalog report clearly labeled, consistent numbers.
		boundByID := map[uuid.UUID]store.CanonicalModelWithStats{}
		if cat := e.Deps().Catalog; cat != nil {
			if models, err := cat.List(ctx); err == nil {
				for _, m := range models {
					boundByID[m.Model.ID] = m.Model
				}
			}
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			entry := map[string]any{
				"canonical_model_id":       row.CanonicalModelID.String(),
				"model_key":                row.ModelKey,
				"display_name":             row.DisplayName,
				"status":                   row.Status,
				"site_count":               row.SiteCount,
				"site_model_count":         row.SiteModelCount,
				"configured_channel_count": row.SiteModelCount,
				"eligible_channel_count":   row.EligibleCount,
				"cooldown_channel_count":   row.CooldownCount,
				"request_count_24h":        row.RequestCount24h,
				"success_count_24h":        row.SuccessCount24h,
			}
			if b, ok := boundByID[row.CanonicalModelID]; ok {
				entry["bound_site_count"] = b.SiteCount
				entry["bound_site_model_count"] = b.SiteModelCount
				entry["active_site_count"] = b.ActiveSiteCount
				entry["active_site_model_count"] = b.ActiveSiteModelCount
			}
			items = append(items, entry)
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items), "window_hours": windowHours}}, nil

	case "candidates", "get", "test":
		modelKey := strings.TrimSpace(req.Target)
		if modelKey == "" {
			modelKey = strings.TrimSpace(stringParam(req.Params, "model"))
		}
		if modelKey == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				"model key is required for route inspection").withField("target")
		}
		limit := 0
		if v, ok := toInt(req.Params["limit"]); ok && v > 0 {
			limit = v
		}
		debug := boolParam(req.Params, "debug", action == "test")
		// Query full candidate set first so routable counts are never skewed by --limit.
		candidates, err := routerSvc.Candidates(ctx, routeengine.CandidateQuery{
			ModelKey:     modelKey,
			EndpointType: stringParam(req.Params, "endpoint_type"),
			Debug:        debug,
		})
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		routableSites := map[uuid.UUID]struct{}{}
		for _, c := range candidates.Items {
			routableSites[c.Site.ID] = struct{}{}
		}
		returnedItems := candidates.Items
		if limit > 0 && len(returnedItems) > limit {
			returnedItems = returnedItems[:limit]
		}
		out := map[string]any{
			"canonical_model":          ctlCanonicalModelBase(candidates.CanonicalModel),
			"items":                    returnedItems,
			"count":                    len(returnedItems),
			"routable_candidate_count": len(candidates.Items),
			"routable_site_count":      len(routableSites),
		}
		if cat := e.Deps().Catalog; cat != nil {
			if models, err := cat.List(ctx); err == nil {
				for _, m := range models {
					if m.Model.ID == candidates.CanonicalModel.ID {
						out["bound_site_count"] = m.Model.SiteCount
						out["bound_site_model_count"] = m.Model.SiteModelCount
						out["active_site_count"] = m.Model.ActiveSiteCount
						out["active_site_model_count"] = m.Model.ActiveSiteModelCount
						break
					}
				}
			}
		}
		return callResult{Data: out}, nil

	case "select":
		modelKey := strings.TrimSpace(stringParam(req.Params, "model"))
		if modelKey == "" {
			modelKey = strings.TrimSpace(req.Target)
		}
		if modelKey == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "model is required").withField("model")
		}
		query := routeengine.CandidateQuery{
			ModelKey:     modelKey,
			EndpointType: stringParam(req.Params, "endpoint_type"),
			Debug:        boolParam(req.Params, "debug", false),
		}
		if rawKey := strings.TrimSpace(stringParam(req.Params, "api_key")); rawKey != "" {
			if e.Deps().Auth == nil {
				return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "auth service is unavailable")
			}
			keyID, ce := e.resolveAPIKeyID(ctx, rawKey)
			if ce != nil {
				return callResult{}, ce
			}
			access, err := e.Deps().Auth.ResolveAPIKeyRouteAccess(ctx, keyID, modelKey)
			if err != nil {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodeInvalidArgument, err.Error())
			}
			query.ModelKey = access.ModelKey
			query.AllowedSiteIDs = access.AllowedSiteIDs
			query.AllowedSiteModelIDs = access.AllowedSiteModelIDs
		}
		plan, err := routerSvc.Plan(ctx, query)
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		return callResult{Data: map[string]any{
			"canonical_model": ctlCanonicalModelBase(plan.CanonicalModel),
			"selected":        plan.Selected,
			"failover":        plan.Failover,
			"failover_count":  len(plan.Failover),
		}}, nil

	case "failover":
		modelKey := strings.TrimSpace(stringParam(req.Params, "model"))
		if modelKey == "" {
			modelKey = strings.TrimSpace(req.Target)
		}
		if modelKey == "" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "model is required").withField("model")
		}
		rawFailed := stringSliceParam(req.Params, "failed_site_ids")
		if len(rawFailed) == 0 {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "failed_site_ids is required").withField("failed_site_ids")
		}
		excludedSites := make([]uuid.UUID, 0, len(rawFailed))
		for _, item := range rawFailed {
			resolvedSite, ce := e.resolveSite(ctx, item)
			if ce != nil {
				return callResult{}, ce
			}
			excludedSites = append(excludedSites, resolvedSite.ID)
		}
		plan, err := routerSvc.Plan(ctx, routeengine.CandidateQuery{
			ModelKey:       modelKey,
			EndpointType:   stringParam(req.Params, "endpoint_type"),
			ExcludeSiteIDs: excludedSites,
			Debug:          boolParam(req.Params, "debug", false),
		})
		if err != nil {
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, err.Error())
		}
		excludedStrs := make([]string, 0, len(excludedSites))
		for _, id := range excludedSites {
			excludedStrs = append(excludedStrs, id.String())
		}
		return callResult{Data: map[string]any{
			"canonical_model":   ctlCanonicalModelBase(plan.CanonicalModel),
			"selected":          plan.Selected,
			"failover":          plan.Failover,
			"failover_count":    len(plan.Failover),
			"excluded_site_ids": excludedStrs,
		}}, nil

	case "traces":
		usageSvc := e.Deps().Usage
		if usageSvc == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "usage service is unavailable")
		}
		limit := 20
		if v, ok := toInt(req.Params["limit"]); ok && v > 0 && v <= 100 {
			limit = v
		}
		rows, err := usageSvc.ListRequests(ctx, usage.RequestQuery{
			Limit:    limit,
			ModelKey: strings.TrimSpace(stringParam(req.Params, "model")),
		})
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]any{
				"id":              row.ID.String(),
				"request_id":      row.RequestID,
				"endpoint":        row.Endpoint,
				"status_code":     row.StatusCode,
				"success":         row.Success,
				"error_type":      nullStringVal(row.ErrorType),
				"latency_ms":      nullInt64Val(row.LatencyMS),
				"site_name":       nullStringVal(row.SiteName),
				"canonical_model": nullStringVal(row.CanonicalModelKey),
				"upstream_model":  nullStringVal(row.SiteModelUpstreamName),
				"created_at":      row.CreatedAt,
			})
		}
		return callResult{Data: map[string]any{"items": items, "count": len(items)}}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown route action")
}

func (e *Engine) handleCooldown(ctx context.Context, action string, req CallRequest) (callResult, *callError) {
	routerSvc := e.Deps().Router
	if routerSvc == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "router service is unavailable")
	}
	switch action {
	case "list":
		rows, err := routerSvc.ActiveCooldowns(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		return callResult{Data: map[string]any{"items": rows, "count": len(rows)}}, nil
	case "get":
		id, ce := parseUUIDParam(req.Target, "cooldown_id")
		if ce != nil {
			return callResult{}, ce
		}
		rows, err := routerSvc.ActiveCooldowns(ctx)
		if err != nil {
			return callResult{}, newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error())
		}
		for _, row := range rows {
			if row.ID == id {
				return callResult{Data: row}, nil
			}
		}
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "cooldown not found")

	case "create", "activate":
		var body struct {
			Site             string `json:"site"`
			SiteID           string `json:"site_id"`
			SiteModelID      string `json:"site_model_id"`
			SiteCredentialID string `json:"site_credential_id"`
			Scope            string `json:"scope"`
			Source           string `json:"source"`
			Reason           string `json:"reason"`
			DurationSeconds  int    `json:"duration_seconds"`
		}
		if ce := decodePayloadInto(req.Payload, &body); ce != nil {
			return callResult{}, ce
		}
		siteRef := strings.TrimSpace(body.SiteID)
		if siteRef == "" {
			siteRef = strings.TrimSpace(body.Site)
		}
		if siteRef == "" {
			siteRef = strings.TrimSpace(req.Target)
		}
		siteID, ce := e.resolveSiteIDOrUUID(ctx, siteRef)
		if ce != nil {
			return callResult{}, ce
		}
		if body.DurationSeconds <= 0 {
			body.DurationSeconds = 300
		}
		if strings.TrimSpace(body.Source) == "" {
			body.Source = "agent_cli"
		}
		if strings.TrimSpace(body.Reason) == "" {
			body.Reason = "manual cooldown triggered via xlyra"
		}
		in := routeengine.CooldownInput{
			SiteID:   siteID,
			Scope:    strings.TrimSpace(body.Scope),
			Source:   strings.TrimSpace(body.Source),
			Reason:   strings.TrimSpace(body.Reason),
			Duration: time.Duration(body.DurationSeconds) * time.Second,
		}
		if body.SiteModelID != "" {
			id, ce := parseUUIDParam(body.SiteModelID, "site_model_id")
			if ce != nil {
				return callResult{}, ce
			}
			in.SiteModelID = &id
		}
		if body.SiteCredentialID != "" {
			id, ce := parseUUIDParam(body.SiteCredentialID, "site_credential_id")
			if ce != nil {
				return callResult{}, ce
			}
			in.SiteCredentialID = &id
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("激活站点 %s 冷却 (%ds)", siteID, body.DurationSeconds),
				Diff: []DiffItem{{
					Path:   "cooldown." + siteID.String(),
					Before: nil,
					After:  map[string]any{"duration_seconds": body.DurationSeconds, "reason": in.Reason, "source": in.Source},
				}},
				AppliedEffects: []string{"cooldown.activated"},
				Commit: func(commitCtx context.Context) (any, error) {
					return routerSvc.ActivateCooldown(commitCtx, in)
				},
			},
		}, nil

	case "clear":
		var body struct {
			Site             string `json:"site"`
			SiteID           string `json:"site_id"`
			SiteModelID      string `json:"site_model_id"`
			SiteCredentialID string `json:"site_credential_id"`
			Source           string `json:"source"`
		}
		if req.Payload != nil {
			if ce := decodePayloadInto(req.Payload, &body); ce != nil {
				return callResult{}, ce
			}
		}
		siteRef := strings.TrimSpace(req.Target)
		if siteRef == "" {
			siteRef = strings.TrimSpace(body.SiteID)
		}
		if siteRef == "" {
			siteRef = strings.TrimSpace(body.Site)
		}
		siteID, ce := e.resolveSiteIDOrUUID(ctx, siteRef)
		if ce != nil {
			return callResult{}, ce
		}
		var siteModelID, credID *uuid.UUID
		smRaw := strings.TrimSpace(body.SiteModelID)
		if smRaw == "" {
			smRaw = strings.TrimSpace(stringParam(req.Params, "site_model_id"))
		}
		if smRaw != "" {
			id, ce := parseUUIDParam(smRaw, "site_model_id")
			if ce != nil {
				return callResult{}, ce
			}
			siteModelID = &id
		}
		credRaw := strings.TrimSpace(body.SiteCredentialID)
		if credRaw == "" {
			credRaw = strings.TrimSpace(stringParam(req.Params, "site_credential_id"))
		}
		if credRaw != "" {
			id, ce := parseUUIDParam(credRaw, "site_credential_id")
			if ce != nil {
				return callResult{}, ce
			}
			credID = &id
		}
		source := strings.TrimSpace(body.Source)
		if source == "" {
			source = strings.TrimSpace(stringParam(req.Params, "source"))
		}
		return callResult{
			Plan: &mutationPlan{
				Tier:    Tier2Managed,
				Summary: fmt.Sprintf("清除站点 %s 冷却状态", siteID),
				Diff: []DiffItem{{
					Path:   "cooldown." + siteID.String(),
					Before: "active",
					After:  "cleared",
				}},
				AppliedEffects: []string{"cooldown.cleared"},
				Commit: func(commitCtx context.Context) (any, error) {
					if err := routerSvc.ClearCooldown(commitCtx, siteID, siteModelID, credID, source); err != nil {
						return nil, err
					}
					return map[string]any{"cleared": true, "site_id": siteID}, nil
				},
			},
		}, nil
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown cooldown action")
}

func (e *Engine) resolveSiteIDOrUUID(ctx context.Context, raw string) (uuid.UUID, *callError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuid.Nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "site_id is required").withField("site_id")
	}
	if e.Deps().Sites != nil {
		resolved, ce := e.resolveSite(ctx, trimmed)
		if ce != nil {
			return uuid.Nil, ce
		}
		return resolved.ID, nil
	}
	if id, err := uuid.Parse(trimmed); err == nil && id != uuid.Nil {
		return id, nil
	}
	return uuid.Nil, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "site service is unavailable")
}

// --- formatting & pagination helpers ---

func ctlCanonicalModelBase(m store.CanonicalModel) map[string]any {
	return map[string]any{
		"id":             m.ID.String(),
		"model_key":      m.ModelKey,
		"display_name":   m.DisplayName,
		"provider":       m.Provider,
		"category":       m.Category,
		"status":         m.Status,
		"pricing_source": m.PricingSource,
		"input_price":    nullFloatVal(m.InputPrice),
		"output_price":   nullFloatVal(m.OutputPrice),
		"updated_at":     m.UpdatedAt,
	}
}

func ctlCanonicalModelSummary(it catalog.CanonicalModelItem) map[string]any {
	out := ctlCanonicalModelBase(it.Model.CanonicalModel)
	out["site_count"] = it.Model.SiteCount
	out["site_model_count"] = it.Model.SiteModelCount
	out["active_site_count"] = it.Model.ActiveSiteCount
	out["active_site_model_count"] = it.Model.ActiveSiteModelCount
	aliases := make([]map[string]any, 0, len(it.Aliases))
	for _, a := range it.Aliases {
		aliases = append(aliases, ctlAliasPayload(a))
	}
	out["aliases"] = aliases
	return out
}

func ctlAliasPayload(a store.CanonicalModelAlias) map[string]any {
	return map[string]any{
		"id":                 a.ID.String(),
		"canonical_model_id": a.CanonicalModelID.String(),
		"alias":              a.Alias,
		"normalized_alias":   a.NormalizedAlias,
		"source":             a.Source,
	}
}

func ctlGroupedMatrixItems(rows []store.CanonicalModelMatrixRow) []map[string]any {
	items := make([]map[string]any, 0)
	indexBySiteModelID := map[uuid.UUID]int{}
	for _, row := range rows {
		idx, ok := indexBySiteModelID[row.SiteModelID]
		if !ok {
			entry := map[string]any{
				"site_id":                 row.SiteID.String(),
				"site_name":               row.SiteName,
				"site_slug":               row.SiteSlug,
				"site_type":               row.SiteType,
				"site_enabled":            row.SiteEnabled,
				"site_model_id":           row.SiteModelID.String(),
				"upstream_model_name":     row.UpstreamModelName,
				"display_name":            row.DisplayName,
				"model_status":            row.ModelStatus,
				"match_source":            row.MatchSource,
				"match_confidence":        row.MatchConfidence,
				"api_key_count":           row.APIKeyCount,
				"available_api_key_count": row.AvailableAPIKeyCount,
				"pricing":                 []map[string]any{},
			}
			items = append(items, entry)
			idx = len(items) - 1
			indexBySiteModelID[row.SiteModelID] = idx
		}
		if row.GroupName.Valid {
			pricing := map[string]any{
				"group_name":        row.GroupName.String,
				"currency":          nullStringVal(row.Currency),
				"input_value":       nullFloatVal(row.InputValue),
				"output_value":      nullFloatVal(row.OutputValue),
				"per_request_value": nullFloatVal(row.PerRequestValue),
				"billing_type":      nullStringVal(row.BillingType),
			}
			items[idx]["pricing"] = append(items[idx]["pricing"].([]map[string]any), pricing)
		}
	}
	return items
}

func modelItemMatchesQuery(it catalog.CanonicalModelItem, q string) bool {
	if strings.Contains(strings.ToLower(it.Model.ModelKey+" "+it.Model.DisplayName+" "+it.Model.Provider), q) {
		return true
	}
	for _, a := range it.Aliases {
		if strings.Contains(strings.ToLower(a.Alias+" "+a.NormalizedAlias), q) {
			return true
		}
	}
	return false
}

func parsePagination(params map[string]any, defaultSize, maxSize int) (int, int) {
	page := 1
	if v, ok := toInt(params["page"]); ok && v > 0 {
		page = v
	}
	pageSize := defaultSize
	if v, ok := toInt(params["page_size"]); ok && v > 0 {
		pageSize = v
	} else if v, ok := toInt(params["limit"]); ok && v > 0 {
		pageSize = v
	}
	if pageSize > maxSize {
		pageSize = maxSize
	}
	return page, pageSize
}

func slicePage[T any](items []T, page, pageSize int) ([]T, bool) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		return items, false
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []T{}, false
	}
	end := start + pageSize
	hasMore := false
	if end < len(items) {
		hasMore = true
	} else {
		end = len(items)
	}
	return items[start:end], hasMore
}

func stringSliceParam(params map[string]any, key string) []string {
	if params == nil {
		return nil
	}
	switch v := params[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return nil
}

func nullFloatVal(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func nullStringVal(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}

func nullInt64Val(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
