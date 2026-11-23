package agentctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"xlyra/server/internal/agentproxy"
	"xlyra/server/internal/config"
	"xlyra/server/internal/httpclient"
	"xlyra/server/internal/ratelimit"
	"xlyra/server/internal/store"
)

type configNamespaceMeta struct {
	Namespace string   `json:"namespace"`
	Writable  bool     `json:"writable"`
	Tier      int      `json:"tier"`
	Summary   string   `json:"summary"`
	Notes     string   `json:"notes,omitempty"`
	Keys      []string `json:"keys,omitempty"`
}

type appearanceState struct {
	BackgroundImage        string   `json:"background_image"`
	CustomBackgroundImages []string `json:"custom_background_images"`
	HiddenMenuItems        []string `json:"hidden_menu_items"`
	HomePage               string   `json:"home_page"`
}

func defaultAppearanceState() appearanceState {
	return appearanceState{
		BackgroundImage:        "",
		CustomBackgroundImages: []string{},
		HiddenMenuItems:        []string{},
		HomePage:               "dashboard",
	}
}

func (a appearanceState) toMap() map[string]any {
	custom := slices.Clone(a.CustomBackgroundImages)
	if custom == nil {
		custom = []string{}
	}
	hidden := slices.Clone(a.HiddenMenuItems)
	if hidden == nil {
		hidden = []string{}
	}
	return map[string]any{
		"background_image":         a.BackgroundImage,
		"custom_background_images": custom,
		"hidden_menu_items":        hidden,
		"home_page":                a.HomePage,
	}
}

func (e *Engine) dispatchConfigAndMeta(ctx context.Context, caller Caller, spec OpSpec, req CallRequest) (callResult, *callError) {
	switch spec.Resource {
	case "status":
		return e.handleStatusGet(ctx, caller)
	case "schema":
		switch spec.Action {
		case "list":
			return e.handleSchemaList(caller, req)
		case "get":
			return e.handleSchemaGet(caller, req)
		}
	case "config":
		switch spec.Action {
		case "list":
			return e.handleConfigList()
		case "describe":
			return e.handleConfigDescribe(req)
		case "get":
			return e.handleConfigGet(ctx, req)
		case "set", "patch":
			return e.handleConfigMutate(ctx, caller, spec, req)
		case "reset":
			return e.handleConfigReset(ctx, caller, req)
		}
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound, "unknown operation")
}

func (e *Engine) handleStatusGet(ctx context.Context, caller Caller) (callResult, *callError) {
	deps := e.Deps()
	_, rev := e.buildConfigSnapshot(ctx)

	policyMode := "allow_all_non_tier0"
	if caller.Policy != nil && strings.TrimSpace(caller.Policy.Mode) != "" {
		policyMode = caller.Policy.Mode
	}
	return callResult{
		Data: map[string]any{
			"server_version":     deps.Version,
			"protocol_version":   ProtocolVersion,
			"channel":            caller.Channel,
			"permission_mode":    caller.PermissionMode,
			"capability_policy":  policyMode,
			"config_revision":    rev,
			"database_connected": deps.DB != nil,
			"config_file_loaded": deps.ConfFile != nil,
			"features": map[string]bool{
				"config":        deps.ConfFile != nil,
				"rate_limits":   deps.RateLimits != nil,
				"sites":         deps.Sites != nil,
				"catalog":       deps.Catalog != nil,
				"router":        deps.Router != nil,
				"dashboard":     deps.Dashboard != nil,
				"usage":         deps.Usage != nil,
				"auth":          deps.Auth != nil,
				"capabilities":  e.capsStore() != nil,
				"js_plugins":    deps.JSPlugins != nil,
				"plugin_market": deps.PluginMarket != nil,
				"cloud":         deps.Cloud != nil,
			},
		},
		Meta: &Meta{Tier: Tier1ReadOnly, Revision: rev},
	}, nil
}

func (e *Engine) handleSchemaList(caller Caller, req CallRequest) (callResult, *callError) {
	domainFilter := strings.ToLower(strings.TrimSpace(stringParam(req.Params, "domain")))
	tierFilter := -1
	if rawTier, exists := req.Params["tier"]; exists && rawTier != nil {
		if t, ok := toInt(rawTier); ok {
			tierFilter = t
		}
	}
	implementedOnly := boolParam(req.Params, "implemented_only", false)

	specs := BuiltinOpSpecs()
	items := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		if domainFilter != "" && sp.Domain != domainFilter {
			continue
		}
		if tierFilter >= 0 && sp.Tier != tierFilter {
			continue
		}
		if implementedOnly && !sp.Implemented {
			continue
		}
		allowed, denyReason := true, ""
		if caller.Policy != nil {
			allowed, denyReason = caller.Policy.AllowsOp(sp)
		} else if sp.Tier == Tier0Forbidden {
			allowed, denyReason = false, "operation is Tier 0 forbidden"
		}
		items = append(items, map[string]any{
			"resource":      sp.Resource,
			"action":        sp.Action,
			"domain":        sp.Domain,
			"verb":          sp.Verb,
			"tier":          sp.Tier,
			"implemented":   sp.Implemented,
			"mcp_name":      sp.MCPName,
			"summary":       sp.Summary,
			"policy_allow":  allowed,
			"policy_reason": denyReason,
		})
	}
	return callResult{
		Data: map[string]any{
			"items": items,
			"count": len(items),
		},
	}, nil
}

