package backup

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSectionsRegistryIDs(t *testing.T) {
	got := Sections()
	if len(got) == 0 {
		t.Fatal("sections registry is empty")
	}
	seen := make(map[string]struct{}, len(got))
	for _, section := range got {
		if _, ok := seen[section.ID]; ok {
			t.Fatalf("duplicate section id %q", section.ID)
		}
		seen[section.ID] = struct{}{}
	}
	for _, id := range []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionPlugins,
		sectionAgentConfig,
		sectionAgentCapabilities,
		sectionAgentSessions,
		sectionAgentWorkspace,
	} {
		if _, ok := seen[id]; !ok {
			t.Fatalf("section %q missing from registry", id)
		}
	}
}

func TestSectionsRegistryReferences(t *testing.T) {
	for _, section := range Sections() {
		for _, kind := range []struct {
			label string
			ids   []string
		}{
			{"Requires", section.Requires},
			{"Recommends", section.Recommends},
		} {
			for _, id := range kind.ids {
				if _, ok := SectionByID(id); !ok {
					t.Fatalf("section %q %s references unknown id %q", section.ID, kind.label, id)
				}
			}
		}
	}
}

func TestSectionsRegistryTableOwnership(t *testing.T) {
	owner := make(map[string]string)
	for _, section := range Sections() {
		for _, table := range section.Tables {
			if prev, ok := owner[table]; ok {
				t.Fatalf("table %q owned by both %q and %q", table, prev, section.ID)
			}
			owner[table] = section.ID
		}
	}
	var orphan []string
	for _, table := range exportTables {
		if strings.HasPrefix(table, "playground_") {
			continue
		}
		if _, ok := owner[table]; !ok {
			orphan = append(orphan, table)
		}
	}
	if len(orphan) > 0 {
		t.Fatalf("export tables not owned by any section: %v", orphan)
	}
}

func TestSectionsRegistryConfigPaths(t *testing.T) {
	seen := make(map[string]string)
	for _, section := range Sections() {
		for _, key := range section.ConfigPaths {
			if prev, ok := seen[key]; ok {
				t.Fatalf("config key %q claimed by both %q and %q", key, prev, section.ID)
			}
			seen[key] = section.ID
		}
	}
}

func TestSectionsReturnsCopies(t *testing.T) {
	before := Sections()
	mutated := Sections()
	// gateway_logs 的 Requires / gateway_sites 的 Tables 都非空,改首元素应只影响拷贝。
	mutated[3].Requires[0] = "mutated"
	mutated[1].Tables[0] = "mutated"
	mutated[1].ConfigPaths[0] = "mutated"
	mutated = append(mutated, Section{ID: "extra"})
	after := Sections()
	if len(after) != len(before) {
		t.Fatalf("registry length changed: %d -> %d", len(before), len(after))
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("registry changed after mutating a returned copy: %+v", after)
	}
	if before[3].Requires[0] != sectionGatewaySites {
		t.Fatalf("gateway_logs requirement mutated: %q", before[3].Requires[0])
	}
	byID, ok := SectionByID(sectionGatewayKeys)
	if !ok {
		t.Fatal("SectionByID(gateway_keys) not found")
	}
	byID.Requires[0] = "mutated"
	again, _ := SectionByID(sectionGatewayKeys)
	if again.Requires[0] != sectionGatewaySites {
		t.Fatalf("SectionByID returned a shared backing array: %q", again.Requires[0])
	}
}

func TestValidateSelection(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want string
	}{
		{
			name: "empty",
			want: "至少选择一个备份类别",
		},
		{
			name: "duplicate",
			ids:  []string{sectionSettings, sectionSettings},
			want: `duplicate backup section "settings"`,
		},
		{
			name: "unknown",
			ids:  []string{sectionSettings, "nope"},
			want: `unknown backup section "nope"`,
		},
		{
			name: "missing requires",
			ids:  []string{sectionGatewayKeys},
			want: `backup section "gateway_keys" requires "gateway_sites"`,
		},
		{
			name: "missing second require",
			ids:  []string{sectionGatewayLogs, sectionGatewaySites},
			want: `backup section "gateway_logs" requires "gateway_keys"`,
		},
		{
			name: "valid",
			ids:  []string{sectionGatewayLogs, sectionGatewayKeys, sectionGatewaySites, sectionSettings},
		},
		{
			name: "valid single",
			ids:  []string{sectionAgentWorkspace},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSelection(tc.ids)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateSelection(%v) = %v, want nil", tc.ids, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateSelection(%v) = nil, want %q", tc.ids, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("ValidateSelection(%v) = %q, want %q", tc.ids, err.Error(), tc.want)
			}
		})
	}
}

