package site

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/adapter"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/store"
	"xlyra/server/internal/upstream"
)

// Plugin kinds other than quota_probe and protocol, as seen from the site service.
//
//	model_list, credential_check, pricing_parse  bound to one site by an admin
//	site_detect, model_metadata                  apply to every site once enabled
//
// Everything here is opt-in: with no plugin bound or enabled the code paths
// behave exactly as before.

const (
	maxPluginPricingBody = 4 << 20
	pluginPricingTimeout = 30 * time.Second
)

// siteHookPlugins returns the mounted plugins that export hook and do not
// declare a site type, in plugin_ids order. A site-type package is the base
// for its own type and does not take effect through a mount. A plugin that is
// no longer enabled is skipped. Duplicate ids are returned once.
func (s *Service) siteHookPlugins(item store.Site, hook string) []*jsplugin.Plugin {
	if s == nil || hook == "" {
		return nil
	}
	registry := s.activeJSRegistry()
	if registry == nil {
		return nil
	}
	var out []*jsplugin.Plugin
	seen := map[string]bool{}
	for _, id := range SitePluginIDs(GatewayConfigFromSiteMeta(item.Meta)) {
		if seen[id] {
			continue
		}
		seen[id] = true
		plugin, ok := registry.ByPluginID(id)
		if !ok || plugin == nil || plugin.Manifest.DeclaresSiteType() || !plugin.HasHook(hook) {
			continue
		}
		out = append(out, plugin)
	}
	return out
}

// siteHookPlugin returns the first mounted plugin that exports hook and does
// not declare a site type. A plugin that is no longer enabled is skipped, so
// disabling it returns the site to the next plugin or the default.
func (s *Service) siteHookPlugin(item store.Site, hook string) (*jsplugin.Plugin, bool) {
	plugins := s.siteHookPlugins(item, hook)
	if len(plugins) == 0 {
		return nil, false
	}
	return plugins[0], true
}

// siteTypeHookPlugin returns the enabled site_provider for siteType when it
// exports hook. A disabled type, another kind, or a missing export is false.
func (s *Service) siteTypeHookPlugin(siteType, hook string) (*jsplugin.Plugin, bool) {
	return ProviderHookPlugin(s.activeJSRegistry(), siteType, hook)
}

// ProviderHookPlugin returns the enabled site_provider registered for siteType
// when it exports hook. registry is the catalog generation to read. A disabled
// type, another kind, a missing export, or a nil registry is false.
func ProviderHookPlugin(registry *jsplugin.Registry, siteType, hook string) (*jsplugin.Plugin, bool) {
	item, ok := LookupPluginSiteType(siteType)
	if !ok || !item.Enabled || item.PluginID == "" {
		return nil, false
	}
	if registry == nil {
		return nil, false
	}
	plugin, ok := registry.ByPluginID(item.PluginID)
	if !ok || plugin == nil || !plugin.Manifest.DeclaresSiteType() || !plugin.HasHook(hook) {
		return nil, false
	}
	return plugin, true
}

// withSitePluginContext attaches per-site setting overrides and site-scoped
// storage to ctx for one plugin call.
func (s *Service) withSitePluginContext(ctx context.Context, item store.Site, plugin *jsplugin.Plugin) context.Context {
	if plugin == nil {
		return ctx
	}
	if overrides := SitePluginSettings(GatewayConfigFromSiteMeta(item.Meta), plugin.Manifest.ID); len(overrides) > 0 {
		ctx = jsplugin.WithSiteSettings(ctx, overrides)
	}
	if s != nil && s.db != nil && item.ID != uuid.Nil && plugin.AllowsStorage() {
		storageRepo := store.NewJSPluginStorageRepository(s.db.DB())
		ctx = jsplugin.WithCallStorage(ctx, jsplugin.NewStoreStorage(storageRepo, plugin.Manifest.ID, jsplugin.StorageScopeSite, item.ID.String()))
	}
	return ctx
}

// driveStepped runs a model_list, credential_check or site_detect plugin to
// completion. The host sends every request and adds the credential; secret is
// empty for kinds that take none, and then no Authorization header is sent.
func driveStepped(ctx context.Context, client *http.Client, plugin *jsplugin.Plugin, probeCtx jsplugin.ProbeContext, secret string) (any, error) {
	return driveSteppedHook(ctx, client, plugin, "", "", probeCtx, secret)
}