func (e *Engine) handleSchemaGet(caller Caller, req CallRequest) (callResult, *callError) {
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"schema get requires target (<resource> or <resource>/<action>)").withField("target")
	}
	if target == "list" {
		return e.handleSchemaList(caller, req)
	}
	target = strings.ReplaceAll(target, ".", "/")
	resName, actName, hasAction := strings.Cut(target, "/")

	specs := BuiltinOpSpecs()
	matched := make([]map[string]any, 0, 8)
	for _, sp := range specs {
		if sp.Resource != resName && (hasAction || sp.Domain != resName) {
			continue
		}
		if hasAction && sp.Action != actName {
			continue
		}
		allowed := sp.Tier != Tier0Forbidden
		if caller.Policy != nil {
			allowed, _ = caller.Policy.AllowsOp(sp)
		}
		matched = append(matched, map[string]any{
			"resource":     sp.Resource,
			"action":       sp.Action,
			"domain":       sp.Domain,
			"verb":         sp.Verb,
			"tier":         sp.Tier,
			"implemented":  sp.Implemented,
			"mcp_name":     sp.MCPName,
			"summary":      sp.Summary,
			"description":  sp.Description,
			"input_schema": sp.InputSchema,
			"policy_allow": allowed,
		})
	}
	if len(matched) == 0 {
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("no schema found for %q", req.Target))
	}
	if hasAction {
		return callResult{Data: matched[0]}, nil
	}
	return callResult{
		Data: map[string]any{
			"resource": resName,
			"actions":  matched,
		},
	}, nil
}

func configNamespaces() []configNamespaceMeta {
	return []configNamespaceMeta{
		{
			Namespace: "general",
			Writable:  true,
			Tier:      Tier2Managed,
			Summary:   "系统常规设置（定时任务 cron、IP 白名单、日志、数据清理、缓存、安全会话时长）",
			Notes:     "general.ip_whitelist.* 与 general.security.* 变更提升为 Tier 3",
			Keys: []string{
				"general.tasks.site_refresh_cron",
				"general.tasks.newapi_checkin_cron",
				"general.ip_whitelist.enabled",
				"general.ip_whitelist.entries",
				"general.log.level",
				"general.log.cleanup_enabled",
				"general.log.retention_days",
				"general.data.request_detail_cleanup_enabled",
				"general.data.request_detail_retention_days",
				"general.cache.enabled",
				"general.cache.observation_enabled",
				"general.cache.observation_ttl_minutes",
				"general.cache.observation_history_limit",
				"general.security.session_lifetime_hours",
			},
		},
		{
			Namespace: "portal",
			Writable:  true,
			Tier:      Tier2Managed,
			Summary:   "共享用量门户开关、分页上限与可见维度开关",
			Keys: []string{
				"portal.enabled",
				"portal.show_requests",
				"portal.show_summary",
				"portal.summary_days",
				"portal.request_page_size_max",
				"portal.dimensions.site",
				"portal.dimensions.model",
				"portal.dimensions.tokens",
				"portal.dimensions.cost",
				"portal.dimensions.latency",
				"portal.dimensions.endpoint",
				"portal.dimensions.upstream",
				"portal.dimensions.error",
			},
		},
		{
			Namespace: "appearance",
			Writable:  true,
			Tier:      Tier2Managed,
			Summary:   "控制台外观与首页视图（home_page、hidden_menu_items、background_image）",
			Keys: []string{
				"appearance.home_page",
				"appearance.hidden_menu_items",
				"appearance.background_image",
			},
		},
		{
			Namespace: "rate-limit",
			Writable:  true,
			Tier:      Tier2Managed,
			Summary:   "网关全局 RPM / TPM 限流配置（存储在数据库 gateway_rate_limits 表）",
			Keys: []string{
				"rate-limit.status",
				"rate-limit.rpm_limit",
				"rate-limit.tpm_limit",
			},
		},
		{
			Namespace: "backup.auto",
			Writable:  true,
			Tier:      Tier2Managed,
			Summary:   "自动备份计划、保留份数与非敏感对象存储参数",
			Notes:     "secret_key* 与 backup_passphrase* 为 Tier 0 禁止项，只能通过 Web 控制台设置",
			Keys: []string{
				"backup.auto.enabled",
				"backup.auto.cron",
				"backup.auto.retention_count",
				"backup.auto.sections",
				"backup.auto.storage.endpoint",
				"backup.auto.storage.region",
				"backup.auto.storage.bucket",
				"backup.auto.storage.prefix",
				"backup.auto.storage.access_key",
				"backup.auto.storage.force_path_style",
				"backup.auto.storage.use_ssl",
				"backup.auto.storage.skip_tls_verify",
			},
		},
		{
			Namespace: "network",
			Writable:  false,
			Tier:      Tier1ReadOnly,
			Summary:   "全局网络代理列表（写操作请使用 xlyra proxy 命令）",
		},
		{
			Namespace: "agent.settings",
			Writable:  false,
			Tier:      Tier0Forbidden,
			Summary:   "内置 Agent 运行参数与模型/站点访问白名单（Tier 0 只读，禁止 Agent 自改）",
		},
		{
			Namespace: "agent.capabilities",
			Writable:  false,
			Tier:      Tier0Forbidden,
			Summary:   "内置 Agent 上下文与技能开关配置（Tier 0 只读，禁止 Agent 自改）",
		},
		{
			Namespace: "env",
			Writable:  false,
			Tier:      Tier0Forbidden,
			Summary:   "启动环境变量非敏感快照（Tier 0 只读，已剔除所有 DSN/密钥）",
		},
	}
}