func TestExpandRequires(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want []string
	}{
		{
			name: "settings stays alone",
			ids:  []string{sectionSettings},
			want: []string{sectionSettings},
		},
		{
			name: "one level",
			ids:  []string{sectionGatewayKeys},
			want: []string{sectionGatewaySites, sectionGatewayKeys},
		},
		{
			name: "transitive",
			ids:  []string{sectionGatewayLogs},
			want: []string{sectionGatewaySites, sectionGatewayKeys, sectionGatewayLogs},
		},
		{
			name: "dedup and order",
			ids:  []string{sectionGatewayLogs, sectionGatewaySites, sectionGatewayKeys, sectionGatewaySites},
			want: []string{sectionGatewaySites, sectionGatewayKeys, sectionGatewayLogs},
		},
		{
			name: "unknown kept at end",
			ids:  []string{"nope", sectionSettings},
			want: []string{sectionSettings, "nope"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandRequires(tc.ids)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExpandRequires(%v) = %v, want %v", tc.ids, got, tc.want)
			}
		})
	}
}

func TestDefaultSections(t *testing.T) {
	cases := []struct {
		auto bool
		want []string
	}{
		{
			auto: false,
			want: []string{
				sectionSettings,
				sectionGatewaySites,
				sectionGatewayKeys,
				sectionGatewayLogs,
				sectionPlugins,
				sectionAgentConfig,
				sectionAgentCapabilities,
				sectionAgentSessions,
			},
		},
		{
			auto: true,
			want: []string{
				sectionSettings,
				sectionGatewaySites,
				sectionGatewayKeys,
				sectionGatewayLogs,
				sectionPlugins,
				sectionAgentConfig,
				sectionAgentCapabilities,
				sectionAgentSessions,
			},
		},
	}
	for _, tc := range cases {
		got := DefaultSections(tc.auto)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("DefaultSections(%v) = %v, want %v", tc.auto, got, tc.want)
		}
	}
}

func TestTablesOf(t *testing.T) {
	want := []string{
		"sites",
		"site_groups",
		"site_group_sites",
		"site_states",
		"oauth_connections",
		"site_credentials",
		"site_api_key_states",
		"canonical_models",
		"api_keys",
		"api_key_site_permissions",
		"api_key_site_group_permissions",
		"site_models",
		"site_model_endpoint_overrides",
		"api_key_site_model_permissions",
		"canonical_model_aliases",
		"site_api_key_models",
		"site_pricing_groups",
		"site_model_pricings",
		"gateway_rate_limits",
	}
	got := TablesOf([]string{sectionGatewayKeys, sectionGatewaySites})
	if !slices.Equal(got, want) {
		t.Fatalf("TablesOf order mismatch:\ngot  %v\nwant %v", got, want)
	}
	if got := TablesOf(nil); len(got) != 0 {
		t.Fatalf("TablesOf(nil) = %v, want empty", got)
	}
	if got := TablesOf([]string{sectionAgentConfig, "nope"}); len(got) != 0 {
		t.Fatalf("TablesOf(agent_config) = %v, want empty", got)
	}
	for _, table := range TablesOf(ExpandRequires([]string{sectionGatewayLogs})) {
		if strings.HasPrefix(table, "playground_") {
			t.Fatalf("TablesOf returned playground table %q", table)
		}
	}
}

func TestRestoreImpact(t *testing.T) {
	cases := []struct {
		name     string
		restored []string
		want     []SectionImpact
	}{
		{
			name:     "sites only",
			restored: []string{sectionGatewaySites},
			want: []SectionImpact{
				{ID: sectionGatewayKeys, Partial: true},
				{ID: sectionGatewayLogs, Partial: false},
			},
		},
		{
			name:     "sites and keys",
			restored: []string{sectionGatewaySites, sectionGatewayKeys},
			want: []SectionImpact{
				{ID: sectionGatewayLogs, Partial: false},
			},
		},
		{
			name:     "logs only",
			restored: []string{sectionGatewayLogs},
			want:     nil,
		},
		{
			name:     "settings only",
			restored: []string{sectionSettings},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RestoreImpact(tc.restored)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RestoreImpact(%v) = %+v, want %+v", tc.restored, got, tc.want)
			}
		})
	}
}