// driveSteppedHook is driveStepped for one export. An empty hook uses the
// plugin kind's own stepped hook. A hook failure is returned as-is.
func driveSteppedHook(ctx context.Context, client *http.Client, plugin *jsplugin.Plugin, hook, resultKind string, probeCtx jsplugin.ProbeContext, secret string) (result any, err error) {
	recorded := hook
	if recorded == "" {
		if spec, ok := jsplugin.LookupKind(plugin.Manifest.Kind); ok && len(spec.Hooks) > 0 {
			recorded = spec.Hooks[0]
		}
	}
	defer func() {
		if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
			breaker.RecordProbeResult(plugin, recorded, err == nil)
		}
	}()
	mode, defaultBase := plugin.Manifest.BaseURLSettings()
	canonical, err := canonicalFromMode(mode, defaultBase, probeCtx.BaseURL)
	if err != nil {
		return nil, err
	}
	probeCtx.BaseURL = canonical
	httpClient := http.Client{Timeout: quotaProbeRequestTimeout}
	if client != nil {
		httpClient = *client
	}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if err := jsplugin.SameOrigin(canonical, req.URL.String()); err != nil {
			return &redirectRejectedError{err: err}
		}
		return nil
	}
	var steps []jsplugin.ProbeStep
	for step := 0; step < jsplugin.MaxProbeSteps; step++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var decision jsplugin.SteppedDecision
		var callErr error
		if hook == "" {
			decision, callErr = plugin.CallStepped(ctx, probeCtx, steps)
		} else {
			decision, callErr = plugin.CallSteppedHook(ctx, hook, resultKind, probeCtx, steps)
		}
		logs, dropped := decision.Logs, decision.DroppedLogs
		if call, ok := callErr.(*jsplugin.CallError); ok {
			logs, dropped = call.Logs, call.DroppedLogs
		}
		emitPluginLogs(ctx, plugin, step, logs, dropped)
		if callErr != nil {
			logProbeFailure(plugin, step, callErr)
			return nil, callErr
		}
		if decision.Error != "" {
			return nil, fmt.Errorf("plugin %s: %s", plugin.Manifest.ID, decision.Error)
		}
		if decision.Result != nil {
			return decision.Result, nil
		}
		if decision.Request == nil || step == jsplugin.MaxProbeSteps-1 {
			return nil, fmt.Errorf("plugin %s exceeded %d steps", plugin.Manifest.ID, jsplugin.MaxProbeSteps)
		}
		target, resolveErr := jsplugin.ResolveProbeURL(canonical, decision.Request.Path, decision.Request.Query)
		if resolveErr != nil {
			return nil, &jsplugin.CallError{Kind: jsplugin.KindURLRejected, Detail: resolveErr.Error()}
		}
		if hostErr := CheckPluginHost(probeCtx.SiteType, target); hostErr != nil {
			return nil, fmt.Errorf("plugin %s: %w", plugin.Manifest.ID, hostErr)
		}
		response, err := doProbeHTTP(ctx, &httpClient, canonical, secret, decision.Request)
		if err != nil {
			return nil, err
		}
		steps = append(steps, jsplugin.ProbeStep{Request: *decision.Request, Response: response})
	}
	return nil, fmt.Errorf("plugin %s exceeded %d steps", plugin.Manifest.ID, jsplugin.MaxProbeSteps)
}

// ---- model_list ----

// pluginModelLister adapts a model_list plugin, or a site_provider listModels
// hook, to the interface the model sync uses. An empty hook uses the plugin's
// own stepped export; hook and resultKind name one export and its contract.
type pluginModelLister struct {
	plugin     *jsplugin.Plugin
	hook       string
	resultKind string
	wrapCtx    func(context.Context) context.Context
	clientFor  func(context.Context) (*http.Client, error)
}

