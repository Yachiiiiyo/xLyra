package agentctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"

	"xlyra/server/internal/agent/capabilities"
	"xlyra/server/internal/auth"
	"xlyra/server/internal/catalog"
	"xlyra/server/internal/cloudclient"
	"xlyra/server/internal/config"
	"xlyra/server/internal/dashboard"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/jsplugin/market"
	"xlyra/server/internal/ratelimit"
	routeengine "xlyra/server/internal/router"
	"xlyra/server/internal/site"
	"xlyra/server/internal/store"
	"xlyra/server/internal/usage"
)

// Deps holds all server subsystems that the transport-agnostic control-plane Engine operates on.
type Deps struct {
	Version         string
	Config          *config.Config
	ConfFile        *config.ConfigFile
	DB              *store.Store
	Auth            *auth.Service
	RateLimits      *ratelimit.Service
	Sites           *site.Service
	Catalog         *catalog.Service
	Router          *routeengine.Service
	Dashboard       *dashboard.Service
	Usage           *usage.Service
	Caps            *capabilities.Store
	JSPlugins       *jsplugin.Manager
	PluginMarket    *market.Service
	Cloud           *cloudclient.Client
	Approver         Approver
	Logger           *slog.Logger
	OnConfigChanged  func(domain string)
	OnCatalogChanged func()
}

// Engine executes normalized control-plane requests independent of transport (UDS CLI, MCP, tests).
type Engine struct {
	mu   sync.RWMutex
	deps Deps
}

// NewEngine constructs a transport-agnostic control-plane engine.
func NewEngine(deps Deps) *Engine {
	if deps.RateLimits == nil && deps.DB != nil {
		deps.RateLimits = ratelimit.NewService(deps.DB)
	}
	return &Engine{deps: deps}
}

// SetCaps attaches the Agent capability store after runner initialization.
func (e *Engine) SetCaps(caps *capabilities.Store) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deps.Caps = caps
}

func (e *Engine) capsStore() *capabilities.Store {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.deps.Caps
}

// Deps returns a snapshot of configured dependencies for read-only inspection.
func (e *Engine) Deps() Deps {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.deps
}

type mutationPlan struct {
	Tier           int
	Revision       string
	Summary        string
	Diff           []DiffItem
	NoOp           bool
	AppliedEffects []string
	Commit         func(ctx context.Context) (any, error)
}

type callResult struct {
	Data           any
	Diff           []DiffItem
	Meta           *Meta
	Plan           *mutationPlan
}

