package agentctl

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Safety tiers enforced unconditionally by the control-plane engine.
const (
	Tier0Forbidden = 0
	Tier1ReadOnly  = 1
	Tier2Managed   = 2
	Tier3HighRisk  = 3
)

// Channels identify the caller transport sharing this engine.
const (
	ChannelAgentCLI = "agent_cli"
	ChannelMCP      = "mcp"
	ChannelInternal = "internal"
)

// High-level domains used for schema grouping and configurable CapabilityPolicy rules.
const (
	DomainSystem       = "system"
	DomainConfig       = "config"
	DomainNetwork      = "network"
	DomainSite         = "site"
	DomainSiteGroup    = "site_group"
	DomainSiteKey      = "site_key"
	DomainModel        = "model"
	DomainSiteModel    = "site_model"
	DomainPricing      = "pricing"
	DomainAPIKey       = "api_key"
	DomainRoute        = "route"
	DomainCooldown     = "cooldown"
	DomainCapabilities = "capabilities"
	DomainBackup       = "backup"
	DomainCloud        = "cloud"
	DomainDashboard    = "dashboard"
	DomainRequest      = "request"
	DomainUsage        = "usage"
	DomainPlugin       = "plugin"
	DomainPluginMarket = "plugin_market"
	DomainTrustedKey   = "trusted_key"
	DomainAudit        = "audit"
)

// Operation verbs describe mutation semantics for policy evaluation and audit logs.
const (
	VerbMeta    = "meta"
	VerbRead    = "read"
	VerbWrite   = "write"
	VerbDelete  = "delete"
	VerbTrigger = "trigger"
)

