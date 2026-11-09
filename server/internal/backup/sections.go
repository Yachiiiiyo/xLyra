package backup

import (
	"fmt"
	"slices"
)

// 备份类别(section)常量。sectionAgentCapabilities 定义在 agent_capabilities.go。
const (
	sectionSettings       = "settings"
	sectionGatewaySites   = "gateway_sites"
	sectionGatewayKeys    = "gateway_keys"
	sectionGatewayLogs    = "gateway_logs"
	sectionPlugins        = "plugins"
	sectionAgentConfig    = "agent_config"
	sectionAgentSessions  = "agent_sessions"
	sectionAgentWorkspace = "agent_workspace"
)

// Section 描述一个备份类别:拥有的数据库表、配置顶层键,以及与其它类别的依赖。
type Section struct {
	ID            string
	Group         string   // "system" | "gateway" | "agent"
	Requires      []string // 硬依赖
	Recommends    []string // 软依赖
	Tables        []string // 数据库表
	ConfigPaths   []string // 认领的配置顶层键;settings 为空表示「其余全部」
	DefaultManual bool
	DefaultAuto   bool
}

// sections 是全部备份类别的注册表,顺序即对外展示顺序(design 文档中的表格顺序)。
var sections = []Section{
	{
		ID:            sectionSettings,
		Group:         "system",
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionGatewaySites,
		Group: "gateway",
		Tables: []string{
			"sites",
			"site_groups",
			"site_group_sites",
			"site_states",
			"oauth_connections",
			"site_credentials",
			"site_api_key_states",
			"canonical_models",
			"site_models",
			"site_model_endpoint_overrides",
			"canonical_model_aliases",
			"site_api_key_models",
			"site_pricing_groups",
			"site_model_pricings",
		},
		ConfigPaths:   []string{"network"},
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionGatewayKeys,
		Group: "gateway",
		Requires: []string{
			sectionGatewaySites,
		},
		Tables: []string{
			"api_keys",
			"api_key_site_permissions",
			"api_key_site_group_permissions",
			"api_key_site_model_permissions",
			"gateway_rate_limits",
		},
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionGatewayLogs,
		Group: "gateway",
		Requires: []string{
			sectionGatewaySites,
			sectionGatewayKeys,
		},
		Tables: []string{
			"request_logs",
			"usage_records",
			"request_usage_daily_summaries",
			"request_usage_summary_days",
		},
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionPlugins,
		Group: "gateway",
		Requires: []string{
			sectionGatewaySites,
		},
		Tables: []string{
			"js_plugins",
			"js_plugin_versions",
			"js_plugin_trusted_keys",
			"js_plugin_bindings",
			"js_plugin_binding_inputs",
			"js_plugin_market_subscriptions",
		},
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionAgentConfig,
		Group: "agent",
		Recommends: []string{
			sectionGatewaySites,
		},
		ConfigPaths:   []string{"agent"},
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:            sectionAgentCapabilities,
		Group:         "agent",
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:            sectionAgentSessions,
		Group:         "agent",
		DefaultManual: true,
		DefaultAuto:   true,
	},
	{
		ID:    sectionAgentWorkspace,
		Group: "agent",
	},
}

// Sections 按注册表顺序返回全部类别的深拷贝,调用方修改不会影响注册表。
func Sections() []Section {
	out := make([]Section, len(sections))
	for i, section := range sections {
		out[i] = section
		out[i].Requires = slices.Clone(section.Requires)
		out[i].Recommends = slices.Clone(section.Recommends)
		out[i].Tables = slices.Clone(section.Tables)
		out[i].ConfigPaths = slices.Clone(section.ConfigPaths)
	}
	return out
}

// SectionByID 查找类别并返回拷贝;未注册时第二返回值为 false。
func SectionByID(id string) (Section, bool) {
	for _, section := range sections {
		if section.ID == id {
			out := section
			out.Requires = slices.Clone(section.Requires)
			out.Recommends = slices.Clone(section.Recommends)
			out.Tables = slices.Clone(section.Tables)
			out.ConfigPaths = slices.Clone(section.ConfigPaths)
			return out, true
		}
	}
	return Section{}, false
}