// Execute validates protocol, safety tier, capability policy, approval, redaction, and size budget.
func (e *Engine) Execute(ctx context.Context, caller Caller, req CallRequest) (int, Envelope) {
	if req.Protocol != ProtocolVersion {
		return http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidProtocol,
				fmt.Sprintf("unsupported protocol version %d, want %d", req.Protocol, ProtocolVersion)),
		)
	}
	req.Resource = strings.ToLower(strings.TrimSpace(req.Resource))
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Resource == "" || req.Action == "" {
		return http.StatusBadRequest, errorEnvelope(
			newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "resource and action are required"),
		)
	}

	spec, ok := LookupOpSpec(req.Resource, req.Action)
	if !ok {
		return http.StatusNotFound, errorEnvelope(
			newCallError(http.StatusNotFound, ErrCodeNotFound,
				fmt.Sprintf("unknown operation %s/%s", req.Resource, req.Action)).
				withHint("请运行 `xlyra schema` 查看当前服务端支持的操作，或检查 CLI 与服务端版本是否匹配"),
		)
	}

	// Tier 0 operations are unconditionally forbidden regardless of capability policy or dry-run.
	if spec.Tier == Tier0Forbidden {
		err := newCallError(http.StatusForbidden, ErrCodePolicyTier0Denied,
			fmt.Sprintf("operation %s/%s is forbidden by Tier 0 security policy", spec.Resource, spec.Action)).
			withTier(Tier0Forbidden)
		e.recordAudit(ctx, caller, spec, req, nil, false, err.Detail.Code, nil)
		return err.Status, errorEnvelope(err)
	}

	if caller.Policy != nil {
		if allowed, reason := caller.Policy.AllowsOp(spec); !allowed {
			err := newCallError(http.StatusForbidden, ErrCodeCapabilityDenied, reason).withTier(spec.Tier)
			e.recordAudit(ctx, caller, spec, req, nil, false, err.Detail.Code, nil)
			return err.Status, errorEnvelope(err)
		}
	}

	if !spec.Implemented {
		err := newCallError(http.StatusNotImplemented, ErrCodeNotImplemented,
			fmt.Sprintf("operation %s/%s is reserved in schema but not yet implemented", spec.Resource, spec.Action)).
			withTier(spec.Tier).
			withHint("use read operations or implemented mutations; see `xlyra schema`")
		return err.Status, errorEnvelope(err)
	}

	res, callErr := e.dispatch(ctx, caller, spec, req)
	if callErr != nil {
		if callErr.Tier == 0 {
			callErr.Tier = spec.Tier
		}
		if spec.Tier >= Tier2Managed || callErr.Detail.Code == ErrCodePolicyTier0Denied {
			e.recordAudit(ctx, caller, spec, req, nil, false, callErr.Detail.Code, nil)
		}
		return callErr.Status, errorEnvelope(callErr)
	}

	// Non-mutating reads and dry-run previews skip approval and disk commit.
	if res.Plan == nil {
		env := e.finalizeEnvelope(res.Data, res.Diff, res.Meta, spec.Tier, req.Options)
		return http.StatusOK, env
	}

	plan := res.Plan
	effectiveTier := spec.Tier
	if plan.Tier > effectiveTier {
		effectiveTier = plan.Tier
	}
	if req.Options.DryRun || plan.NoOp {
		data := res.Data
		if data == nil {
			data = map[string]any{
				"dry_run": req.Options.DryRun,
				"changed": !plan.NoOp && len(plan.Diff) > 0,
				"summary": plan.Summary,
			}
		}
		meta := &Meta{
			Tier:           effectiveTier,
			DryRun:         req.Options.DryRun,
			Revision:       plan.Revision,
			AppliedEffects: nil,
		}
		if plan.NoOp {
			meta.AppliedEffects = []string{"no_changes"}
		}
		env := e.finalizeEnvelope(data, plan.Diff, meta, effectiveTier, req.Options)
		return http.StatusOK, env
	}

	// Check per-run mutation quota before requesting approval.
	if err := caller.Quota.ConsumeWrite(); err != nil {
		ce := asCallError(err, effectiveTier)
		e.recordAudit(ctx, caller, spec, req, plan.Diff, false, ce.Detail.Code, nil)
		return ce.Status, errorEnvelope(ce)
	}

	// Gate Tier 2/3 writes through the Approver (single confirmation card or classifier).
	if effectiveTier >= Tier2Managed {
		approver := caller.Approver
		if approver == nil {
			approver = e.deps.Approver
		}
		if approver == nil {
			if strings.EqualFold(caller.PermissionMode, "full") {
				approver = ApproverFunc(func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
					return ApprovalDecision{Granted: true}, nil
				})
			} else {
				err := newCallError(http.StatusForbidden, ErrCodeApprovalDenied,
					"no control-plane approver configured for non-full permission mode").withTier(effectiveTier)
				e.recordAudit(ctx, caller, spec, req, plan.Diff, false, err.Detail.Code, nil)
				return err.Status, errorEnvelope(err)
			}
		}
		redactedDiff := redactDiffItems(plan.Diff)
		decision, approveErr := approver.Approve(ctx, ApprovalRequest{
			RunID:          caller.RunID,
			SessionID:      caller.SessionID,
			PermissionMode: caller.PermissionMode,
			Resource:       spec.Resource,
			Action:         spec.Action,
			Target:         req.Target,
			SubTarget:      req.SubTarget,
			Tier:           effectiveTier,
			Summary:        plan.Summary,
			Diff:           redactedDiff,
			DryRun:         req.Options.DryRun,
		})
		if approveErr != nil {
			code := ErrCodeApprovalDenied
			if errors.Is(approveErr, context.DeadlineExceeded) || errors.Is(approveErr, context.Canceled) {
				code = ErrCodeApprovalTimeout
			}
			err := newCallError(http.StatusForbidden, code, approveErr.Error()).withTier(effectiveTier)
			e.recordAudit(ctx, caller, spec, req, plan.Diff, false, err.Detail.Code, nil)
			return err.Status, errorEnvelope(err)
		}
		if !decision.Granted {
			code := strings.TrimSpace(decision.Code)
			if code == "" {
				code = ErrCodeApprovalDenied
			}
			reason := strings.TrimSpace(decision.Reason)
			if reason == "" {
				reason = "control-plane operation denied by approver"
			}
			err := newCallError(http.StatusForbidden, code, reason).withTier(effectiveTier)
			e.recordAudit(ctx, caller, spec, req, plan.Diff, false, err.Detail.Code, nil)
			return err.Status, errorEnvelope(err)
		}
	}

	committedData, commitErr := plan.Commit(ctx)
	if commitErr != nil {
		ce := asCallError(commitErr, effectiveTier)
		e.recordAudit(ctx, caller, spec, req, plan.Diff, false, ce.Detail.Code, nil)
		return ce.Status, errorEnvelope(ce)
	}

	if e.deps.OnConfigChanged != nil && (spec.Domain == DomainConfig || spec.Domain == DomainNetwork) {
		e.deps.OnConfigChanged(spec.Domain)
	}
	if e.deps.OnCatalogChanged != nil {
		switch spec.Domain {
		case DomainModel, DomainSiteModel, DomainSite, DomainSiteGroup, DomainSiteKey, DomainPricing, DomainAPIKey, DomainCooldown:
			e.deps.OnCatalogChanged()
		}
	}
	e.recordAudit(ctx, caller, spec, req, plan.Diff, true, "", plan.AppliedEffects)

	meta := &Meta{
		Tier:           effectiveTier,
		DryRun:         false,
		Revision:       plan.Revision,
		AppliedEffects: plan.AppliedEffects,
	}
	env := e.finalizeEnvelope(committedData, plan.Diff, meta, effectiveTier, req.Options)
	return http.StatusOK, env
}