func (l pluginModelLister) ListModels(ctx context.Context, site adapter.SiteConfig, apiKey string) ([]adapter.Model, error) {
	if l.wrapCtx != nil {
		ctx = l.wrapCtx(ctx)
	}
	client := site.Client
	if l.clientFor != nil && l.plugin.Manifest.TransportProfile() != "" {
		if profiled, clientErr := l.clientFor(ctx); clientErr == nil && profiled != nil {
			client = profiled
		}
	}
	result, err := driveSteppedHook(ctx, client, l.plugin, l.hook, l.resultKind, jsplugin.ProbeContext{
		SiteType:       site.SiteType,
		BaseURL:        site.BaseURL,
		CredentialType: defaultCredentialType,
		Now:            time.Now().UnixMilli(),
	}, apiKey)
	if err != nil {
		return nil, err
	}
	list, ok := result.(*jsplugin.ModelListResult)
	if !ok {
		return nil, fmt.Errorf("plugin %s returned an unexpected result", l.plugin.Manifest.ID)
	}
	models := make([]adapter.Model, 0, len(list.Models))
	for _, entry := range list.Models {
		capabilities := map[string]any{}
		for key, value := range entry.Capabilities {
			capabilities[key] = value
		}
		if len(entry.Meta) > 0 {
			capabilities["plugin_meta"] = entry.Meta
		}
		display := entry.DisplayName
		if display == "" {
			display = entry.Name
		}
		models = append(models, adapter.Model{UpstreamName: entry.Name, DisplayName: display, Capabilities: capabilities})
	}
	return models, nil
}

// modelListerFor returns the model source for a site. Mounted listModels
// plugins are merged with the site type's hook and the adapter: a higher
// priority source overrides a lower one for the same upstream name, and names
// only a lower source has are kept. A single source is returned as itself, so
// a site with one plugin behaves exactly as before. With no plugin source the
// adapter is used, including when it does not support listing models.
func (s *Service) modelListerFor(item store.Site, module adapter.Module) (adapter.GatewayModelLister, bool) {
	var sources []adapter.GatewayModelLister
	for _, plugin := range s.siteHookPlugins(item, "listModels") {
		sources = append(sources, s.pluginModelSource(item, plugin))
	}
	if plugin, ok := s.siteTypeHookPlugin(item.SiteType, "listModels"); ok {
		sources = append(sources, s.pluginModelSource(item, plugin))
	}
	switch len(sources) {
	case 0:
		return adapter.AsGatewayModelLister(module)
	case 1:
		return sources[0], true
	default:
		if fallback, ok := adapter.AsGatewayModelLister(module); ok {
			sources = append(sources, fallback)
		}
		return mergedModelLister{sources: sources}, true
	}
}

// pluginModelSource is one plugin's listModels hook as a model lister.
func (s *Service) pluginModelSource(item store.Site, plugin *jsplugin.Plugin) pluginModelLister {
	return pluginModelLister{
		plugin:     plugin,
		hook:       "listModels",
		resultKind: jsplugin.KindModelList,
		wrapCtx:    func(ctx context.Context) context.Context { return s.withSitePluginContext(ctx, item, plugin) },
		clientFor:  func(ctx context.Context) (*http.Client, error) { return s.pluginProfileClient(ctx, item, plugin) },
	}
}

// mergedModelLister calls each source from highest priority to lowest. An
// entry with an upstream name already produced is skipped, so the first source
// wins. A source that fails is logged and skipped; the error is returned only
// when every source failed or none produced a model.
type mergedModelLister struct {
	sources []adapter.GatewayModelLister
}

func (l mergedModelLister) ListModels(ctx context.Context, site adapter.SiteConfig, apiKey string) ([]adapter.Model, error) {
	var merged []adapter.Model
	seen := map[string]bool{}
	var failures []error
	for _, source := range l.sources {
		models, err := source.ListModels(ctx, site, apiKey)
		if err != nil {
			slog.Default().Debug("listModels source failed", "error", err)
			failures = append(failures, err)
			continue
		}
		for _, model := range models {
			if seen[model.UpstreamName] {
				continue
			}
			seen[model.UpstreamName] = true
			merged = append(merged, model)
		}
	}
	if len(merged) == 0 && len(failures) > 0 {
		return nil, failures[0]
	}
	return merged, nil
}

// ---- credential_check ----

// pluginCredentialValidator adapts a credential_check plugin, or a
// site_provider check hook, to adapter.CredentialValidator. A rejected key
// becomes an error that classifies as credential_invalid; an unanswered check
// becomes a transient one, so a site outage is never mistaken for a bad key.
// An empty hook uses the plugin's own stepped export.
type pluginCredentialValidator struct {
	plugin     *jsplugin.Plugin
	hook       string
	resultKind string
	wrapCtx    func(context.Context) context.Context
	clientFor  func(context.Context) (*http.Client, error)
}