func (e *Engine) handleConfigList() (callResult, *callError) {
	items := configNamespaces()
	return callResult{
		Data: map[string]any{
			"items": items,
			"count": len(items),
		},
	}, nil
}

func (e *Engine) handleConfigDescribe(req CallRequest) (callResult, *callError) {
	target := strings.TrimSpace(req.Target)
	if target == "" || target == "all" {
		return e.handleConfigList()
	}
	norm := normalizeConfigPath(target)
	for _, ns := range configNamespaces() {
		if norm == ns.Namespace || strings.HasPrefix(norm, ns.Namespace+".") {
			return callResult{
				Data: map[string]any{
					"path":      norm,
					"namespace": ns.Namespace,
					"writable":  ns.Writable && !isTier0ConfigPath(norm),
					"tier":      effectiveTierForConfigPath(norm, ns.Tier),
					"summary":   ns.Summary,
					"notes":     ns.Notes,
					"keys":      ns.Keys,
				},
			}, nil
		}
	}
	return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
		fmt.Sprintf("unknown config namespace or path %q", target)).withField("target")
}

func (e *Engine) handleConfigGet(ctx context.Context, req CallRequest) (callResult, *callError) {
	snap, rev := e.buildConfigSnapshot(ctx)
	target := strings.TrimSpace(req.Target)
	if target == "" || target == "all" {
		return callResult{
			Data: snap,
			Meta: &Meta{Tier: Tier1ReadOnly, Revision: rev},
		}, nil
	}
	norm := normalizeConfigPath(target)
	val, writable, tier, found := lookupConfigSnapshotPath(snap, norm)
	if !found {
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("config path %q not found", target)).withField("target")
	}
	return callResult{
		Data: map[string]any{
			"path":     norm,
			"value":    val,
			"writable": writable,
			"tier":     tier,
		},
		Meta: &Meta{Tier: Tier1ReadOnly, Revision: rev},
	}, nil
}