func TestClearedTables(t *testing.T) {
	want := []string{
		"sites",
		"site_groups",
		"site_group_sites",
		"site_states",
		"oauth_connections",
		"site_credentials",
		"site_api_key_states",
		"canonical_models",
		"api_key_site_permissions",
		"api_key_site_group_permissions",
		"site_models",
		"site_model_endpoint_overrides",
		"api_key_site_model_permissions",
		"canonical_model_aliases",
		"site_api_key_models",
		"site_pricing_groups",
		"site_model_pricings",
		"request_logs",
		"usage_records",
		"request_usage_daily_summaries",
		"request_usage_summary_days",
	}
	got := ClearedTables([]string{sectionGatewaySites})
	if !slices.Equal(got, want) {
		t.Fatalf("ClearedTables([gateway_sites]) order mismatch:\ngot  %v\nwant %v", got, want)
	}
	logsOnly := ClearedTables([]string{sectionGatewayLogs})
	if !slices.Equal(logsOnly, []string{
		"request_logs",
		"usage_records",
		"request_usage_daily_summaries",
		"request_usage_summary_days",
	}) {
		t.Fatalf("ClearedTables([gateway_logs]) = %v", logsOnly)
	}
	if got := ClearedTables([]string{sectionSettings, sectionAgentConfig}); len(got) != 0 {
		t.Fatalf("ClearedTables(config-only sections) = %v, want empty", got)
	}
}

func TestFKDescendants(t *testing.T) {
	got := fkDescendants([]string{"api_keys"})
	if !slices.Equal(got, []string{
		"api_key_site_permissions",
		"api_key_site_group_permissions",
		"api_key_site_model_permissions",
		"gateway_rate_limits",
		"request_logs",
		"usage_records",
	}) {
		t.Fatalf("fkDescendants([api_keys]) = %v", got)
	}
	// 传递依赖:canonical_models → site_models → site_model_pricings。
	got = fkDescendants([]string{"canonical_models"})
	if !slices.Contains(got, "site_model_pricings") || !slices.Contains(got, "canonical_model_aliases") {
		t.Fatalf("fkDescendants([canonical_models]) missing transitive children: %v", got)
	}
	// 入参本身是其它入参的子表时不重复出现。
	got = fkDescendants([]string{"sites", "site_models"})
	if slices.Contains(got, "site_models") {
		t.Fatalf("fkDescendants includes input table: %v", got)
	}
}

// TestTableFKChildrenMatchesDatabase 用开发 PostgreSQL 校验 tableFKChildren 与
// 真实 schema 的外键一致:只比较两端都是备份表的边,忽略 playground_*。
func TestTableFKChildrenMatchesDatabase(t *testing.T) {
	st := openAgentBackupTestStore(t)
	db := st.DB()
	var edges []struct {
		Child  string
		Parent string
	}
	err := db.Raw(`
		SELECT conrelid::regclass::text AS child, confrelid::regclass::text AS parent
		FROM pg_constraint
		WHERE contype = 'f'
		  AND connamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())
	`).Scan(&edges).Error
	if err != nil {
		t.Skipf("query pg_constraint failed: %v", err)
	}
	backupSet := make(map[string]struct{}, len(backupTables))
	for _, table := range backupTables {
		backupSet[table.Name] = struct{}{}
	}
	dbChildren := make(map[string]map[string]struct{})
	for _, edge := range edges {
		if _, ok := backupSet[edge.Parent]; !ok {
			continue
		}
		if _, ok := backupSet[edge.Child]; !ok {
			continue
		}
		if strings.HasPrefix(edge.Parent, "playground_") || strings.HasPrefix(edge.Child, "playground_") {
			continue
		}
		set := dbChildren[edge.Parent]
		if set == nil {
			set = make(map[string]struct{})
			dbChildren[edge.Parent] = set
		}
		set[edge.Child] = struct{}{}
	}
	for parent, children := range tableFKChildren {
		got := dbChildren[parent]
		if got == nil {
			got = map[string]struct{}{}
		}
		want := make(map[string]struct{}, len(children))
		for _, child := range children {
			want[child] = struct{}{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tableFKChildren[%q] = %v, database says %v", parent, want, got)
		}
	}
	for parent, got := range dbChildren {
		if _, ok := tableFKChildren[parent]; !ok {
			t.Fatalf("database has backup-table FK children for %q missing from tableFKChildren: %v", parent, got)
		}
	}
}