func (v pluginCredentialValidator) ValidateCredentials(ctx context.Context, site adapter.SiteConfig, apiKey string) error {
	if v.wrapCtx != nil {
		ctx = v.wrapCtx(ctx)
	}
	client := site.Client
	if v.clientFor != nil && v.plugin.Manifest.TransportProfile() != "" {
		if profiled, clientErr := v.clientFor(ctx); clientErr == nil && profiled != nil {
			client = profiled
		}
	}
	result, err := driveSteppedHook(ctx, client, v.plugin, v.hook, v.resultKind, jsplugin.ProbeContext{
		SiteType:       site.SiteType,
		BaseURL:        site.BaseURL,
		CredentialType: defaultCredentialType,
		Now:            time.Now().UnixMilli(),
	}, apiKey)
	if err != nil {
		return err
	}
	check, ok := result.(*jsplugin.CredentialCheckResult)
	if !ok {
		return fmt.Errorf("plugin %s returned an unexpected result", v.plugin.Manifest.ID)
	}
	message := strings.TrimSpace(check.Message)
	switch check.Status {
	case "ok":
		return nil
	case "invalid":
		return credentialCheckError(http.StatusUnauthorized, upstream.FailureCredentialInvalid, nonEmpty(message, "credential rejected"))
	default:
		return credentialCheckError(http.StatusServiceUnavailable, upstream.FailureTransient, nonEmpty(message, "site could not answer"))
	}
}