func (e *Engine) handleConfigMutate(ctx context.Context, caller Caller, _ OpSpec, req CallRequest) (callResult, *callError) {
	deps := e.Deps()
	if deps.ConfFile == nil {
		return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal,
			"config file is unavailable")
	}
	mutations, err := flattenMutations(req.Target, req.Payload)
	if err != nil {
		return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
	}
	if len(mutations) == 0 {
		return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"no config mutations provided in payload")
	}

	snap, currentRev := e.buildConfigSnapshot(ctx)
	if strings.TrimSpace(req.Options.IfMatch) != "" && strings.TrimSpace(req.Options.IfMatch) != currentRev {
		return callResult{}, newCallError(http.StatusPreconditionFailed, ErrCodePreconditionFailed,
			fmt.Sprintf("config revision mismatch: got %s, current %s", req.Options.IfMatch, currentRev)).
			withHint("run `xlyra config get` first to read the latest revision")
	}

	// Read writable working copies up front so validation stays atomic across namespaces.
	gen := config.ReadGeneralConfig(deps.ConfFile)
	genMap := config.GeneralConfigToMap(gen)
	genDefault := config.GeneralConfigToMap(config.DefaultGeneralConfig())

	portal := config.ReadPortalConfig(deps.ConfFile)
	portalMap := config.PortalConfigToMap(portal)
	portalDefault := config.PortalConfigToMap(config.DefaultPortalConfig())

	appState := e.readAppearanceState()
	appMap := appState.toMap()
	appDefault := defaultAppearanceState().toMap()

	rlCfg := ratelimit.DefaultConfig()
	if deps.RateLimits != nil {
		if val, rlErr := deps.RateLimits.GetGlobal(ctx); rlErr == nil {
			rlCfg = val
		}
	}
	rlMap := rateLimitToMap(rlCfg)

	backupCfg, _ := config.ReadAutomaticBackupConfig(deps.ConfFile)
	backupSafeMap := config.AutomaticBackupConfigForExport(backupCfg)
	backupDefaultSafe := config.AutomaticBackupConfigForExport(config.DefaultAutomaticBackupConfig())

	touched := map[string]bool{}
	effectiveTier := Tier2Managed
	diffs := make([]DiffItem, 0, len(mutations))

	keys := make([]string, 0, len(mutations))
	for k := range mutations {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	for _, rawKey := range keys {
		val := mutations[rawKey]
		path := normalizeConfigPath(rawKey)
		if isTier0ConfigPath(path) {
			return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
				fmt.Sprintf("config path %q is protected by Tier 0 policy and cannot be modified via agent CLI", path)).
				withTier(Tier0Forbidden).withField(path)
		}
		if strings.HasPrefix(path, "network.") || path == "network" {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				"network proxy settings cannot be edited via config set/patch").
				withField(path).
				withHint("use `xlyra proxy list|create|update|delete` instead")
		}
		if caller.Policy != nil {
			if allowed, reason := caller.Policy.AllowsConfigPath(path); !allowed {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodeCapabilityDenied, reason).
					withField(path)
			}
		}
		pathTier := effectiveTierForConfigPath(path, Tier2Managed)
		if pathTier > effectiveTier {
			effectiveTier = pathTier
		}

		switch {
		case strings.HasPrefix(path, "general."):
			sub := strings.TrimPrefix(path, "general.")
			before, typedVal, ce := applyTypedMapLeaf(genMap, genDefault, sub, val, path)
			if ce != nil {
				return callResult{}, ce
			}
			touched["general"] = true
			if !equalJSONValue(before, typedVal) {
				diffs = append(diffs, DiffItem{Path: path, Before: before, After: typedVal})
			}

		case strings.HasPrefix(path, "portal."):
			sub := strings.TrimPrefix(path, "portal.")
			before, typedVal, ce := applyTypedMapLeaf(portalMap, portalDefault, sub, val, path)
			if ce != nil {
				return callResult{}, ce
			}
			touched["portal"] = true
			if !equalJSONValue(before, typedVal) {
				diffs = append(diffs, DiffItem{Path: path, Before: before, After: typedVal})
			}

		case strings.HasPrefix(path, "appearance."):
			sub := strings.TrimPrefix(path, "appearance.")
			if sub == "custom_background_images" {
				return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
					"uploading binary/gallery background images via CLI is disabled").
					withTier(Tier0Forbidden).withField(path)
			}
			before, typedVal, ce := applyTypedMapLeaf(appMap, appDefault, sub, val, path)
			if ce != nil {
				return callResult{}, ce
			}
			touched["appearance"] = true
			if !equalJSONValue(before, typedVal) {
				diffs = append(diffs, DiffItem{Path: path, Before: before, After: typedVal})
			}

		case strings.HasPrefix(path, "rate-limit."):
			if deps.RateLimits == nil {
				return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal,
					"rate limit service is unavailable (no database connection)")
			}
			sub := strings.TrimPrefix(path, "rate-limit.")
			before, typedVal, ce := applyRateLimitLeaf(rlMap, sub, val, path)
			if ce != nil {
				return callResult{}, ce
			}
			touched["rate-limit"] = true
			if !equalJSONValue(before, typedVal) {
				diffs = append(diffs, DiffItem{Path: path, Before: before, After: typedVal})
			}

		case strings.HasPrefix(path, "backup.auto."):
			sub := strings.TrimPrefix(path, "backup.auto.")
			before, typedVal, ce := applyTypedMapLeaf(backupSafeMap, backupDefaultSafe, sub, val, path)
			if ce != nil {
				return callResult{}, ce
			}
			touched["backup.auto"] = true
			if !equalJSONValue(before, typedVal) {
				diffs = append(diffs, DiffItem{Path: path, Before: before, After: typedVal})
			}

		default:
			return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("unknown or non-writable config path %q", rawKey)).withField(rawKey)
		}
	}

	// Validate all modified domain objects before saving or prompting.
	var nextGen config.GeneralConfig
	if touched["general"] {
		nextGen = config.GeneralConfigFromRaw(genMap)
		if err := config.ValidateGeneralConfig(nextGen); err != nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
		}
	}
	var nextPortal config.PortalConfig
	if touched["portal"] {
		nextPortal = config.PortalConfigFromRaw(portalMap)
		if err := config.ValidatePortalConfig(nextPortal); err != nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
		}
	}
	var nextAppearance appearanceState
	if touched["appearance"] {
		var ce *callError
		nextAppearance, ce = validateAppearanceMap(appMap)
		if ce != nil {
			return callResult{}, ce
		}
	}
	var nextRL ratelimit.ConfigInput
	if touched["rate-limit"] {
		var ce *callError
		nextRL, ce = validateRateLimitMap(rlMap)
		if ce != nil {
			return callResult{}, ce
		}
	}
	var nextBackup config.AutomaticBackupConfig
	if touched["backup.auto"] {
		nextBackup = config.AutomaticBackupConfigFromRaw(backupSafeMap)
		// Preserve encrypted credentials untouched on update.
		nextBackup.Storage.SecretKeyEncrypted = backupCfg.Storage.SecretKeyEncrypted
		nextBackup.Storage.SecretKeyMasked = backupCfg.Storage.SecretKeyMasked
		nextBackup.Storage.BackupPassphraseEncrypted = backupCfg.Storage.BackupPassphraseEncrypted
		nextBackup.Storage.BackupPassphraseMasked = backupCfg.Storage.BackupPassphraseMasked
		if err := config.ValidateAutomaticBackupConfig(nextBackup); err != nil {
			return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
		}
	}

	_ = snap // snapshot read used for revision check above
	effects := make([]string, 0, len(touched))
	for ns := range touched {
		effects = append(effects, "config."+ns+".updated")
	}
	slices.Sort(effects)

	summary := fmt.Sprintf("修改 %d 项系统配置", len(diffs))
	if len(diffs) == 1 {
		summary = fmt.Sprintf("修改配置 %s: %v -> %v", diffs[0].Path, diffs[0].Before, diffs[0].After)
	}

	return callResult{
		Plan: &mutationPlan{
			Tier:           effectiveTier,
			Revision:       currentRev,
			Summary:        summary,
			Diff:           diffs,
			NoOp:           len(diffs) == 0,
			AppliedEffects: effects,
			Commit: func(commitCtx context.Context) (any, error) {
				if touched["general"] {
					if err := deps.ConfFile.Set(config.GeneralConfigPath, config.GeneralConfigToMap(nextGen)); err != nil {
						return nil, err
					}
				}
				if touched["portal"] {
					if err := deps.ConfFile.Set(config.PortalConfigPath, config.PortalConfigToMap(nextPortal)); err != nil {
						return nil, err
					}
				}
				if touched["appearance"] {
					if err := deps.ConfFile.Set("appearance", nextAppearance.toMap()); err != nil {
						return nil, err
					}
				}
				if touched["backup.auto"] {
					if err := deps.ConfFile.Set(config.AutomaticBackupConfigPath, config.AutomaticBackupConfigToMap(nextBackup)); err != nil {
						return nil, err
					}
				}
				if touched["rate-limit"] {
					if _, err := deps.RateLimits.SetGlobal(commitCtx, nextRL); err != nil {
						return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, err.Error())
					}
				}
				_, nextRev := e.buildConfigSnapshot(commitCtx)
				return map[string]any{
					"updated":  true,
					"revision": nextRev,
					"effects":  effects,
				}, nil
			},
		},
	}, nil
}