// ValidateSelection 校验用户勾选的类别集合:不允许为空、重复、未知 id,
// 也不允许缺少硬依赖;不做任何自动补全。
func ValidateSelection(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("至少选择一个备份类别")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate backup section %q", id)
		}
		seen[id] = struct{}{}
		if _, ok := SectionByID(id); !ok {
			return fmt.Errorf("unknown backup section %q", id)
		}
	}
	for _, id := range ids {
		section, _ := SectionByID(id)
		for _, require := range section.Requires {
			if _, ok := seen[require]; !ok {
				return fmt.Errorf("backup section %q requires %q", id, require)
			}
		}
	}
	return nil
}

// ExpandRequires 返回 ids 及其(传递)硬依赖类别,去重并按注册表顺序排列;
// 未知 id 原样保留在末尾,由 ValidateSelection 负责报错。
func ExpandRequires(ids []string) []string {
	known := make(map[string]struct{})
	var unknown []string
	for _, id := range ids {
		if _, ok := SectionByID(id); ok {
			known[id] = struct{}{}
		} else if !slices.Contains(unknown, id) {
			unknown = append(unknown, id)
		}
	}
	// 闭包:把硬依赖不断并入,直到不再增长。
	for {
		grew := false
		for id := range known {
			section, ok := SectionByID(id)
			if !ok {
				continue
			}
			for _, require := range section.Requires {
				if _, ok := known[require]; !ok {
					known[require] = struct{}{}
					grew = true
				}
			}
		}
		if !grew {
			break
		}
	}
	out := make([]string, 0, len(known)+len(unknown))
	for _, section := range sections {
		if _, ok := known[section.ID]; ok {
			out = append(out, section.ID)
		}
	}
	return append(out, unknown...)
}

// DefaultSections 返回手动导出或自动备份的默认勾选类别(注册表顺序)。
func DefaultSections(auto bool) []string {
	var out []string
	for _, section := range sections {
		if auto {
			if section.DefaultAuto {
				out = append(out, section.ID)
			}
		} else if section.DefaultManual {
			out = append(out, section.ID)
		}
	}
	return out
}

// TablesOf 返回所选类别拥有的数据库表并集,按全局导出顺序排列。
func TablesOf(ids []string) []string {
	wanted := make(map[string]struct{})
	for _, id := range ids {
		section, ok := SectionByID(id)
		if !ok {
			continue
		}
		for _, table := range section.Tables {
			wanted[table] = struct{}{}
		}
	}
	return tablesInExportOrder(wanted)
}

// tableFKChildren 是备份表之间的外键「父表 → 直接子表」映射,与数据库
// schema(见 migrations)中的外键一致;非备份表的引用不在其中。
var tableFKChildren = map[string][]string{
	"sites": {
		"site_group_sites",
		"site_states",
		"oauth_connections",
		"site_credentials",
		"site_api_key_states",
		"site_models",
		"site_pricing_groups",
		"site_model_pricings",
		"site_api_key_models",
		"api_key_site_permissions",
		"request_logs",
		"usage_records",
	},
	"site_groups": {
		"site_group_sites",
		"api_key_site_group_permissions",
	},
	"site_credentials": {
		"site_api_key_states",
		"site_api_key_models",
	},
	"site_models": {
		"api_key_site_model_permissions",
		"site_model_endpoint_overrides",
		"site_api_key_models",
		"site_model_pricings",
		"request_logs",
	},
	"canonical_models": {
		"canonical_model_aliases",
		"site_models",
		"request_logs",
		"usage_records",
	},
	"api_keys": {
		"api_key_site_permissions",
		"api_key_site_group_permissions",
		"api_key_site_model_permissions",
		"gateway_rate_limits",
		"request_logs",
		"usage_records",
	},
	"request_logs": {
		"usage_records",
	},
	"js_plugins": {
		"js_plugin_versions",
		"js_plugin_bindings",
		"js_plugin_market_subscriptions",
	},
	"js_plugin_versions": {
		"js_plugin_bindings",
	},
	"js_plugin_bindings": {
		"js_plugin_binding_inputs",
	},
}