// credentialCheckError carries the plugin's verdict as an already classified
// upstream error, so the callers that branch on the class (disabling a key,
// retrying later) do not have to guess it from the text.
func credentialCheckError(statusCode int, class upstream.FailureClass, message string) error {
	return &upstream.HTTPError{
		Prefix: "credential check returned",
		Body:   []byte(message),
		Failure: upstream.Failure{
			Class:      class,
			StatusCode: statusCode,
			Message:    message,
		},
	}
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// credentialValidatorFor returns the credential check for a site. Every
// mounted check plugin and the site type's check hook must pass. A single
// plugin is returned as itself, so a site with one check behaves exactly as
// before. With no plugin check the adapter is used.
// checkPlugins is every check for a site, highest priority first: mounted
// plugins, then the site type's own hook.
func (s *Service) checkPlugins(item store.Site) []pluginCredentialValidator {
	var validators []pluginCredentialValidator
	for _, plugin := range s.siteHookPlugins(item, "check") {
		validators = append(validators, s.pluginCheckSource(item, plugin))
	}
	if plugin, ok := s.siteTypeHookPlugin(item.SiteType, "check"); ok {
		validators = append(validators, s.pluginCheckSource(item, plugin))
	}
	return validators
}

func (s *Service) credentialValidatorFor(item store.Site, module adapter.Module) (adapter.CredentialValidator, bool) {
	validators := s.checkPlugins(item)
	switch len(validators) {
	case 0:
		return adapter.AsCredentialValidator(module)
	case 1:
		return validators[0], true
	default:
		return mergedCredentialValidator{validators: validators}, true
	}
}

// pluginCheckSource is one plugin's check hook, with the site's settings.
func (s *Service) pluginCheckSource(item store.Site, plugin *jsplugin.Plugin) pluginCredentialValidator {
	validator := s.pluginCheckValidator(item, plugin)
	validator.wrapCtx = func(ctx context.Context) context.Context { return s.withSitePluginContext(ctx, item, plugin) }
	return validator
}

// mergedCredentialValidator passes only when every check passes. The first
// invalid verdict wins; otherwise the first failure or unusable answer wins.
// A hook that errors is treated as unavailable and the remaining checks still run.
type mergedCredentialValidator struct {
	validators []pluginCredentialValidator
}

func (v mergedCredentialValidator) ValidateCredentials(ctx context.Context, site adapter.SiteConfig, apiKey string) error {
	var firstInvalid, firstOther error
	for _, validator := range v.validators {
		err := validator.ValidateCredentials(ctx, site, apiKey)
		if err == nil {
			continue
		}
		if upstream.ClassifyError(err).CredentialInvalid() {
			if firstInvalid == nil {
				firstInvalid = err
			}
			continue
		}
		slog.Default().Debug("check plugin failed", "plugin_id", validator.plugin.Manifest.ID, "error", err)
		if firstOther == nil {
			firstOther = err
		}
	}
	if firstInvalid != nil {
		return firstInvalid
	}
	return firstOther
}

// pluginCheckValidator is the check hook for one plugin, with its transport profile.
func (s *Service) pluginCheckValidator(item store.Site, plugin *jsplugin.Plugin) pluginCredentialValidator {
	return pluginCredentialValidator{
		plugin: plugin, hook: "check", resultKind: jsplugin.KindCredentialCheck,
		clientFor: func(ctx context.Context) (*http.Client, error) { return s.pluginProfileClient(ctx, item, plugin) },
	}
}

// runMergedHealthCheck runs every check plugin for the site. All of them must
// pass; the first invalid verdict, otherwise the first failure, is the result.
func (s *Service) runMergedHealthCheck(ctx context.Context, item store.Site, validator mergedCredentialValidator, start time.Time, check siteHealthCheck) siteHealthCheck {
	ids := make([]string, 0, len(validator.validators))
	for _, source := range validator.validators {
		ids = append(ids, source.plugin.Manifest.ID)
	}
	check.endpoint = "credential_check " + strings.Join(ids, ",")
	check.metadata["plugins"] = ids
	_, credential, err := s.siteWithAPIKeyCredential(ctx, item.ID)
	if err != nil {
		check.latency = time.Since(start)
		check.errorType = "missing_credential"
		check.message = err.Error()
		return check
	}
	err = validator.ValidateCredentials(ctx, s.toAdapterSite(ctx, item), credential.Secret)
	check.latency = time.Since(start)
	if err == nil {
		check.success = true
		check.message = "ok"
		return check
	}
	failure := upstream.ClassifyError(err)
	switch {
	case failure.CredentialInvalid():
		check.errorType = "validation_failed"
	case failure.Limited():
		check.success = true
		check.message = "ok"
		check.metadata["credential_limited"] = healthFailureMetadata(failure)
		return check
	default:
		check.errorType = "upstream_unreachable"
	}
	check.message = err.Error()
	return check
}

// runPluginHealthCheck runs a credential_check plugin, or a site_provider
// check hook, against the site's first usable API key. check passing is healthy.
func (s *Service) runPluginHealthCheck(ctx context.Context, item store.Site, validator pluginCredentialValidator, start time.Time, check siteHealthCheck) siteHealthCheck {
	plugin := validator.plugin
	check.endpoint = "credential_check " + plugin.Manifest.ID
	check.metadata["plugin"] = plugin.Manifest.ID
	_, credential, err := s.siteWithAPIKeyCredential(ctx, item.ID)
	if err != nil {
		check.latency = time.Since(start)
		check.errorType = "missing_credential"
		check.message = err.Error()
		return check
	}
	err = validator.ValidateCredentials(ctx, s.toAdapterSite(ctx, item), credential.Secret)
	check.latency = time.Since(start)
	if err == nil {
		check.success = true
		check.message = "ok"
		return check
	}
	failure := upstream.ClassifyError(err)
	switch {
	case failure.CredentialInvalid():
		check.errorType = "validation_failed"
	case failure.Limited():
		check.success = true
		check.message = "ok"
		check.metadata["credential_limited"] = healthFailureMetadata(failure)
		return check
	default:
		check.errorType = "upstream_unreachable"
	}
	check.message = err.Error()
	return check
}

// ---- site_detect ----

// detectWithPlugins runs every enabled site_detect plugin and returns the most
// confident match. A plugin must name a site type xLyra already supports;
// anything else is ignored, because a plugin cannot add a site type.
func (s *Service) detectWithPlugins(ctx context.Context, baseURL string) (DetectResult, bool) {
	registry := s.activeJSRegistry()
	if registry == nil || s.httpClients == nil {
		return DetectResult{}, false
	}
	plugins := registry.WithHook("detect")
	if len(plugins) == 0 {
		return DetectResult{}, false
	}
	var best DetectResult
	for _, plugin := range plugins {
		client, err := s.pluginProfileClient(ctx, store.Site{}, plugin)
		if err != nil {
			continue
		}
		result, err := driveSteppedHook(ctx, client, plugin, "detect", jsplugin.KindSiteDetect, jsplugin.ProbeContext{BaseURL: baseURL, Now: time.Now().UnixMilli()}, "")
		if err != nil {
			slog.Default().Debug("site_detect plugin failed", "plugin_id", plugin.Manifest.ID, "error", err)
			continue
		}
		detected, ok := result.(*jsplugin.SiteDetectResult)
		if !ok || !detected.Matched {
			continue
		}
		siteType := normalizeSiteType(detected.SiteType)
		if _, supported := s.adapters.ModuleForSiteType(siteType); !supported {
			slog.Default().Warn("site_detect plugin named an unsupported site type", "plugin_id", plugin.Manifest.ID, "site_type", detected.SiteType)
			continue
		}
		confidence := 0.5
		if detected.Confidence != nil {
			confidence = *detected.Confidence
		}
		if best.Matched && confidence <= best.Confidence {
			continue
		}
		features := map[string]any{}
		for key, value := range detected.Features {
			features[key] = value
		}
		features["plugin"] = plugin.Manifest.ID
		best = DetectResult{SiteType: siteType, Matched: true, Confidence: confidence, Features: features}
	}
	return best, best.Matched
}

// ---- model_metadata ----

// describeModelsPlugins orders the plugins that fill model metadata: the
// site's mounted plugins first (highest priority), then the other enabled
// plugins by id, and the site type's own hook last. A plugin appears once.
func describeModelsPlugins(registry *jsplugin.Registry, item store.Site) []*jsplugin.Plugin {
	if registry == nil {
		return nil
	}
	var out []*jsplugin.Plugin
	seen := map[string]bool{}
	add := func(plugin *jsplugin.Plugin) {
		if plugin == nil || seen[plugin.Manifest.ID] || !plugin.HasHook("describeModels") {
			return
		}
		seen[plugin.Manifest.ID] = true
		out = append(out, plugin)
	}
	cfg := GatewayConfigFromSiteMeta(item.Meta)
	for _, id := range SitePluginIDs(cfg) {
		if plugin, ok := registry.ByPluginID(id); ok {
			add(plugin)
		}
	}
	for _, plugin := range registry.WithHook("describeModels") {
		add(plugin)
	}
	if plugin, ok := ProviderHookPlugin(registry, item.SiteType, "describeModels"); ok {
		add(plugin)
	}
	return out
}

// applyModelMetadataPlugins lets enabled model_metadata plugins fill in what is
// still missing on a model. Plugins have the lowest priority: they never
// replace a value the upstream, the curated list or the catalog already gave.
func (s *Service) applyModelMetadataPlugins(ctx context.Context, item store.Site, model adapter.Model) adapter.Model {
	registry := s.activeJSRegistry()
	if registry == nil || strings.TrimSpace(model.UpstreamName) == "" {
		return model
	}
	for _, plugin := range describeModelsPlugins(registry, item) {
		callCtx := s.withSitePluginContext(ctx, item, plugin)
		result, err := plugin.CallModelMetadataHook(callCtx,
			jsplugin.ModelMetadataContext{SiteType: item.SiteType},
			jsplugin.ModelMetadataInput{Models: []string{model.UpstreamName}})
		if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
			breaker.RecordProbeResult(plugin, "describeModels", err == nil)
		}
		if err != nil {
			slog.Default().Debug("model_metadata plugin failed", "plugin_id", plugin.Manifest.ID, "error", err)
			continue
		}
		for _, entry := range result.Models {
			if entry.ID != model.UpstreamName {
				continue
			}
			if model.DisplayName == "" || model.DisplayName == model.UpstreamName {
				if entry.Name != "" {
					model.DisplayName = entry.Name
				}
			}
			if (len(entry.Capabilities) > 0 || len(entry.Meta) > 0) && model.Capabilities == nil {
				model.Capabilities = map[string]any{}
			}
			for key, value := range entry.Capabilities {
				if _, present := model.Capabilities[key]; !present {
					model.Capabilities[key] = value
				}
			}
			if len(entry.Meta) > 0 {
				if _, present := model.Capabilities["plugin_meta"]; !present {
					model.Capabilities["plugin_meta"] = entry.Meta
				}
			}
		}
	}
	return model
}