func (e *Engine) handleConfigReset(ctx context.Context, caller Caller, req CallRequest) (callResult, *callError) {
	deps := e.Deps()
	target := normalizeConfigPath(req.Target)
	if target == "" {
		return callResult{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"config reset requires target namespace (general | portal | appearance | rate-limit)").withField("target")
	}
	if isTier0ConfigPath(target) || target == "backup.auto" {
		return callResult{}, newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
			fmt.Sprintf("resetting %q is forbidden via agent CLI", target)).withTier(Tier0Forbidden)
	}
	if caller.Policy != nil {
		if allowed, reason := caller.Policy.AllowsConfigPath(target); !allowed {
			return callResult{}, newCallError(http.StatusForbidden, ErrCodeCapabilityDenied, reason).withField("target")
		}
	}

	_, currentRev := e.buildConfigSnapshot(ctx)
	if strings.TrimSpace(req.Options.IfMatch) != "" && strings.TrimSpace(req.Options.IfMatch) != currentRev {
		return callResult{}, newCallError(http.StatusPreconditionFailed, ErrCodePreconditionFailed,
			fmt.Sprintf("config revision mismatch: got %s, current %s", req.Options.IfMatch, currentRev))
	}

	var diffs []DiffItem
	var commitFn func(context.Context) (any, error)

	switch target {
	case "general":
		if deps.ConfFile == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "config file is unavailable")
		}
		cur := config.GeneralConfigToMap(config.ReadGeneralConfig(deps.ConfFile))
		def := config.GeneralConfigToMap(config.DefaultGeneralConfig())
		diffs = diffFlatMaps("general", cur, def)
		commitFn = func(commitCtx context.Context) (any, error) {
			if err := deps.ConfFile.Set(config.GeneralConfigPath, def); err != nil {
				return nil, err
			}
			_, nextRev := e.buildConfigSnapshot(commitCtx)
			return map[string]any{"reset": "general", "revision": nextRev}, nil
		}
	case "portal":
		if deps.ConfFile == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "config file is unavailable")
		}
		cur := config.PortalConfigToMap(config.ReadPortalConfig(deps.ConfFile))
		def := config.PortalConfigToMap(config.DefaultPortalConfig())
		diffs = diffFlatMaps("portal", cur, def)
		commitFn = func(commitCtx context.Context) (any, error) {
			if err := deps.ConfFile.Set(config.PortalConfigPath, def); err != nil {
				return nil, err
			}
			_, nextRev := e.buildConfigSnapshot(commitCtx)
			return map[string]any{"reset": "portal", "revision": nextRev}, nil
		}
	case "appearance":
		if deps.ConfFile == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "config file is unavailable")
		}
		cur := e.readAppearanceState().toMap()
		def := defaultAppearanceState().toMap()
		diffs = diffFlatMaps("appearance", cur, def)
		commitFn = func(commitCtx context.Context) (any, error) {
			if err := deps.ConfFile.Set("appearance", def); err != nil {
				return nil, err
			}
			_, nextRev := e.buildConfigSnapshot(commitCtx)
			return map[string]any{"reset": "appearance", "revision": nextRev}, nil
		}
	case "rate-limit":
		if deps.RateLimits == nil {
			return callResult{}, newCallError(http.StatusServiceUnavailable, ErrCodeInternal, "rate limit service is unavailable")
		}
		curCfg, _ := deps.RateLimits.GetGlobal(ctx)
		defCfg := ratelimit.DefaultConfig()
		diffs = diffFlatMaps("rate-limit", rateLimitToMap(curCfg), rateLimitToMap(defCfg))
		commitFn = func(commitCtx context.Context) (any, error) {
			if _, err := deps.RateLimits.SetGlobal(commitCtx, ratelimit.ConfigInput{
				Status: store.RateLimitStatusDisabled,
			}); err != nil {
				return nil, err
			}
			_, nextRev := e.buildConfigSnapshot(commitCtx)
			return map[string]any{"reset": "rate-limit", "revision": nextRev}, nil
		}
	default:
		return callResult{}, newCallError(http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("unknown reset target %q", req.Target)).withField("target")
	}

	return callResult{
		Plan: &mutationPlan{
			Tier:           Tier3HighRisk,
			Revision:       currentRev,
			Summary:        fmt.Sprintf("重置配置域 %s 为默认值（%d 项变更）", target, len(diffs)),
			Diff:           diffs,
			NoOp:           len(diffs) == 0,
			AppliedEffects: []string{"config." + target + ".reset"},
			Commit:         commitFn,
		},
	}, nil
}