func (e *Engine) finalizeEnvelope(data any, diff []DiffItem, meta *Meta, tier int, opts CallOptions) Envelope {
	redactedData := RedactValue(data)
	if len(opts.Fields) > 0 {
		redactedData = ProjectFields(redactedData, opts.Fields)
	}
	var redactedDiff []DiffItem
	if len(diff) > 0 {
		redactedDiff = redactDiffItems(diff)
	}
	if meta == nil {
		meta = &Meta{Tier: tier, DryRun: opts.DryRun}
	} else if meta.Tier == 0 {
		meta.Tier = tier
	}
	env := Envelope{
		OK:   true,
		Data: redactedData,
		Diff: redactedDiff,
		Meta: meta,
	}
	TruncateEnvelope(&env, DefaultMaxResponseBytes)
	return env
}

func redactDiffItems(items []DiffItem) []DiffItem {
	out := make([]DiffItem, len(items))
	for i, item := range items {
		out[i] = DiffItem{
			Path:   item.Path,
			Before: RedactValue(item.Before),
			After:  RedactValue(item.After),
		}
	}
	return out
}

func asCallError(err error, defaultTier int) *callError {
	var ce *callError
	if errors.As(err, &ce) && ce != nil {
		if ce.Tier == 0 {
			ce.Tier = defaultTier
		}
		return ce
	}
	return newCallError(http.StatusInternalServerError, ErrCodeInternal, err.Error()).withTier(defaultTier)
}

func (e *Engine) dispatch(ctx context.Context, caller Caller, spec OpSpec, req CallRequest) (callResult, *callError) {
	switch spec.Resource {
	case "status", "schema", "config":
		return e.dispatchConfigAndMeta(ctx, caller, spec, req)
	default:
		return e.dispatchResource(ctx, caller, spec, req)
	}
}

func (e *Engine) recordAudit(ctx context.Context, caller Caller, spec OpSpec, req CallRequest, diff []DiffItem, success bool, errCode string, effects []string) {
	if e.deps.Auth == nil {
		return
	}
	actorType := strings.TrimSpace(caller.ActorType)
	if actorType == "" {
		if caller.Channel == ChannelMCP {
			actorType = ChannelMCP
		} else {
			actorType = auth.AgentCLIActorType
		}
	}
	meta := map[string]any{
		"channel":         caller.Channel,
		"run_id":          caller.RunID,
		"session_id":      caller.SessionID,
		"permission_mode": caller.PermissionMode,
		"tier":            spec.Tier,
		"target":          req.Target,
		"sub_target":      req.SubTarget,
		"dry_run":         req.Options.DryRun,
	}
	if len(diff) > 0 {
		redactedDiff := redactDiffItems(diff)
		if len(redactedDiff) > 25 {
			redactedDiff = redactedDiff[:25]
		}
		meta["diff"] = redactedDiff
	}
	if len(effects) > 0 {
		meta["effects"] = effects
	}
	e.deps.Auth.RecordAudit(ctx, auth.AuditInput{
		Actor:        auth.AdminActor{Type: actorType},
		Action:       "ctl." + spec.Resource + "." + spec.Action,
		ResourceType: spec.Resource,
		ResourceID:   strings.TrimSpace(req.Target),
		Success:      success,
		ErrorCode:    errCode,
		Metadata:     meta,
	})
}

func parseUUIDParam(raw, fieldName string) (uuid.UUID, *callError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuid.Nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			fieldName+" is required").withField(fieldName)
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, newCallError(http.StatusBadRequest, ErrCodeInvalidArgument,
			fmt.Sprintf("%s must be a valid UUID", fieldName)).withField(fieldName)
	}
	return parsed, nil
}

func decodePayloadInto(payload any, dst any) *callError {
	if payload == nil {
		return newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "request payload is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "invalid payload: "+err.Error())
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return newCallError(http.StatusBadRequest, ErrCodeInvalidArgument, "invalid payload shape: "+err.Error())
	}
	return nil
}