// ---- pricing_parse ----

// fetchPluginPricing downloads the price table a pricing_parse plugin declared
// in its manifest and returns the plugin's reading of it. The host makes the
// request; the plugin only sees the JSON that came back.
func (s *Service) fetchPluginPricing(ctx context.Context, item store.Site, plugin *jsplugin.Plugin) (adapter.PricingSnapshot, error) {
	_, credential, err := s.siteWithAPIKeyCredential(ctx, item.ID)
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	mode, defaultBase := plugin.Manifest.BaseURLSettings()
	canonical, err := canonicalFromMode(nonEmpty(mode, "as_is"), defaultBase, item.BaseURL)
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	client, err := s.pluginProfileClient(ctx, item, plugin)
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	path := strings.TrimSpace(plugin.Manifest.Site.PricingPath)
	if path == "" {
		return adapter.PricingSnapshot{}, fmt.Errorf("plugin %s has no pricing path", plugin.Manifest.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, pluginPricingTimeout)
	defer cancel()
	payload, err := fetchJSON(ctx, client, canonical, path, credential.Secret)
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	parsed, err := plugin.CallPricingParseHook(s.withSitePluginContext(ctx, item, plugin), jsplugin.PricingParseContext{SiteType: item.SiteType}, payload)
	if breaker := jsplugin.DefaultCatalog().Breaker(); breaker != nil {
		breaker.RecordProbeResult(plugin, "parsePricing", err == nil)
	}
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	return pricingSnapshotFromPlugin(parsed, payload), nil
}

func fetchJSON(ctx context.Context, client *http.Client, base, path, secret string) (any, error) {
	target, err := jsplugin.ResolveProbeURL(base, path, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", jsplugin.KindURLRejected, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "application/json")
	httpClient := *client
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return jsplugin.SameOrigin(base, next.URL.String())
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginPricingBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPluginPricingBody {
		return nil, fmt.Errorf("%s: pricing response is too large", jsplugin.KindResponseTooLarge)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("pricing endpoint returned %d", resp.StatusCode)
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("pricing endpoint did not return JSON: %w", err)
	}
	return payload, nil
}

func pricingSnapshotFromPlugin(parsed jsplugin.PricingParseResult, raw any) adapter.PricingSnapshot {
	snapshot := adapter.PricingSnapshot{Raw: raw}
	for _, group := range parsed.Groups {
		entry := adapter.PricingGroup{GroupName: group.Name, DisplayName: group.DisplayName, IsAuto: group.Auto, Ratio: 1}
		if group.Ratio != nil {
			entry.Ratio = *group.Ratio
		}
		snapshot.Groups = append(snapshot.Groups, entry)
	}
	for _, item := range parsed.Items {
		entry := adapter.ModelPricing{
			ModelName:   item.Model,
			DisplayName: item.DisplayName,
			GroupName:   item.Group,
			BillingType: item.BillingType,
			Currency:    item.Currency,
			GroupRatio:  1,
		}
		setRatio := func(dst *float64, has *bool, value *float64) {
			if value != nil {
				*dst, *has = *value, true
			}
		}
		if item.GroupRatio != nil {
			entry.GroupRatio = *item.GroupRatio
		}
		setRatio(&entry.ModelRatio, &entry.HasModelRatio, item.ModelRatio)
		setRatio(&entry.CompletionRatio, &entry.HasCompletionRatio, item.CompletionRatio)
		setRatio(&entry.CacheRatio, &entry.HasCacheRatio, item.CacheRatio)
		setRatio(&entry.CreateCacheRatio, &entry.HasCreateCacheRatio, item.CreateCacheRatio)
		setRatio(&entry.CreateCache1hRatio, &entry.HasCreateCache1hRatio, item.CreateCache1hRatio)
		setRatio(&entry.ImageRatio, &entry.HasImageRatio, item.ImageRatio)
		setRatio(&entry.AudioRatio, &entry.HasAudioRatio, item.AudioRatio)
		setRatio(&entry.AudioCompletionRatio, &entry.HasAudioCompletionRatio, item.AudioCompletionRatio)
		setRatio(&entry.ModelPrice, &entry.HasModelPrice, item.ModelPrice)
		setRatio(&entry.InputValue, &entry.HasInputValue, item.InputValue)
		setRatio(&entry.OutputValue, &entry.HasOutputValue, item.OutputValue)
		setRatio(&entry.PerRequestValue, &entry.HasPerRequestValue, item.PerRequestValue)
		snapshot.Items = append(snapshot.Items, entry)
	}
	return snapshot
}

// pricingPlugins returns the parsePricing plugins for a site, highest
// priority first: mounted plugins in plugin_ids order, then the site type's
// own hook as the fallback.
func (s *Service) pricingPlugins(item store.Site) []*jsplugin.Plugin {
	plugins := s.siteHookPlugins(item, "parsePricing")
	if plugin, ok := s.siteTypeHookPlugin(item.SiteType, "parsePricing"); ok {
		plugins = append(plugins, plugin)
	}
	return plugins
}

// syncPluginPricing is the pricing step of a model refresh for a site with a
// pricing_parse plugin bound. bound is false when there is nothing to do.
// Several plugins are merged: a higher priority item or group replaces a lower
// one with the same name, and scalar fields keep the highest priority value.
// One plugin failing is logged and skipped; every plugin failing is an error.
func (s *Service) syncPluginPricing(ctx context.Context, item store.Site, models []store.SiteModel, now time.Time) (snapshot adapter.PricingSnapshot, groups []store.SitePricingGroup, pricings []store.SiteModelPricing, bound bool, err error) {
	plugins := s.pricingPlugins(item)
	if len(plugins) == 0 {
		return adapter.PricingSnapshot{}, nil, nil, false, nil
	}
	var failures []error
	var parts []adapter.PricingSnapshot
	for _, plugin := range plugins {
		part, fetchErr := s.fetchPluginPricing(ctx, item, plugin)
		if fetchErr != nil {
			slog.Default().Debug("parsePricing plugin failed", "plugin_id", plugin.Manifest.ID, "error", fetchErr)
			failures = append(failures, fmt.Errorf("pricing plugin %s: %w", plugin.Manifest.ID, fetchErr))
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return adapter.PricingSnapshot{}, nil, nil, true, failures[0]
	}
	snapshot = mergePricingSnapshots(parts)
	byName := make(map[string]store.SiteModel, len(models))
	for _, model := range models {
		byName[model.UpstreamName] = model
		byName[model.DisplayName] = model
	}
	groups, pricings, err = syncPricingState(ctx, item.ID, item.SiteType, snapshot, byName,
		store.NewSitePricingGroupRepository(s.db.DB()), store.NewSiteModelPricingRepository(s.db.DB()), now)
	if err != nil {
		return snapshot, nil, nil, true, fmt.Errorf("pricing plugin %s: %w", plugins[0].Manifest.ID, err)
	}
	return snapshot, groups, pricings, true, nil
}

// mergePricingSnapshots combines snapshots from highest priority to lowest.
// The first item for a model and group, and the first group of a name, win.
// Scalar fields are taken from the highest priority snapshot that sets them.
func mergePricingSnapshots(parts []adapter.PricingSnapshot) adapter.PricingSnapshot {
	if len(parts) == 1 {
		return parts[0]
	}
	var merged adapter.PricingSnapshot
	groups := map[string]bool{}
	items := map[string]bool{}
	for _, part := range parts {
		if merged.Raw == nil && part.Raw != nil {
			merged.Raw = part.Raw
		}
		for _, group := range part.Groups {
			if groups[group.GroupName] {
				continue
			}
			groups[group.GroupName] = true
			merged.Groups = append(merged.Groups, group)
		}
		for _, item := range part.Items {
			key := item.ModelName + "\x00" + item.GroupName
			if items[key] {
				continue
			}
			items[key] = true
			merged.Items = append(merged.Items, item)
		}
	}
	return merged
}

// PreviewPluginPricing fetches and parses a site's price table without saving
// anything, so an admin can read the result before binding the plugin.
func (s *Service) PreviewPluginPricing(ctx context.Context, siteID uuid.UUID, plugin *jsplugin.Plugin) (adapter.PricingSnapshot, error) {
	item, err := s.Get(ctx, siteID)
	if err != nil {
		return adapter.PricingSnapshot{}, err
	}
	return s.fetchPluginPricing(ctx, item, plugin)
}