// OpSpec describes a control-plane operation cleanly enough for both CLI schema inspection
// and future MCP tool generation.
type OpSpec struct {
	Resource    string         `json:"resource"`
	Action      string         `json:"action"`
	Domain      string         `json:"domain"`
	Verb        string         `json:"verb"`
	Tier        int            `json:"tier"`
	Implemented bool           `json:"implemented"`
	MCPName     string         `json:"mcp_name,omitempty"`
	Summary     string         `json:"summary"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// OpKey returns the canonical "resource/action" key.
func (s OpSpec) OpKey() string {
	return s.Resource + "/" + s.Action
}

// Caller identifies who invoked the control plane (built-in Agent CLI, future MCP server, etc.).
type Caller struct {
	Channel        string
	RunID          string
	SessionID      string
	ActorType      string
	PermissionMode string
	Policy         *CapabilityPolicy
	Quota          *RunQuota
	Approver       Approver
}

// CapabilityPolicy defines declarative feature-level access control on top of server Tier enforcement.
// Tier 0 is always forbidden regardless of CapabilityPolicy settings.
type CapabilityPolicy struct {
	// Mode is one of "allow_all_non_tier0" (default for built-in agent), "read_only", or "custom".
	Mode                string   `json:"mode"`
	AllowDomains        []string `json:"allow_domains,omitempty"`
	DenyDomains         []string `json:"deny_domains,omitempty"`
	AllowOps            []string `json:"allow_ops,omitempty"`
	DenyOps             []string `json:"deny_ops,omitempty"`
	AllowConfigPrefixes []string `json:"allow_config_prefixes,omitempty"`
	DenyConfigPrefixes  []string `json:"deny_config_prefixes,omitempty"`
	// MaxTier caps the maximum permitted risk tier when > 0 (defaults to Tier3HighRisk if <= 0).
	MaxTier int `json:"max_tier,omitempty"`
}

// DefaultBuiltinAgentPolicy returns the permissive policy for the built-in Agent:
// all non-Tier0 capabilities are open and governed by native permissionMode + Tier approvals.
func DefaultBuiltinAgentPolicy() *CapabilityPolicy {
	return &CapabilityPolicy{
		Mode:    "allow_all_non_tier0",
		MaxTier: Tier3HighRisk,
	}
}

// ReadOnlyPolicy restricts callers to read/metadata Tier 1 operations only.
func ReadOnlyPolicy() *CapabilityPolicy {
	return &CapabilityPolicy{
		Mode:    "read_only",
		MaxTier: Tier1ReadOnly,
	}
}

// AllowsOp evaluates whether the policy permits executing the given operation spec.
func (p *CapabilityPolicy) AllowsOp(spec OpSpec) (bool, string) {
	if spec.Tier == Tier0Forbidden {
		return false, "operation is Tier 0 forbidden"
	}
	if p == nil {
		return true, ""
	}
	maxTier := p.MaxTier
	if maxTier <= 0 {
		maxTier = Tier3HighRisk
	}
	if spec.Tier > maxTier {
		return false, fmt.Sprintf("operation tier %d exceeds capability policy max_tier %d", spec.Tier, maxTier)
	}
	opKey := spec.OpKey()
	if matchesPattern(p.DenyOps, spec.Resource, opKey) {
		return false, fmt.Sprintf("operation %q is explicitly denied by policy", opKey)
	}
	if slices.Contains(p.DenyDomains, spec.Domain) {
		return false, fmt.Sprintf("domain %q is explicitly denied by policy", spec.Domain)
	}

	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case "", "allow_all_non_tier0":
		return true, ""
	case "read_only":
		if spec.Verb == VerbMeta || spec.Verb == VerbRead {
			return true, ""
		}
		return false, fmt.Sprintf("operation %q (%s) is blocked in read_only policy", opKey, spec.Verb)
	case "custom":
		if matchesPattern(p.AllowOps, spec.Resource, opKey) {
			return true, ""
		}
		if slices.Contains(p.AllowDomains, spec.Domain) {
			return true, ""
		}
		return false, fmt.Sprintf("operation %q (domain %q) is not included in capability allowlist", opKey, spec.Domain)
	default:
		return false, fmt.Sprintf("unknown capability policy mode %q", p.Mode)
	}
}

// AllowsConfigPath checks optional config prefix allow/deny filters in CapabilityPolicy.
func (p *CapabilityPolicy) AllowsConfigPath(path string) (bool, string) {
	if p == nil {
		return true, ""
	}
	norm := strings.TrimSpace(strings.ToLower(path))
	for _, prefix := range p.DenyConfigPrefixes {
		if matchPrefix(norm, strings.ToLower(strings.TrimSpace(prefix))) {
			return false, fmt.Sprintf("config prefix %q is denied by capability policy", prefix)
		}
	}
	if len(p.AllowConfigPrefixes) > 0 {
		for _, prefix := range p.AllowConfigPrefixes {
			if matchPrefix(norm, strings.ToLower(strings.TrimSpace(prefix))) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("config path %q is not in allow_config_prefixes", path)
	}
	return true, ""
}

func matchPrefix(value, prefix string) bool {
	if prefix == "" {
		return false
	}
	prefix = strings.TrimSuffix(prefix, ".*")
	prefix = strings.TrimSuffix(prefix, ".")
	return value == prefix || strings.HasPrefix(value, prefix+".")
}

func matchesPattern(patterns []string, resource, opKey string) bool {
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if pat == "*" || pat == opKey || pat == resource+"/*" {
			return true
		}
	}
	return false
}

// RunQuota guards a single run against runaway mutation loops.
type RunQuota struct {
	mu        sync.Mutex
	maxWrites int
	writes    int
}

// NewRunQuota constructs a per-run quota limiter. Defaults to 60 write mutations if <= 0.
func NewRunQuota(maxWrites int) *RunQuota {
	if maxWrites <= 0 {
		maxWrites = 60
	}
	return &RunQuota{maxWrites: maxWrites}
}

// ConsumeWrite increments write usage or returns a quota-exceeded callError.
func (q *RunQuota) ConsumeWrite() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.writes >= q.maxWrites {
		return newCallError(http.StatusTooManyRequests, ErrCodeQuotaExceeded,
			fmt.Sprintf("control-plane mutation quota exceeded for this run (%d writes)", q.maxWrites))
	}
	q.writes++
	return nil
}

// ApprovalRequest describes a Tier 2/3 mutation awaiting classifier or human approval.
type ApprovalRequest struct {
	RunID          string
	SessionID      string
	PermissionMode string
	Resource       string
	Action         string
	Target         string
	SubTarget      string
	Tier           int
	Summary        string
	Diff           []DiffItem
	DryRun         bool
}

// ApprovalDecision reports whether a mutation was permitted.
type ApprovalDecision struct {
	Granted bool
	Code    string
	Reason  string
}

// Approver abstracts approval evaluation across built-in Agent UI, headless tests, and future MCP elicitation.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)
}

// ApproverFunc adapts a plain function into an Approver.
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error)

// Approve implements Approver.
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
	return f(ctx, req)
}

var builtinOpSpecs = []OpSpec{
	// System / Meta
	{Resource: "status", Action: "get", Domain: DomainSystem, Verb: VerbMeta, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_status_get", Summary: "查看控制面状态、主程序版本、权限模式与配置修订号"},
	{Resource: "schema", Action: "list", Domain: DomainSystem, Verb: VerbMeta, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_schema_list", Summary: "列出受支持的控制面资源、动作与安全等级"},
	{Resource: "schema", Action: "get", Domain: DomainSystem, Verb: VerbMeta, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_schema_get", Summary: "查看指定资源的动作参数结构与安全约束"},

	// Config
	{Resource: "config", Action: "list", Domain: DomainConfig, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_config_list", Summary: "列出所有配置命名空间（含只读与可写域）"},
	{Resource: "config", Action: "describe", Domain: DomainConfig, Verb: VerbMeta, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_config_describe", Summary: "查看指定配置命名空间或配置键的类型、范围、默认值与风险等级"},
	{Resource: "config", Action: "get", Domain: DomainConfig, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_config_get", Summary: "读取指定配置命名空间或具体路径的当前脱敏值"},
	{Resource: "config", Action: "set", Domain: DomainConfig, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_config_set", Summary: "设置单个或多个配置键值（支持 dry-run 与 if-match 并发校验）"},
	{Resource: "config", Action: "patch", Domain: DomainConfig, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_config_patch", Summary: "合并更新指定命名空间下的部分配置字段"},
	{Resource: "config", Action: "reset", Domain: DomainConfig, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: true, MCPName: "xlyra_config_reset", Summary: "将可写配置域重置为默认值"},

	// Proxy / Network
	{Resource: "proxy", Action: "list", Domain: DomainNetwork, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_proxy_list", Summary: "列出全局网络代理配置（URL 密码已脱敏）"},
	{Resource: "proxy", Action: "get", Domain: DomainNetwork, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_proxy_get", Summary: "查看指定代理配置详情"},
	{Resource: "proxy", Action: "create", Domain: DomainNetwork, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_proxy_create", Summary: "新增网络代理配置"},
	{Resource: "proxy", Action: "update", Domain: DomainNetwork, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_proxy_update", Summary: "更新网络代理配置"},
	{Resource: "proxy", Action: "delete", Domain: DomainNetwork, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: true, MCPName: "xlyra_proxy_delete", Summary: "删除网络代理配置"},
	{Resource: "proxy", Action: "test", Domain: DomainNetwork, Verb: VerbTrigger, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_proxy_test", Summary: "测试指定代理连通性"},
	{Resource: "proxy", Action: "set-default", Domain: DomainNetwork, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_proxy_set_default", Summary: "设置全局默认代理"},

	// Sites
	{Resource: "site", Action: "list", Domain: DomainSite, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_list", Summary: "列出站点列表与实时健康状态（凭证已脱敏）"},
	{Resource: "site", Action: "get", Domain: DomainSite, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_get", Summary: "查看单个站点配置与连接池状态"},
	{Resource: "site", Action: "create", Domain: DomainSite, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_site_create", Summary: "创建上游站点（需通过标准站点配置流程）"},
	{Resource: "site", Action: "update", Domain: DomainSite, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: true, MCPName: "xlyra_site_update", Summary: "更新站点基础名称、启停或分组等非密文元数据"},
	{Resource: "site", Action: "delete", Domain: DomainSite, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_site_delete", Summary: "删除站点"},
	{Resource: "site", Action: "enable", Domain: DomainSite, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_enable", Summary: "启用指定站点"},
	{Resource: "site", Action: "disable", Domain: DomainSite, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_disable", Summary: "停用指定站点"},
	{Resource: "site", Action: "health", Domain: DomainSite, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_health", Summary: "查看站点模型健康状态与冷却信息"},
	{Resource: "site", Action: "test", Domain: DomainSite, Verb: VerbTrigger, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_test", Summary: "测试站点连通性"},

	// Site groups
	{Resource: "site-group", Action: "list", Domain: DomainSiteGroup, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_group_list", Summary: "列出站点分组"},
	{Resource: "site-group", Action: "get", Domain: DomainSiteGroup, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_group_get", Summary: "查看站点分组详情"},
	{Resource: "site-group", Action: "create", Domain: DomainSiteGroup, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_group_create", Summary: "创建站点分组"},
	{Resource: "site-group", Action: "update", Domain: DomainSiteGroup, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_group_update", Summary: "更新站点分组"},
	{Resource: "site-group", Action: "delete", Domain: DomainSiteGroup, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_site_group_delete", Summary: "删除站点分组"},

	// Site keys
	{Resource: "site-key", Action: "list", Domain: DomainSiteKey, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_key_list", Summary: "列出站点上游凭证摘要（绝不返回密文）"},
	{Resource: "site-key", Action: "get", Domain: DomainSiteKey, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_key_get", Summary: "查看站点凭证元数据"},
	{Resource: "site-key", Action: "create", Domain: DomainSiteKey, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 通过 CLI 写入上游密钥"},
	{Resource: "site-key", Action: "update", Domain: DomainSiteKey, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, Summary: "更新站点凭证状态"},
	{Resource: "site-key", Action: "delete", Domain: DomainSiteKey, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, Summary: "删除站点凭证"},
	{Resource: "site-key", Action: "enable", Domain: DomainSiteKey, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_key_enable", Summary: "启用站点凭证"},
	{Resource: "site-key", Action: "disable", Domain: DomainSiteKey, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_key_disable", Summary: "停用站点凭证"},

	// Models
	{Resource: "model", Action: "list", Domain: DomainModel, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_model_list", Summary: "列出全局标准模型目录（支持 --provider/--category/--search/分页）"},
	{Resource: "model", Action: "get", Domain: DomainModel, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_model_get", Summary: "查看标准模型详情、别名、统计口径与可选站点矩阵（--matrix）"},
	{Resource: "model", Action: "matrix", Domain: DomainModel, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_model_matrix", Summary: "查看标准模型的站点能力矩阵"},
	{Resource: "model", Action: "create", Domain: DomainModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_create", Summary: "创建标准模型"},
	{Resource: "model", Action: "update", Domain: DomainModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_update", Summary: "更新标准模型"},
	{Resource: "model", Action: "delete", Domain: DomainModel, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: true, MCPName: "xlyra_model_delete", Summary: "归档无关联站点的标准模型"},
	{Resource: "model", Action: "merge", Domain: DomainModel, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: true, MCPName: "xlyra_model_merge", Summary: "合并源标准模型到目标标准模型（迁移渠道与别名并归档源模型）"},
	{Resource: "model", Action: "alias-add", Domain: DomainModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_alias_add", Summary: "为标准模型添加手动别名"},
	{Resource: "model", Action: "alias-delete", Domain: DomainModel, Verb: VerbDelete, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_alias_delete", Summary: "删除标准模型别名"},
	{Resource: "model", Action: "pricing-set", Domain: DomainModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_pricing_set", Summary: "设置标准模型手动参考定价"},
	{Resource: "model", Action: "pricing-reset", Domain: DomainModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_model_pricing_reset", Summary: "重置标准模型参考定价"},
	{Resource: "model", Action: "discover", Domain: DomainModel, Verb: VerbTrigger, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_model_discover", Summary: "触发站点模型发现"},

	// Site models
	{Resource: "site-model", Action: "list", Domain: DomainSiteModel, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_site_model_list", Summary: "列出站点模型（支持 --site/--canonical/--enabled/--page/--page-size 过滤与分页）"},
	{Resource: "site-model", Action: "enable", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_model_enable", Summary: "启用站点模型"},
	{Resource: "site-model", Action: "disable", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_model_disable", Summary: "停用站点模型"},
	{Resource: "site-model", Action: "update", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_model_update", Summary: "更新站点模型启停状态"},
	{Resource: "site-model", Action: "batch-status", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_model_batch_status", Summary: "批量启停站点模型"},
	{Resource: "site-model", Action: "bind-canonical", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_site_model_bind_canonical", Summary: "将站点模型绑定或解绑到指定标准模型"},
	{Resource: "site-model", Action: "bulk-enable", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_model_bulk_enable", Summary: "批量启用站点模型"},
	{Resource: "site-model", Action: "create", Domain: DomainSiteModel, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_site_model_create", Summary: "新增站点模型"},
	{Resource: "site-model", Action: "delete", Domain: DomainSiteModel, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_site_model_delete", Summary: "删除站点模型"},

	// Pricing
	{Resource: "pricing", Action: "list", Domain: DomainPricing, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_pricing_list", Summary: "列出模型价格目录"},
	{Resource: "pricing", Action: "get", Domain: DomainPricing, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_pricing_get", Summary: "查看单个模型价格详情"},
	{Resource: "pricing", Action: "sync", Domain: DomainPricing, Verb: VerbTrigger, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_pricing_sync", Summary: "触发价格目录同步"},
	{Resource: "pricing", Action: "update", Domain: DomainPricing, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_pricing_update", Summary: "更新模型价格"},
	{Resource: "pricing", Action: "bulk-update", Domain: DomainPricing, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_pricing_bulk_update", Summary: "批量更新模型价格"},

	// API Keys
	{Resource: "api-key", Action: "list", Domain: DomainAPIKey, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_api_key_list", Summary: "列出外部访问 API Key 元数据（已隐藏内置 Key 且绝不返回明文）"},
	{Resource: "api-key", Action: "get", Domain: DomainAPIKey, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_api_key_get", Summary: "查看指定 API Key 元数据与模型限制"},
	{Resource: "api-key", Action: "reveal", Domain: DomainAPIKey, Verb: VerbRead, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 读取外部 API Key 明文"},
	{Resource: "api-key", Action: "create", Domain: DomainAPIKey, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_api_key_create", Summary: "创建外部 API Key"},
	{Resource: "api-key", Action: "update", Domain: DomainAPIKey, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_api_key_update", Summary: "更新外部 API Key 配置"},
	{Resource: "api-key", Action: "enable", Domain: DomainAPIKey, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_api_key_enable", Summary: "启用外部 API Key"},
	{Resource: "api-key", Action: "disable", Domain: DomainAPIKey, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_api_key_disable", Summary: "停用外部 API Key"},
	{Resource: "api-key", Action: "reset-usage", Domain: DomainAPIKey, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_api_key_reset_usage", Summary: "重置 API Key 用量统计"},
	{Resource: "api-key", Action: "delete", Domain: DomainAPIKey, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_api_key_delete", Summary: "删除外部 API Key"},

	// Routing & Cooldowns
	{Resource: "route", Action: "overview", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_overview", Summary: "列出路由概览与候选站点统计"},
	{Resource: "route", Action: "list", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_list", Summary: "列出路由概览与候选站点评分（overview 别名）"},
	{Resource: "route", Action: "candidates", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_candidates", Summary: "查看指定模型的全部路由候选明细与评分"},
	{Resource: "route", Action: "select", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_select", Summary: "模拟路由主选与容灾计划"},
	{Resource: "route", Action: "failover", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_failover", Summary: "模拟排除故障站点后的路由容灾计划"},
	{Resource: "route", Action: "traces", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_traces", Summary: "查看近期路由决策轨迹"},
	{Resource: "route", Action: "get", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_get", Summary: "查看指定模型的路由候选明细（candidates 别名）"},
	{Resource: "route", Action: "test", Domain: DomainRoute, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_route_test", Summary: "模拟路由候选排序诊断（candidates 别名）"},
	{Resource: "route", Action: "create", Domain: DomainRoute, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_route_create", Summary: "创建路由规则"},
	{Resource: "route", Action: "update", Domain: DomainRoute, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_route_update", Summary: "更新路由规则"},
	{Resource: "route", Action: "delete", Domain: DomainRoute, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_route_delete", Summary: "删除路由规则"},

	{Resource: "cooldown", Action: "list", Domain: DomainCooldown, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_cooldown_list", Summary: "列出当前生效的模型或凭证冷却记录"},
	{Resource: "cooldown", Action: "get", Domain: DomainCooldown, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_cooldown_get", Summary: "查看冷却详情"},
	{Resource: "cooldown", Action: "create", Domain: DomainCooldown, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_cooldown_create", Summary: "手动创建站点或模型冷却"},
	{Resource: "cooldown", Action: "activate", Domain: DomainCooldown, Verb: VerbWrite, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_cooldown_activate", Summary: "手动激活站点或模型冷却（create 别名）"},
	{Resource: "cooldown", Action: "clear", Domain: DomainCooldown, Verb: VerbDelete, Tier: Tier2Managed, Implemented: true, MCPName: "xlyra_cooldown_clear", Summary: "清除指定冷却记录"},
	{Resource: "cooldown", Action: "clear-all", Domain: DomainCooldown, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_cooldown_clear_all", Summary: "清空所有冷却记录"},

	// Agent Self-Configs (strictly Tier 0 on writes)
	{Resource: "agents-md", Action: "get", Domain: DomainCapabilities, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_agents_md_get", Summary: "只读查看全局 AGENTS.md"},
	{Resource: "agents-md", Action: "set", Domain: DomainCapabilities, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 修改自身 AGENTS.md"},
	{Resource: "agents-md", Action: "delete", Domain: DomainCapabilities, Verb: VerbDelete, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 删除自身 AGENTS.md"},

	{Resource: "skill", Action: "list", Domain: DomainCapabilities, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_skill_list", Summary: "只读列出 Agent 可用 Skills"},
	{Resource: "skill", Action: "get", Domain: DomainCapabilities, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_skill_get", Summary: "只读查看单个 Skill 内容"},
	{Resource: "skill", Action: "put", Domain: DomainCapabilities, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 修改自身 Skills"},
	{Resource: "skill", Action: "delete", Domain: DomainCapabilities, Verb: VerbDelete, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 删除自身 Skills"},

	// Backup & Cloud
	{Resource: "backup", Action: "sections", Domain: DomainBackup, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_backup_sections", Summary: "列出可导出的系统备份分片与元数据"},
	{Resource: "backup", Action: "export", Domain: DomainBackup, Verb: VerbRead, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_backup_export", Summary: "导出系统备份分片"},
	{Resource: "backup", Action: "restore", Domain: DomainBackup, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 执行系统全量恢复或导入"},
	{Resource: "backup", Action: "import", Domain: DomainBackup, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 导入系统全量恢复包"},

	{Resource: "cloud", Action: "status", Domain: DomainCloud, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_cloud_status", Summary: "查看云端账户绑定状态"},
	{Resource: "cloud", Action: "bind", Domain: DomainCloud, Verb: VerbWrite, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 修改云端绑定凭证"},
	{Resource: "cloud", Action: "unbind", Domain: DomainCloud, Verb: VerbDelete, Tier: Tier0Forbidden, Implemented: true, Summary: "禁止 Agent 解除云端绑定"},

	// Observability
	{Resource: "dashboard", Action: "overview", Domain: DomainDashboard, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_dashboard_overview", Summary: "查看仪表盘用量、站点健康与概览数据"},
	{Resource: "dashboard", Action: "insights", Domain: DomainDashboard, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_dashboard_insights", Summary: "查看仪表盘异常洞察分析"},

	{Resource: "request", Action: "list", Domain: DomainRequest, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_request_list", Summary: "分页查询代理请求日志"},
	{Resource: "request", Action: "get", Domain: DomainRequest, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_request_get", Summary: "查看单条请求详情（敏感请求头已脱敏）"},
	{Resource: "request", Action: "summary", Domain: DomainRequest, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_request_summary", Summary: "统计请求量、错误率与延迟汇总"},

	{Resource: "usage", Action: "summary", Domain: DomainUsage, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_usage_summary", Summary: "查看 Token 与费用汇总"},
	{Resource: "usage", Action: "query", Domain: DomainUsage, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_usage_query", Summary: "按模型或时间窗查询用量统计"},
	{Resource: "usage", Action: "timeseries", Domain: DomainUsage, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_usage_timeseries", Summary: "查看用量时间序列"},

	// Plugins & Audit
	{Resource: "plugin", Action: "list", Domain: DomainPlugin, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_plugin_list", Summary: "列出已安装 JS 插件"},
	{Resource: "plugin", Action: "get", Domain: DomainPlugin, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_plugin_get", Summary: "查看 JS 插件详情"},
	{Resource: "plugin", Action: "enable", Domain: DomainPlugin, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_plugin_enable", Summary: "启用 JS 插件"},
	{Resource: "plugin", Action: "disable", Domain: DomainPlugin, Verb: VerbWrite, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_plugin_disable", Summary: "停用 JS 插件"},
	{Resource: "plugin", Action: "install", Domain: DomainPlugin, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_plugin_install", Summary: "安装 JS 插件"},
	{Resource: "plugin", Action: "uninstall", Domain: DomainPlugin, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_plugin_uninstall", Summary: "卸载 JS 插件"},
	{Resource: "plugin", Action: "update", Domain: DomainPlugin, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_plugin_update", Summary: "更新 JS 插件"},
	{Resource: "plugin", Action: "reload", Domain: DomainPlugin, Verb: VerbTrigger, Tier: Tier2Managed, Implemented: false, MCPName: "xlyra_plugin_reload", Summary: "重载 JS 插件"},

	{Resource: "plugin-market", Action: "list", Domain: DomainPluginMarket, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_plugin_market_list", Summary: "查看插件市场源与目录"},
	{Resource: "plugin-market", Action: "search", Domain: DomainPluginMarket, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_plugin_market_search", Summary: "搜索插件市场条目"},
	{Resource: "plugin-market", Action: "install", Domain: DomainPluginMarket, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_plugin_market_install", Summary: "从市场安装插件"},

	{Resource: "trusted-key", Action: "list", Domain: DomainTrustedKey, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_trusted_key_list", Summary: "列出受信任的插件签名公钥"},
	{Resource: "trusted-key", Action: "add", Domain: DomainTrustedKey, Verb: VerbWrite, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_trusted_key_add", Summary: "添加插件签名公钥"},
	{Resource: "trusted-key", Action: "delete", Domain: DomainTrustedKey, Verb: VerbDelete, Tier: Tier3HighRisk, Implemented: false, MCPName: "xlyra_trusted_key_delete", Summary: "删除插件签名公钥"},

	{Resource: "audit", Action: "list", Domain: DomainAudit, Verb: VerbRead, Tier: Tier1ReadOnly, Implemented: true, MCPName: "xlyra_audit_list", Summary: "查询管理员与 Agent 控制面审计日志"},
}

// BuiltinOpSpecs returns a copy of all registered control-plane operation specs.
func BuiltinOpSpecs() []OpSpec {
	out := make([]OpSpec, len(builtinOpSpecs))
	copy(out, builtinOpSpecs)
	return out
}

// LookupOpSpec finds an OpSpec by resource and action.
func LookupOpSpec(resource, action string) (OpSpec, bool) {
	r := strings.ToLower(strings.TrimSpace(resource))
	a := strings.ToLower(strings.TrimSpace(action))
	for _, spec := range builtinOpSpecs {
		if spec.Resource == r && spec.Action == a {
			return spec, true
		}
	}
	return OpSpec{}, false
}