func (e *Engine) buildConfigSnapshot(ctx context.Context) (map[string]any, string) {
	deps := e.Deps()
	gen := config.GeneralConfigToMap(config.ReadGeneralConfig(deps.ConfFile))
	portal := config.PortalConfigToMap(config.ReadPortalConfig(deps.ConfFile))
	appMap := e.readAppearanceState().toMap()

	rlCfg := ratelimit.DefaultConfig()
	if deps.RateLimits != nil {
		if val, err := deps.RateLimits.GetGlobal(ctx); err == nil {
			rlCfg = val
		}
	}
	rlMap := rateLimitToMap(rlCfg)

	backupCfg, _ := config.ReadAutomaticBackupConfig(deps.ConfFile)
	backupMap := config.AutomaticBackupConfigForExport(backupCfg)

	netCfg := httpclient.ReadNetworkConfig(deps.ConfFile)
	netProxies := make([]map[string]any, 0, len(netCfg.Proxies))
	for _, p := range netCfg.Proxies {
		netProxies = append(netProxies, map[string]any{
			"id":   p.ID,
			"name": p.Name,
			"type": p.Type,
			"url":  RedactURL(p.URL),
		})
	}

	agentSettings := buildAgentSettingsSnapshot(deps.ConfFile)
	agentCaps := map[string]any{}
	if cs := e.capsStore(); cs != nil {
		if cfg, err := cs.GetConfig(); err == nil {
			agentCaps = map[string]any{
				"enable_agents_md":      cfg.EnableAgentsMD,
				"enable_skills":         cfg.EnableSkills,
				"sandbox_enabled":       cfg.SandboxEnabled,
				"disabled_skills":       cfg.DisabledSkills,
				"compact_trigger_ratio": cfg.CompactTriggerRatio,
				"context_window":        cfg.ContextWindow,
			}
		}
	}

	envSnap := sanitizedEnvSnapshot(deps.Config)

	snapshot := map[string]any{
		"general":            gen,
		"portal":             portal,
		"appearance":         appMap,
		"rate-limit":         rlMap,
		"backup":             map[string]any{"auto": backupMap},
		"network":            map[string]any{"proxies": netProxies},
		"agent":              map[string]any{"settings": agentSettings, "capabilities": agentCaps},
		"env":                envSnap,
	}

	revInput, _ := json.Marshal(map[string]any{
		"general":     gen,
		"portal":      portal,
		"appearance":  appMap,
		"rate-limit":  rlMap,
		"backup_auto": backupMap,
		"network":     netProxies,
	})
	sum := sha256.Sum256(revInput)
	return snapshot, "sha256:" + hex.EncodeToString(sum[:8])
}

func (e *Engine) readAppearanceState() appearanceState {
	out := defaultAppearanceState()
	deps := e.Deps()
	if deps.ConfFile == nil {
		return out
	}
	raw, ok := deps.ConfFile.Get("appearance")
	if !ok || raw == nil {
		return out
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return out
	}
	if v, ok := m["background_image"].(string); ok {
		out.BackgroundImage = v
	}
	if v, ok := m["home_page"].(string); ok && strings.TrimSpace(v) != "" {
		out.HomePage = strings.TrimSpace(v)
	}
	out.CustomBackgroundImages = toStringSlice(m["custom_background_images"])
	out.HiddenMenuItems = toStringSlice(m["hidden_menu_items"])
	return out
}

func validateAppearanceMap(m map[string]any) (appearanceState, *callError) {
	home, _ := m["home_page"].(string)
	home = strings.TrimSpace(strings.ToLower(home))
	if home == "" {
		home = "dashboard"
	}
	if home != "dashboard" && home != "agent" {
		return appearanceState{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"appearance.home_page must be dashboard or agent").withField("appearance.home_page")
	}
	hidden := toStringSlice(m["hidden_menu_items"])
	if len(hidden) > 50 {
		return appearanceState{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"appearance.hidden_menu_items exceeds 50 items").withField("appearance.hidden_menu_items")
	}
	custom := toStringSlice(m["custom_background_images"])
	bg, _ := m["background_image"].(string)
	bg = strings.TrimSpace(bg)
	if bg != "" && !slices.Contains(custom, bg) {
		return appearanceState{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"appearance.background_image must be empty or one of custom_background_images").
			withField("appearance.background_image")
	}
	return appearanceState{
		BackgroundImage:        bg,
		CustomBackgroundImages: custom,
		HiddenMenuItems:        hidden,
		HomePage:               home,
	}, nil
}

func buildAgentSettingsSnapshot(cf *config.ConfigFile) map[string]any {
	sitePolicy, modelPolicy, siteIDs, siteModelIDs := agentproxy.AccessPolicyFromConfig(cf)
	return map[string]any{
		"fast_model":                agentproxy.FastModelFromConfig(cf),
		"classifier_model":          agentproxy.ClassifierModelFromConfig(cf),
		"prompt_suggestion_enabled": agentproxy.PromptSuggestionEnabledFromConfig(cf),
		"image_models":              agentproxy.ImageModelsFromConfig(cf),
		"access_policy": map[string]any{
			"site_policy":    sitePolicy,
			"model_policy":   modelPolicy,
			"site_ids":       siteIDs,
			"site_model_ids": siteModelIDs,
		},
	}
}

