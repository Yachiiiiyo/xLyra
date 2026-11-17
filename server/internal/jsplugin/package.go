package jsplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xlyra/server/internal/store"
	"xlyra/server/internal/version"
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`)

// Manifest is the plugin package manifest.
type Manifest struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	APIVersion   int                 `json:"apiVersion"`
	HostAPI      int                 `json:"hostApi"`
	XLyra        string              `json:"xlyra"`
	Kind         string              `json:"kind,omitempty"`
	Exports      []string            `json:"exports,omitempty"`
	Description  string              `json:"description"`
	License      string              `json:"license"`
	Icon         string              `json:"icon,omitempty"`
	Permissions  []string            `json:"permissions,omitempty"`
	Settings     []SettingSpec       `json:"settings,omitempty"`
	QuotaProbe   QuotaProbeSection   `json:"quotaProbe"`
	Site         SiteSection         `json:"site"`
	Protocol     ProtocolSection     `json:"protocol"`
	Automation   AutomationSection   `json:"automation"`
	SiteProvider SiteProviderSection `json:"siteProvider,omitzero"`
	// Request is the top-level upstream header template. A site type uses it
	// only when siteProvider.request is empty. Other plugins may set headers.
	Request   SiteProviderRequest `json:"request,omitempty"`
	Transport TransportSection    `json:"transport,omitempty"`
	SHA256    map[string]string   `json:"sha256"`

	// hasQuotaProbe and hasSite record that the JSON object contained the
	// section, including {}. Kindless manifests use them to tell an empty
	// section from a missing one.
	hasQuotaProbe bool `json:"-"`
	hasSite       bool `json:"-"`
}

// QuotaProbeSection is the quota_probe manifest block.
type QuotaProbeSection struct {
	BaseURLMode    string `json:"baseURLMode"`
	DefaultBaseURL string `json:"defaultBaseURL,omitempty"`
	Replaces       string `json:"replaces,omitempty"`
}

// SiteSection is the manifest block for the site-facing kinds other than
// quota_probe (model_list, credential_check, site_detect).
type SiteSection struct {
	BaseURLMode    string `json:"baseURLMode,omitempty"`
	DefaultBaseURL string `json:"defaultBaseURL,omitempty"`
	// PricingPath is where xLyra fetches the site's price table for a
	// pricing_parse plugin. Required for that kind, ignored by the others.
	PricingPath string `json:"pricingPath,omitempty"`
}

// DeclaresSiteType reports whether the package declares a site type, either
// as a site_provider kind or as a kindless manifest with a siteProvider section.
func (m Manifest) DeclaresSiteType() bool {
	if m.Kind == KindSiteProvider {
		return true
	}
	return m.Kind == "" && strings.TrimSpace(m.SiteProvider.SiteType) != ""
}

// KindLabel is the kind to show and compare. A kindless site type is
// site_provider; any other kindless package is KindHooks. KindHooks is not a
// manifest kind: writing "kind": "hooks" is still unsupported.
func (m Manifest) KindLabel() string {
	if m.Kind != "" {
		return m.Kind
	}
	if m.DeclaresSiteType() {
		return KindSiteProvider
	}
	return KindHooks
}

// BaseURLSettings returns how the site address is prepared for the plugin.
func (m Manifest) BaseURLSettings() (mode, defaultBase string) {
	switch m.Kind {
	case KindQuotaProbe:
		return m.QuotaProbe.BaseURLMode, m.QuotaProbe.DefaultBaseURL
	case KindSiteProvider:
		// No site section. An empty mode is as_is, the same default quota probes use.
		return "as_is", m.SiteProvider.Protocol.OfficialBaseURL
	case "":
		if m.DeclaresSiteType() {
			return "as_is", m.SiteProvider.Protocol.OfficialBaseURL
		}
		if m.hasQuotaProbe {
			mode := m.QuotaProbe.BaseURLMode
			if mode == "" {
				mode = "as_is"
			}
			return mode, m.QuotaProbe.DefaultBaseURL
		}
		mode := m.Site.BaseURLMode
		if mode == "" {
			mode = "as_is"
		}
		return mode, m.Site.DefaultBaseURL
	default:
		return m.Site.BaseURLMode, m.Site.DefaultBaseURL
	}
}

// ProtocolSection is the protocol manifest block. Method and Auth are applied
// by Go; the plugin never sees the upstream credential.
type ProtocolSection struct {
	Name           string         `json:"name"`
	DownstreamPath string         `json:"downstreamPath"`
	EndpointType   string         `json:"endpointType"`
	Method         string         `json:"method"`
	Auth           string         `json:"auth"`
	DefaultBaseURL string         `json:"defaultBaseURL,omitempty"`
	Stream         *StreamSection `json:"stream,omitempty"`
}

// AuthHeader returns the header that carries the credential, or "" for auth "none".
func (s ProtocolSection) AuthHeader() string {
	switch {
	case s.Auth == "bearer":
		return "Authorization"
	case strings.HasPrefix(s.Auth, "header:"):
		return strings.TrimSpace(strings.TrimPrefix(s.Auth, "header:"))
	default:
		return ""
	}
}

// Plugin is a loaded, compiled plugin of one kind.
type Plugin struct {
	Manifest       Manifest
	program        *program
	pool           *pool
	hotPool        *pool
	fixtures       []Fixture
	gen            int64
	source         string
	packageSHA256  string
	stateMu        sync.RWMutex
	globalSettings map[string]any
	defaultStorage StorageBackend
	storageRepo    store.JSPluginStorageRepository
	hasStorageRepo bool
}

type callStorageCtxKey struct{}
type callSecretBagCtxKey struct{}
type callSiteSettingsCtxKey struct{}
type callStorageScopeCtxKey struct{}
type discardLogsCtxKey struct{}

type storageScope struct {
	scopeType string
	scopeID   string
}

// WithCallStorage attaches a StorageBackend for the duration of a hook call.
func WithCallStorage(ctx context.Context, backend StorageBackend) context.Context {
	if backend == nil {
		return ctx
	}
	return context.WithValue(ctx, callStorageCtxKey{}, backend)
}

// WithStorageScope selects the plugin's private KV scope for a hook call.
// An explicit WithCallStorage backend still wins. An empty scopeID is ignored.
func WithStorageScope(ctx context.Context, scopeType, scopeID string) context.Context {
	if ctx == nil || strings.TrimSpace(scopeID) == "" {
		return ctx
	}
	return context.WithValue(ctx, callStorageScopeCtxKey{}, storageScope{scopeType: scopeType, scopeID: scopeID})
}

// WithCallSecretBag attaches a SecretBag so storage.set rejects values that
// leak secrets held by the bag.
func WithCallSecretBag(ctx context.Context, bag *SecretBag) context.Context {
	if bag == nil {
		return ctx
	}
	return context.WithValue(ctx, callSecretBagCtxKey{}, bag)
}

// WithDiscardLogs drops log lines collected during a hook call. signRequest
// uses it while a plain credential is visible to the plugin.
func WithDiscardLogs(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, discardLogsCtxKey{}, true)
}

func discardLogsFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	discard, _ := ctx.Value(discardLogsCtxKey{}).(bool)
	return discard
}

// WithSiteSettings attaches per-site setting overrides for a hook call.
func WithSiteSettings(ctx context.Context, settings map[string]any) context.Context {
	if len(settings) == 0 {
		return ctx
	}
	return context.WithValue(ctx, callSiteSettingsCtxKey{}, settings)
}

func storageFromContext(ctx context.Context) StorageBackend {
	if ctx == nil {
		return nil
	}
	backend, _ := ctx.Value(callStorageCtxKey{}).(StorageBackend)
	return backend
}

func storageScopeFromContext(ctx context.Context) (storageScope, bool) {
	if ctx == nil {
		return storageScope{}, false
	}
	scope, ok := ctx.Value(callStorageScopeCtxKey{}).(storageScope)
	if !ok || strings.TrimSpace(scope.scopeID) == "" {
		return storageScope{}, false
	}
	return scope, true
}

func secretBagFromContext(ctx context.Context) *SecretBag {
	if ctx == nil {
		return nil
	}
	bag, _ := ctx.Value(callSecretBagCtxKey{}).(*SecretBag)
	return bag
}

func siteSettingsFromContext(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	settings, _ := ctx.Value(callSiteSettingsCtxKey{}).(map[string]any)
	return settings
}

// PackageSHA256 is the canonical digest of the package this plugin was compiled
// from. Builtins and plugins built with NewPlugin leave it empty.
func (p *Plugin) PackageSHA256() string {
	if p == nil {
		return ""
	}
	return p.packageSHA256
}

// ProbeType is the Go probe type this plugin replaces, such as "kimi".
func (p *Plugin) ProbeType() string {
	return p.Manifest.QuotaProbe.Replaces
}

// ProtocolName is the gateway protocol name from the manifest.
func (p *Plugin) ProtocolName() string {
	return p.Manifest.Protocol.Name
}

// Fixtures returns the package samples.
func (p *Plugin) Fixtures() []Fixture {
	if p == nil {
		return nil
	}
	return append([]Fixture(nil), p.fixtures...)
}

// AllowsStorage reports whether the plugin manifest declares the "storage" permission.
func (p *Plugin) AllowsStorage() bool {
	if p == nil {
		return false
	}
	return contains(p.Manifest.Permissions, PermissionStorage)
}

// SetGlobalSettings updates the plugin-wide admin setting overrides.
func (p *Plugin) SetGlobalSettings(settings map[string]any) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	p.globalSettings = maps.Clone(settings)
	p.stateMu.Unlock()
}

// SetDefaultStorage sets the fallback StorageBackend when a call context does
// not supply a narrower scope.
func (p *Plugin) SetDefaultStorage(backend StorageBackend) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	p.defaultStorage = backend
	p.stateMu.Unlock()
}

// SetStorageRepository remembers the plugin storage repository and uses its
// global scope as the fallback backend.
func (p *Plugin) SetStorageRepository(repo store.JSPluginStorageRepository) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	p.storageRepo = repo
	p.hasStorageRepo = true
	p.defaultStorage = NewStoreStorage(repo, p.Manifest.ID, StorageScopeGlobal, "")
	p.stateMu.Unlock()
}

// EffectiveSettings returns the resolved settings map (defaults -> global -> site).
// It returns nil when the manifest declares no settings.
func (p *Plugin) EffectiveSettings(siteOverrides map[string]any) map[string]any {
	if p == nil || len(p.Manifest.Settings) == 0 {
		return nil
	}
	p.stateMu.RLock()
	global := p.globalSettings
	p.stateMu.RUnlock()
	return ResolveSettings(p.Manifest.Settings, global, siteOverrides)
}

// HasSeparateHotPool reports whether the plugin allocated dedicated hot-path and
// cold-path runtime pools.
func (p *Plugin) HasSeparateHotPool() bool {
	return p != nil && p.hotPool != nil && p.pool != nil && p.hotPool != p.pool
}

func isHotHookName(name string) bool {
	switch name {
	case "decodeRequest", "buildRequest", "parseResponse", HookParseError, HookSignRequest,
		"classify", "classifyError", "rewritePayload", "transformStream":
		return true
	default:
		return false
	}
}

func classifyProgramHooks(hooks map[string]any) (hasHot, hasCold bool) {
	for name := range hooks {
		if isHotHookName(name) {
			hasHot = true
		} else {
			hasCold = true
		}
	}
	return hasHot, hasCold
}

// NewPlugin compiles one module and checks its manifest. resident is the
// number of runtimes kept warm; builtins use the default pool size.
func NewPlugin(manifest Manifest, source string, fixtures []Fixture, resident int) (*Plugin, error) {
	if manifest.Kind == KindQuotaProbe && manifest.QuotaProbe.BaseURLMode == "" {
		manifest.QuotaProbe.BaseURLMode = "as_is"
	}
	if spec, ok := lookupKind(manifest.Kind); ok && spec.Section == "site" && manifest.Site.BaseURLMode == "" {
		manifest.Site.BaseURLMode = "as_is"
	}
	if manifest.Kind == KindProtocol {
		manifest.Protocol.Method = strings.ToUpper(strings.TrimSpace(manifest.Protocol.Method))
		if manifest.Protocol.Method == "" {
			manifest.Protocol.Method = "POST"
		}
		if manifest.Protocol.Auth == "" {
			manifest.Protocol.Auth = "bearer"
		}
	}
	if err := validateManifest(manifest, source); err != nil {
		return nil, err
	}
	program, err := compileProgram(manifest.ID+".js", source, kindHooksOf(manifest.Kind)...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if spec, ok := lookupKind(manifest.Kind); ok {
		if err := program.addOptionalHooks(spec.OptionalHooks); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
	}
	if manifest.DeclaresSiteType() {
		if err := program.requireHooks(accountPoolRequiredExports(manifest.SiteProvider)); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
	}
	// Register any additional known hooks exported by the module so selftest and
	// multi-hook dispatch can invoke them.
	if err := program.addOptionalHooks(allKnownOptionalHooks); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if err := rejectListModelsWithoutPerAccount(manifest, program); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if manifest.Kind == KindProtocol {
		if _, signs := program.hooks[HookSignRequest]; signs && manifest.Protocol.Auth != "none" {
			return nil, fmt.Errorf("%s: signRequest needs protocol.auth \"none\", so the credential is only used for signing", manifest.ID)
		}
	}
	loaded, err := newSession(program)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if spec, ok := lookupKind(manifest.Kind); ok {
		if err := loaded.requireCallableHooks(spec.OptionalHooks); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
	}
	if err := loaded.requireCallableHooks(allKnownOptionalHooks); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if manifest.DeclaresSiteType() {
		if err := loaded.requireExports(accountPoolRequiredExports(manifest.SiteProvider)); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
	}
	if manifest.Kind == "" {
		if err := rejectKindlessRestrictedHooks(program); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
		if !manifest.DeclaresSiteType() && len(program.knownHooks()) == 0 {
			return nil, fmt.Errorf("%s: plugin exports no known hook", manifest.ID)
		}
	}
	if err := exportsMatchManifest(manifest, program); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if err := aliasHookNames(program); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	meta, err := loaded.exportMeta()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	if err := metaMatches(manifest, meta); err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	hasHot, hasCold := false, false
	for name := range program.hooks {
		if isHotHookName(name) {
			hasHot = true
		} else {
			hasCold = true
		}
	}
	spec, _ := lookupKind(manifest.Kind)
	primaryHot := spec.HotPath || (hasHot && !hasCold)
	maxPool := probePoolMax
	primaryResident := resident
	if primaryHot {
		if primaryResident == 0 {
			primaryResident = protocolPoolResident
		}
		maxPool = protocolPoolMax
	} else if primaryResident == 0 {
		primaryResident = probePoolResident
	}
	plugin := &Plugin{Manifest: manifest, program: program, fixtures: fixtures, gen: 1, source: source}
	pool, err := newPool(primaryResident, maxPool, func() (*session, error) {
		return newSession(program)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifest.ID, err)
	}
	plugin.pool = pool
	if !primaryHot && hasHot {
		hotResident := protocolPoolResident
		if resident > 0 && resident < hotResident {
			hotResident = resident
		}
		hotPool, err := newPool(hotResident, protocolPoolMax, func() (*session, error) {
			return newSession(program)
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", manifest.ID, err)
		}
		plugin.hotPool = hotPool
	}
	return plugin, nil
}

// KnownHookNames lists every hook a module may export, in a stable order.
func KnownHookNames() []string {
	return append([]string(nil), allKnownOptionalHooks...)
}

var allKnownOptionalHooks = []string{
	"probe", "listModels", "check", "classify", "classifyError", "rewritePayload",
	"parsePricing", "detect", "describeModels",
	"startLogin", "pollLogin", "refreshTokens", "summarizeAccount",
	"decodeRequest", "buildRequest", "parseResponse", HookParseError, HookSignRequest,
	"handle", "onEvent", "onSchedule",
}

func (p *Plugin) poolForHook(hookName string) *pool {
	if p == nil {
		return nil
	}
	if isHotHookName(hookName) && p.hotPool != nil {
		return p.hotPool
	}
	return p.pool
}

func (p *Plugin) buildCallEnv(ctx context.Context) callEnv {
	allows := p.AllowsStorage()
	var backend StorageBackend
	if allows {
		backend = storageFromContext(ctx)
		p.stateMu.RLock()
		if backend == nil {
			if scope, ok := storageScopeFromContext(ctx); ok && p.hasStorageRepo {
				backend = NewStoreStorage(p.storageRepo, p.Manifest.ID, scope.scopeType, scope.scopeID)
			}
		}
		if backend == nil {
			backend = p.defaultStorage
		}
		p.stateMu.RUnlock()
		if bag := secretBagFromContext(ctx); bag != nil {
			backend = GuardStorage(backend, bag)
		}
	}
	return callEnv{
		ctx:           ctx,
		allowsStorage: allows,
		storage:       backend,
	}
}

func (p *Plugin) injectContextSettings(ctx context.Context, arg any) any {
	if p == nil || len(p.Manifest.Settings) == 0 {
		return arg
	}
	ctxMap, ok := arg.(map[string]any)
	if !ok || ctxMap == nil {
		return arg
	}
	cloned := maps.Clone(ctxMap)
	var explicit map[string]any
	if existing, ok := cloned["settings"].(map[string]any); ok {
		explicit = existing
	}
	siteOverrides := siteSettingsFromContext(ctx)
	p.stateMu.RLock()
	global := p.globalSettings
	p.stateMu.RUnlock()
	cloned["settings"] = ResolveSettings(p.Manifest.Settings, global, siteOverrides, explicit)
	return cloned
}

// CallProbe runs the probe hook once.
func (p *Plugin) CallProbe(ctx context.Context, probeCtx ProbeContext, steps []ProbeStep) (Decision, error) {
	targetPool := p.poolForHook("probe")
	if p == nil || targetPool == nil {
		return Decision{}, fmt.Errorf("plugin is not loaded")
	}
	started := time.Now()
	session, err := targetPool.Acquire(ctx)
	if err != nil {
		return Decision{}, annotate(p, err, "probe", len(steps), time.Since(started).Milliseconds())
	}
	hook, ok := p.program.hooks["probe"]
	if !ok {
		targetPool.Release(session, true)
		return Decision{}, fmt.Errorf("probe hook is missing")
	}
	ctxArg := p.injectContextSettings(ctx, probeCtx.asMap())
	out, logs, dropped, discard, callErr := session.callWithEnv(ctx, probeHookTimeout, hook, p.buildCallEnv(ctx), []any{ctxArg, stepsAsValue(steps)})
	targetPool.Release(session, discard || fatalRuntime(callErr))
	duration := time.Since(started).Milliseconds()
	if callErr != nil {
		return Decision{Logs: logs, DroppedLogs: dropped}, annotate(p, callErr, "probe", len(steps), duration)
	}
	decision, err := decodeDecision(out)
	if err != nil {
		return Decision{Logs: logs, DroppedLogs: dropped}, annotate(p, err, "probe", len(steps), duration)
	}
	decision.Logs = logs
	decision.DroppedLogs = dropped
	return decision, nil
}

func kindHooksOf(kind string) []string {
	spec, _ := lookupKind(kind)
	return spec.Hooks
}

func validateBaseURLMode(mode string) error {
	switch mode {
	case "trim_v1", "trim_v1_fold", "origin", "as_is":
		return nil
	}
	return fmt.Errorf("baseURLMode %q is not supported", mode)
}

func validateManifest(manifest Manifest, source string) error {
	return validateManifestFiles(manifest, source, nil)
}

func validateManifestFiles(manifest Manifest, source string, files map[string][]byte) error {
	if !pluginIDPattern.MatchString(manifest.ID) || len(manifest.ID) < 3 || len(manifest.ID) > 64 {
		return fmt.Errorf("invalid plugin id %q", manifest.ID)
	}
	if manifest.APIVersion != hookAPIVersion {
		return fmt.Errorf("apiVersion %d is not supported", manifest.APIVersion)
	}
	if manifest.HostAPI > hostAPIVersion {
		return fmt.Errorf("hostApi %d is not supported", manifest.HostAPI)
	}
	if manifest.HostAPI < 1 {
		return fmt.Errorf("hostApi must be at least 1")
	}
	if err := VersionAllowed(manifest.XLyra, version.Current().Version); err != nil {
		return err
	}
	sum, ok := manifest.SHA256["plugin.js"]
	if !ok || !strings.EqualFold(sum, hashSource(source)) {
		return fmt.Errorf("plugin.js sha256 does not match the manifest")
	}
	if err := validateManifestPermissions(manifest.Permissions); err != nil {
		return err
	}
	if err := validateSettingSpecs(manifest.Settings); err != nil {
		return err
	}
	if !manifest.DeclaresSiteType() && siteProviderRequestPresent(manifest.SiteProvider.Request) {
		return fmt.Errorf("request is not allowed for %s", manifest.KindLabel())
	}
	if err := validateManifestRequest(manifest); err != nil {
		return err
	}
	if err := validateTransport(manifest.Transport); err != nil {
		return err
	}
	switch manifest.Kind {
	case KindQuotaProbe:
		if err := validateBaseURLMode(manifest.QuotaProbe.BaseURLMode); err != nil {
			return fmt.Errorf("quotaProbe.%w", err)
		}
		if manifest.QuotaProbe.Replaces != "" && !strings.HasPrefix(manifest.ID, "xlyra.") {
			return fmt.Errorf("only xlyra plugins can set quotaProbe.replaces")
		}
	case KindProtocol:
		if strings.TrimSpace(manifest.Protocol.Name) == "" {
			return fmt.Errorf("protocol.name is required")
		}
		if strings.TrimSpace(manifest.Protocol.DownstreamPath) == "" {
			return fmt.Errorf("protocol.downstreamPath is required")
		}
		if strings.TrimSpace(manifest.Protocol.EndpointType) == "" {
			return fmt.Errorf("protocol.endpointType is required")
		}
		switch manifest.Protocol.Auth {
		case "none", "bearer":
		default:
			if !strings.HasPrefix(manifest.Protocol.Auth, "header:") {
				return fmt.Errorf("protocol.auth %q is not supported", manifest.Protocol.Auth)
			}
		}
		if err := validateStreamSection(manifest.Protocol.Stream, "protocol.stream"); err != nil {
			return err
		}
	case KindModelList, KindCredentialCheck, KindSiteDetect:
		if mode := manifest.Site.BaseURLMode; mode != "" {
			if err := validateBaseURLMode(mode); err != nil {
				return fmt.Errorf("site.%w", err)
			}
		}
	case KindPricingParse:
		if mode := manifest.Site.BaseURLMode; mode != "" {
			if err := validateBaseURLMode(mode); err != nil {
				return fmt.Errorf("site.%w", err)
			}
		}
		if err := validatePluginPath(manifest.Site.PricingPath); err != nil {
			return fmt.Errorf("site.pricingPath: %w", err)
		}
	case KindAutomation:
		if err := validateAutomationSection(manifest.Automation); err != nil {
			return err
		}
	case KindErrorClassifier, KindModelMetadata:
		// No manifest section: these kinds are pure functions of their input.
	case KindSiteProvider:
		if err := validateSiteProvider(manifest, files); err != nil {
			return err
		}
	case "":
		if err := validateKindlessManifest(manifest, files); err != nil {
			return err
		}
	default:
		return unsupportedKindError(manifest.Kind)
	}
	return nil
}

// validateKindlessManifest checks a manifest that omits kind. protocol and
// automation still need a kind in this stage.
func validateKindlessManifest(manifest Manifest, files map[string][]byte) error {
	if manifest.Protocol != (ProtocolSection{}) {
		return fmt.Errorf("protocol requires \"kind\": \"protocol\"")
	}
	if automationSectionPresent(manifest) {
		return fmt.Errorf("automation requires \"kind\": \"automation\"")
	}
	if manifest.hasQuotaProbe {
		mode := manifest.QuotaProbe.BaseURLMode
		if mode == "" {
			mode = "as_is"
		}
		if err := validateBaseURLMode(mode); err != nil {
			return fmt.Errorf("quotaProbe.%w", err)
		}
		if manifest.QuotaProbe.Replaces != "" && !strings.HasPrefix(manifest.ID, "xlyra.") {
			return fmt.Errorf("only xlyra plugins can set quotaProbe.replaces")
		}
	}
	if manifest.hasSite {
		mode := manifest.Site.BaseURLMode
		if mode == "" {
			mode = "as_is"
		}
		if err := validateBaseURLMode(mode); err != nil {
			return fmt.Errorf("site.%w", err)
		}
		if manifest.Site.PricingPath != "" {
			if err := validatePluginPath(manifest.Site.PricingPath); err != nil {
				return fmt.Errorf("site.pricingPath: %w", err)
			}
		}
	}
	if manifest.DeclaresSiteType() {
		if err := validateSiteProvider(manifest, files); err != nil {
			return err
		}
	}
	return nil
}

func automationSectionPresent(manifest Manifest) bool {
	form := manifest.Automation.Form
	return len(manifest.Automation.Subscribes) > 0 || len(manifest.Automation.Permissions) > 0 || len(manifest.Automation.Inputs) > 0 ||
		manifest.Automation.Schedule != (AutomationSchedule{}) ||
		len(form.Title) > 0 || len(form.Description) > 0 || len(form.AddLabel) > 0 || len(form.EmptyText) > 0
}

// kindlessRestrictedHooks still belong to a kind in this stage.
var kindlessRestrictedHooks = []string{"decodeRequest", "buildRequest", "parseResponse", "handle", "onEvent"}

func rejectKindlessRestrictedHooks(program *program) error {
	for _, name := range kindlessRestrictedHooks {
		if _, ok := program.hooks[name]; ok {
			switch name {
			case "decodeRequest", "buildRequest", "parseResponse":
				return fmt.Errorf("export %s requires \"kind\": \"protocol\"", name)
			default:
				return fmt.Errorf("export %s requires \"kind\": \"automation\"", name)
			}
		}
	}
	return nil
}

// aliasHookNames registers the other name of a pair when the module exported
// only one of them, so callers of either name hit the same function.
func aliasHookNames(program *program) error {
	pairs := [][2]string{{"classify", "classifyError"}, {"handle", "onEvent"}}
	for _, pair := range pairs {
		left, right := pair[0], pair[1]
		_, hasLeft := program.hooks[left]
		_, hasRight := program.hooks[right]
		if hasLeft == hasRight {
			continue
		}
		source, target := left, right
		if hasRight {
			source, target = right, left
		}
		hook, err := program.mod.Hook(source)
		if err != nil {
			return fmt.Errorf("%s hook: %w", source, err)
		}
		program.hooks[target] = hook
	}
	return nil
}

// ExportedHooks returns the known hooks a module exports, sorted. Aliases are
// not included. The CLI writes them into the built manifest.
func ExportedHooks(source string) ([]string, error) {
	program, err := compileProgram("plugin.js", source)
	if err != nil {
		return nil, err
	}
	if err := program.addOptionalHooks(allKnownOptionalHooks); err != nil {
		return nil, err
	}
	return program.knownHooks(), nil
}

// knownHooks lists the known hooks the module itself exported, before aliases.
func (p *program) knownHooks() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, name := range allKnownOptionalHooks {
		if _, ok := p.hooks[name]; ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func exportsMatchManifest(manifest Manifest, program *program) error {
	if len(manifest.Exports) == 0 {
		return nil
	}
	got := program.knownHooks()
	want := append([]string(nil), manifest.Exports...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf("manifest exports do not match the module")
	}
	return nil
}

func metaMatches(manifest Manifest, meta map[string]any) error {
	if got, _ := meta["id"].(string); got != manifest.ID {
		return fmt.Errorf("meta.id %q does not match manifest", got)
	}
	got, hasKind := meta["kind"].(string)
	if manifest.Kind == "" {
		if hasKind && got != "" {
			return fmt.Errorf("meta.kind %q does not match manifest", got)
		}
	} else if got != manifest.Kind {
		return fmt.Errorf("meta.kind %q does not match manifest", got)
	}
	if !metaVersion(meta["apiVersion"], int64(manifest.APIVersion)) {
		return fmt.Errorf("meta.apiVersion does not match manifest")
	}
	return nil
}

func metaVersion(value any, want int64) bool {
	switch typed := value.(type) {
	case int64:
		return typed == want
	case int:
		return int64(typed) == want
	case float64:
		return typed == float64(want)
	default:
		return false
	}
}

func hashSource(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// VersionAllowed reports whether current satisfies an xLyra constraint such as ">=1.15.0".
// An empty current, or current "dev", always passes. An empty constraint always passes.
func VersionAllowed(constraint, current string) error {
	current = strings.TrimSpace(current)
	if current == "" || current == "dev" {
		return nil
	}
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	for _, part := range strings.Fields(constraint) {
		if !strings.HasPrefix(part, ">=") {
			return fmt.Errorf("unsupported xlyra constraint %q", part)
		}
		if semverLess(current, strings.TrimPrefix(part, ">=")) {
			return fmt.Errorf("xlyra %s does not satisfy %s", current, constraint)
		}
	}
	return nil
}

func semverLess(current, minimum string) bool {
	left := semverParts(current)
	right := semverParts(minimum)
	for i := 0; i < 3; i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

func semverParts(value string) [3]int {
	value, _, _ = strings.Cut(value, "-")
	parts := strings.Split(value, ".")
	var out [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		number, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}
		}
		out[i] = number
	}
	return out
}

// CallDecodeRequest runs the decodeRequest hook once.
func (p *Plugin) CallDecodeRequest(ctx context.Context, endpointCtx ProtocolEndpointContext, payload map[string]any) (ProtocolDecodeResult, *ProtocolDecodeFailure, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, "decodeRequest", protocolHookTimeout, 0, endpointCtx.asMap(), payload)
	if err != nil {
		return ProtocolDecodeResult{}, nil, logs, dropped, err
	}
	result, failure, decodeErr := decodeProtocolDecode(out)
	return result, failure, logs, dropped, decodeErr
}

// CallBuildRequest runs the buildRequest hook once.
func (p *Plugin) CallBuildRequest(ctx context.Context, buildCtx ProtocolBuildContext, payload map[string]any) (ProtocolBuiltRequest, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, "buildRequest", protocolHookTimeout, 0, buildCtx.asMap(), payload)
	if err != nil {
		return ProtocolBuiltRequest{}, logs, dropped, err
	}
	built, decodeErr := decodeProtocolBuild(out)
	return built, logs, dropped, decodeErr
}

// HasHook reports whether the plugin exports the hook (optional hooks may be absent).
func (p *Plugin) HasHook(name string) bool {
	if p == nil || p.program == nil {
		return false
	}
	_, ok := p.program.hooks[name]
	return ok
}

// CallSignRequest runs the optional signRequest hook once.
func (p *Plugin) CallSignRequest(ctx context.Context, buildCtx ProtocolBuildContext, input ProtocolSignInput) (ProtocolSignResult, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, HookSignRequest, signHookTimeout, 0, buildCtx.asMap(), signInputAsMap(input))
	if err != nil {
		return ProtocolSignResult{}, logs, dropped, err
	}
	signed, decodeErr := decodeProtocolSign(out)
	return signed, logs, dropped, decodeErr
}

// CallParseError runs the optional parseError hook once.
func (p *Plugin) CallParseError(ctx context.Context, buildCtx ProtocolBuildContext, input ProtocolErrorInput) (ProtocolErrorResult, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, HookParseError, protocolHookTimeout, 0, buildCtx.asMap(), parseInputAsMap(input))
	if err != nil {
		return ProtocolErrorResult{}, logs, dropped, err
	}
	parsed, decodeErr := decodeProtocolError(out)
	return parsed, logs, dropped, decodeErr
}

// CallParseResponse runs the parseResponse hook once.
func (p *Plugin) CallParseResponse(ctx context.Context, buildCtx ProtocolBuildContext, input ProtocolParseInput) (ProtocolParsedResponse, []LogEntry, int, error) {
	out, logs, dropped, err := p.callHook(ctx, "parseResponse", protocolHookTimeout, 0, buildCtx.asMap(), parseInputAsMap(input))
	if err != nil {
		return ProtocolParsedResponse{}, logs, dropped, err
	}
	parsed, decodeErr := decodeProtocolParse(out)
	return parsed, logs, dropped, decodeErr
}

func (p *Plugin) callHook(ctx context.Context, hookName string, timeout time.Duration, step int, args ...any) (any, []LogEntry, int, error) {
	targetPool := p.poolForHook(hookName)
	if p == nil || targetPool == nil {
		return nil, nil, 0, fmt.Errorf("plugin is not loaded")
	}
	hook, ok := p.program.hooks[hookName]
	if !ok {
		return nil, nil, 0, fmt.Errorf("%s hook is missing", hookName)
	}
	if len(args) > 0 {
		args[0] = p.injectContextSettings(ctx, args[0])
	}
	started := time.Now()
	session, err := targetPool.Acquire(ctx)
	if err != nil {
		return nil, nil, 0, annotate(p, err, hookName, step, time.Since(started).Milliseconds())
	}
	out, logs, dropped, discard, callErr := session.callWithEnv(ctx, timeout, hook, p.buildCallEnv(ctx), args)
	targetPool.Release(session, discard || fatalRuntime(callErr))
	duration := time.Since(started).Milliseconds()
	if discardLogsFromContext(ctx) {
		logs, dropped = nil, 0
		if call, ok := callErr.(*CallError); ok {
			call.Logs = nil
			call.DroppedLogs = 0
		}
	}
	if callErr != nil {
		return nil, logs, dropped, annotate(p, callErr, hookName, step, duration)
	}
	return out, logs, dropped, nil
}

// HashSource is the sha256 used in a manifest, exposed for tests.
func HashSource(source string) string { return hashSource(source) }

// ParseManifest decodes a manifest JSON document.
func ParseManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return Manifest{}, err
	}
	_, manifest.hasQuotaProbe = present["quotaProbe"]
	_, manifest.hasSite = present["site"]
	return manifest, nil
}