// tableDependentChildren 是表级「随行清空」映射:汇总表由 request_logs 聚合
// 而来,明细数据被清空后汇总数据随之作废(恢复时与明细表一起重建)。它们
// 没有数据库外键,但 ClearedTables 会把它们并入需清空的表。
var tableDependentChildren = map[string][]string{
	"request_logs": {
		"request_usage_daily_summaries",
		"request_usage_summary_days",
	},
}

// fkDescendants 返回 tables 的全部(传递)外键子表,不含 tables 自身,
// 按全局导出顺序排列并去重。
func fkDescendants(tables []string) []string {
	input := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		input[table] = struct{}{}
	}
	reached := make(map[string]struct{})
	queue := slices.Clone(tables)
	for len(queue) > 0 {
		table := queue[0]
		queue = queue[1:]
		for _, child := range tableFKChildren[table] {
			if _, ok := reached[child]; ok {
				continue
			}
			reached[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	for table := range input {
		delete(reached, table)
	}
	return tablesInExportOrder(reached)
}

// SectionImpact 描述恢复操作对某个未恢复类别的清空影响。
type SectionImpact struct {
	ID string `json:"id"`
	// Partial 表示该类别只有部分表的数据被清空;false 表示全部表被清空。
	Partial bool `json:"partial"`
}

// RestoreImpact 计算恢复 restored 这些类别时,不在 restored 里、却会因
// TRUNCATE … CASCADE(以及汇总表随明细作废)而丢失数据的类别,按注册表顺序
// 返回。某类别只有部分表被清空时 Partial 为 true。
func RestoreImpact(restored []string) []SectionImpact {
	restoredSet := make(map[string]struct{}, len(restored))
	for _, id := range restored {
		restoredSet[id] = struct{}{}
	}
	// 数据丢失面 = TRUNCATE 清单(已含随行清空的汇总表)。
	lost := make(map[string]struct{})
	for _, table := range ClearedTables(restored) {
		lost[table] = struct{}{}
	}
	var impacts []SectionImpact
	for _, section := range sections {
		if _, ok := restoredSet[section.ID]; ok {
			continue
		}
		cleared := 0
		for _, table := range section.Tables {
			if _, ok := lost[table]; ok {
				cleared++
			}
		}
		if cleared == 0 {
			continue
		}
		impacts = append(impacts, SectionImpact{
			ID:      section.ID,
			Partial: cleared < len(section.Tables),
		})
	}
	return impacts
}

// ClearedTables 返回恢复 restored 这些类别时需要 TRUNCATE 的表:
// 所选类别的表及其全部外键子表,按全局导出顺序排列。
func ClearedTables(restored []string) []string {
	owned := TablesOf(restored)
	wanted := make(map[string]struct{}, len(owned))
	for _, table := range owned {
		wanted[table] = struct{}{}
	}
	for _, table := range fkDescendants(owned) {
		wanted[table] = struct{}{}
	}
	// 汇总表没有外键,但明细被清空后必须随之清空,否则与明细不一致。
	for _, table := range tablesInExportOrder(wanted) {
		for _, child := range tableDependentChildren[table] {
			wanted[child] = struct{}{}
		}
	}
	return tablesInExportOrder(wanted)
}

// tablesInExportOrder 把表集合按 exportTables 的全局导出顺序输出并去重。
func tablesInExportOrder(tables map[string]struct{}) []string {
	out := make([]string, 0, len(tables))
	for _, table := range exportTables {
		if _, ok := tables[table]; ok {
			out = append(out, table)
		}
	}
	return out
}