func sanitizedEnvSnapshot(cfg *config.Config) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	return map[string]any{
		"app_env":               cfg.AppEnv,
		"app_name":              cfg.AppName,
		"http_host":             cfg.HTTPHost,
		"http_port":             cfg.HTTPPort,
		"log_level":             cfg.LogLevel,
		"log_debug_enabled":     cfg.LogDebugEnabled,
		"log_retention_days":    cfg.LogRetentionDays,
		"request_timeout":       cfg.RequestTimeout.String(),
		"site_health_interval":  cfg.SiteHealthInterval.String(),
		"site_health_workers":   cfg.SiteHealthWorkers,
		"agent_max_concurrent":  cfg.AgentMaxConcurrent,
		"agent_run_timeout":     cfg.AgentRunTimeout.String(),
		"agent_cli_configured":  strings.TrimSpace(cfg.AgentCLIBin) != "",
		"device_idle_days":      cfg.DeviceIdleDays,
		"push_enabled":          cfg.PushEnabled,
	}
}

func normalizeConfigPath(raw string) string {
	norm := strings.TrimSpace(raw)
	norm = strings.TrimPrefix(norm, "global.")
	norm = strings.ReplaceAll(norm, "rate_limit.", "rate-limit.")
	if norm == "rate_limit" {
		norm = "rate-limit"
	}
	if strings.HasPrefix(norm, "backup.automatic.") {
		norm = "backup.auto." + strings.TrimPrefix(norm, "backup.automatic.")
	} else if norm == "backup.automatic" {
		norm = "backup.auto"
	}
	return norm
}

func isTier0ConfigPath(path string) bool {
	if path == "agent" || strings.HasPrefix(path, "agent.") ||
		path == "env" || strings.HasPrefix(path, "env.") ||
		path == "mcp" || strings.HasPrefix(path, "mcp.") {
		return true
	}
	low := strings.ToLower(path)
	if strings.HasPrefix(low, "backup.auto.storage.secret_key") ||
		strings.HasPrefix(low, "backup.auto.storage.backup_passphrase") {
		return true
	}
	return false
}

func effectiveTierForConfigPath(path string, baseTier int) int {
	if isTier0ConfigPath(path) {
		return Tier0Forbidden
	}
	if strings.HasPrefix(path, "general.ip_whitelist.") || path == "general.ip_whitelist" ||
		strings.HasPrefix(path, "general.security.") || path == "general.security" {
		return Tier3HighRisk
	}
	return baseTier
}

func lookupConfigSnapshotPath(snap map[string]any, norm string) (val any, writable bool, tier int, found bool) {
	parts := strings.Split(norm, ".")
	if len(parts) == 0 {
		return nil, false, 0, false
	}
	// Resolve dotted namespace metadata.
	tier = effectiveTierForConfigPath(norm, Tier2Managed)
	writable = tier > Tier0Forbidden && !strings.HasPrefix(norm, "network")
	if norm == "network" || strings.HasPrefix(norm, "network.") {
		tier = Tier1ReadOnly
		writable = false
	}

	// Walk rate-limit top key directly because snap uses "rate-limit".
	cur := any(snap)
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false, 0, false
		}
		nxt, exists := m[p]
		if !exists {
			return nil, false, 0, false
		}
		cur = nxt
	}
	return cur, writable, tier, true
}

func flattenMutations(target string, payload any) (map[string]any, error) {
	m, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload must be a JSON object")
	}
	rawFlat := make(map[string]any)
	flattenMapInto("", m, rawFlat)

	prefix := normalizeConfigPath(target)
	if prefix == "all" {
		prefix = ""
	}
	out := make(map[string]any, len(rawFlat))
	for k, v := range rawFlat {
		normK := normalizeConfigPath(k)
		if prefix == "" || normK == prefix || strings.HasPrefix(normK, prefix+".") || hasKnownConfigRoot(normK) {
			out[normK] = v
		} else {
			out[prefix+"."+normK] = v
		}
	}
	return out, nil
}

func hasKnownConfigRoot(path string) bool {
	for _, root := range []string{"general", "portal", "appearance", "rate-limit", "backup.auto", "network", "agent", "env", "mcp"} {
		if path == root || strings.HasPrefix(path, root+".") {
			return true
		}
	}
	return false
}

func flattenMapInto(prefix string, src map[string]any, dst map[string]any) {
	for k, v := range src {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		full := k
		if prefix != "" {
			full = prefix + "." + k
		}
		if subMap, ok := v.(map[string]any); ok && len(subMap) > 0 {
			flattenMapInto(full, subMap, dst)
			continue
		}
		dst[full] = v
	}
}

func applyTypedMapLeaf(curMap map[string]any, defaultMap map[string]any, subPath string, incoming any, fullPath string) (any, any, *callError) {
	parts := strings.Split(subPath, ".")
	if len(parts) == 0 {
		return nil, nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "empty config key")
	}
	beforeVal, ok := getNestedMapValue(curMap, parts)
	templateVal, hasTemplate := getNestedMapValue(defaultMap, parts)
	if !ok && !hasTemplate {
		return nil, nil, newCallError(http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("unknown config key %q", fullPath)).withField(fullPath)
	}
	ref := templateVal
	if !hasTemplate {
		ref = beforeVal
	}
	typed, ce := coerceByTemplate(ref, incoming, fullPath)
	if ce != nil {
		return nil, nil, ce
	}
	setNestedMapValue(curMap, parts, typed)
	return beforeVal, typed, nil
}

func applyRateLimitLeaf(rlMap map[string]any, sub string, incoming any, fullPath string) (any, any, *callError) {
	switch sub {
	case "status":
		before := rlMap["status"]
		s, ok := incoming.(string)
		if !ok {
			return nil, nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				"rate-limit.status must be a string (enabled or disabled)").withField(fullPath)
		}
		rlMap["status"] = strings.TrimSpace(strings.ToLower(s))
		return before, rlMap["status"], nil
	case "rpm_limit", "tpm_limit":
		before := rlMap[sub]
		if incoming == nil {
			rlMap[sub] = nil
			return before, nil, nil
		}
		if s, ok := incoming.(string); ok && strings.EqualFold(strings.TrimSpace(s), "null") {
			rlMap[sub] = nil
			return before, nil, nil
		}
		n, ok := toInt64Strict(incoming)
		if !ok || n <= 0 {
			return nil, nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				fullPath+" must be a positive integer or null").withField(fullPath)
		}
		rlMap[sub] = n
		return before, n, nil
	default:
		return nil, nil, newCallError(http.StatusNotFound, ErrCodeNotFound,
			fmt.Sprintf("unknown rate-limit key %q", fullPath)).withField(fullPath)
	}
}

func validateRateLimitMap(rlMap map[string]any) (ratelimit.ConfigInput, *callError) {
	status, _ := rlMap["status"].(string)
	status = strings.TrimSpace(strings.ToLower(status))
	if status != store.RateLimitStatusEnabled && status != store.RateLimitStatusDisabled {
		return ratelimit.ConfigInput{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			"rate-limit.status must be enabled or disabled").withField("rate-limit.status")
	}
	var rpm, tpm *int64
	if rlMap["rpm_limit"] != nil {
		n, ok := toInt64Strict(rlMap["rpm_limit"])
		if !ok || n <= 0 {
			return ratelimit.ConfigInput{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				"rate-limit.rpm_limit must be > 0").withField("rate-limit.rpm_limit")
		}
		rpm = &n
	}
	if rlMap["tpm_limit"] != nil {
		n, ok := toInt64Strict(rlMap["tpm_limit"])
		if !ok || n <= 0 {
			return ratelimit.ConfigInput{}, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				"rate-limit.tpm_limit must be > 0").withField("rate-limit.tpm_limit")
		}
		tpm = &n
	}
	return ratelimit.ConfigInput{
		Status:   status,
		RPMLimit: rpm,
		TPMLimit: tpm,
	}, nil
}

func rateLimitToMap(cfg ratelimit.Config) map[string]any {
	status := cfg.Status
	if status == "" {
		status = store.RateLimitStatusDisabled
	}
	return map[string]any{
		"status":    status,
		"rpm_limit": cfg.RPMLimit,
		"tpm_limit": cfg.TPMLimit,
	}
}

func coerceByTemplate(template any, incoming any, fullPath string) (any, *callError) {
	switch template.(type) {
	case bool:
		switch v := incoming.(type) {
		case bool:
			return v, nil
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(v))
			if err == nil {
				return parsed, nil
			}
		}
		return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			fmt.Sprintf("%s expects a boolean value", fullPath)).withField(fullPath)
	case int, int64, float64:
		n, ok := toIntStrict(incoming)
		if !ok {
			return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				fmt.Sprintf("%s expects an integer value", fullPath)).withField(fullPath)
		}
		return n, nil
	case []string:
		switch v := incoming.(type) {
		case []string:
			return slices.Clone(v), nil
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
						fmt.Sprintf("%s expects an array of strings", fullPath)).withField(fullPath)
				}
				out = append(out, s)
			}
			return out, nil
		case string:
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "[") {
				var parsed []string
				if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
					return parsed, nil
				}
			}
		}
		return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			fmt.Sprintf("%s expects a string array", fullPath)).withField(fullPath)
	case string:
		s, ok := incoming.(string)
		if !ok {
			return nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
				fmt.Sprintf("%s expects a string value", fullPath)).withField(fullPath)
		}
		return s, nil
	default:
		return incoming, nil
	}
}

func getNestedMapValue(m map[string]any, parts []string) (any, bool) {
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		nxt, exists := mm[p]
		if !exists {
			return nil, false
		}
		cur = nxt
	}
	return cur, true
}

func setNestedMapValue(m map[string]any, parts []string, val any) {
	cur := m
	for i := 0; i < len(parts)-1; i++ {
		nxt, ok := cur[parts[i]].(map[string]any)
		if !ok {
			nxt = map[string]any{}
			cur[parts[i]] = nxt
		}
		cur = nxt
	}
	cur[parts[len(parts)-1]] = val
}

func diffFlatMaps(prefix string, before, after map[string]any) []DiffItem {
	bFlat := map[string]any{}
	aFlat := map[string]any{}
	flattenMapInto(prefix, before, bFlat)
	flattenMapInto(prefix, after, aFlat)
	keys := make([]string, 0, len(aFlat))
	seen := map[string]bool{}
	for k := range bFlat {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range aFlat {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	diffs := make([]DiffItem, 0)
	for _, k := range keys {
		bv := bFlat[k]
		av := aFlat[k]
		if !equalJSONValue(bv, av) {
			diffs = append(diffs, DiffItem{Path: k, Before: bv, After: av})
		}
	}
	return diffs
}

func equalJSONValue(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func toIntStrict(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if float64(int(x)) != x {
			return 0, false
		}
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	default:
		return 0, false
	}
}

func toInt64Strict(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if float64(int64(x)) != x {
			return 0, false
		}
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func toInt(v any) (int, bool) {
	return toIntStrict(v)
}

func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return slices.Clone(x)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{}
	}
}

func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	s, _ := params[key].(string)
	return s
}

func boolParam(params map[string]any, key string, def bool) bool {
	if params == nil {
		return def
	}
	raw, exists := params[key]
	if !exists || raw == nil {
		return def
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}
