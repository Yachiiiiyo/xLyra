package backup

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seedPluginBackupStore 在测试库里种入:站点 + 模型 + 站点级端点覆盖 + API 密钥,
// 以及插件(含 package 字节、版本、绑定、绑定输入、信任公钥、市场订阅)。
// 返回 package 的原始字节,供往返后比对。
func seedPluginBackupStore(t *testing.T, db *gorm.DB) (siteModelID uuid.UUID, pluginPackage []byte) {
	t.Helper()

	siteID := uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	apiKeyID := uuid.MustParse("00000000-0000-0000-0000-0000000000d2")
	siteModelID = uuid.MustParse("00000000-0000-0000-0000-0000000000d3")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000d4")
	bindingID := uuid.MustParse("00000000-0000-0000-0000-0000000000d5")

	if err := db.Exec(`INSERT INTO sites (id, name, slug, site_type, base_url, status, enabled, routing_priority)
		VALUES (?, 'plugin-site', 'plugin-site', 'openai', 'https://plugin.example', 'active', true, 1)`, siteID).Error; err != nil {
		t.Fatalf("seed site: %v", err)
	}
	if err := db.Exec(`INSERT INTO api_keys (id, name, key_prefix, key_hash, masked_key, scope, status)
		VALUES (?, 'plugin-key', 'sk-plugin', 'hash-plugin', 'sk-***-plugin', 'all', 'active')`, apiKeyID).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.Exec(`INSERT INTO site_models (id, site_id, upstream_model_name, display_name)
		VALUES (?, ?, 'gpt-plugin', 'GPT Plugin')`, siteModelID, siteID).Error; err != nil {
		t.Fatalf("seed site model: %v", err)
	}
	if err := db.Exec(`INSERT INTO site_model_endpoint_overrides (site_model_id, mode, endpoint_types, reason)
		VALUES (?, 'allowlist', '["chat.completions"]', '测试覆盖')`, siteModelID).Error; err != nil {
		t.Fatalf("seed site model endpoint override: %v", err)
	}
	if err := db.Exec(`INSERT INTO admins (id, username, password_hash)
		VALUES (?, 'backup-admin', 'hash-admin')`, adminID).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugins (id, source) VALUES ('demo-plugin', 'uploaded')`).Error; err != nil {
		t.Fatalf("seed js plugin: %v", err)
	}
	// 含非 UTF-8 字节(0xff、0x00)的假 zip 包,验证二进制列往返无损。
	pluginPackage = []byte{'P', 'K', 0x03, 0x04, 0xff, 0xfe, 0x00, 'x', 0x80, 'y'}
	if err := db.Exec(`INSERT INTO js_plugin_versions
		(plugin_id, version, manifest, package, package_sha256, signer, status, selftest, uploaded_by, origin, attestation, granted_permissions)
		VALUES ('demo-plugin', '1.2.0', '{"name":"demo"}', ?, 'sha256-demo', 'signer-demo', 'enabled', '{}', ?, 'manual', NULL, '[]')`,
		pluginPackage, adminID).Error; err != nil {
		t.Fatalf("seed js plugin version: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugin_trusted_keys (id, name, public_key, fingerprint, created_by)
		VALUES (?, 'demo-key', 'public-key-demo', 'fp-demo', ?)`, uuid.New(), adminID).Error; err != nil {
		t.Fatalf("seed js plugin trusted key: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugin_bindings (id, plugin_id, version, kind, scope_type, scope_id, slot, config)
		VALUES (?, 'demo-plugin', '1.2.0', 'protocol', 'endpoint', '/v1/plugins/demo', 'protocol', '{"timeout":30}')`, bindingID).Error; err != nil {
		t.Fatalf("seed js plugin binding: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugin_binding_inputs (binding_id, input_name, entity_type, entity_id, event_subject, applied)
		VALUES (?, 'accounts', 'site', ?, true, '{"scope":"demo"}')`, bindingID, siteID).Error; err != nil {
		t.Fatalf("seed js plugin binding input: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugin_market_subscriptions (plugin_id, update_policy, paused_reason, notified_version)
		VALUES ('demo-plugin', 'auto', '', '1.1.0')`).Error; err != nil {
		t.Fatalf("seed js plugin market subscription: %v", err)
	}
	return siteModelID, pluginPackage
}

// TestImportRestorePluginsRoundTrip 真库往返:导出 [gateway_sites, gateway_keys,
// plugins] 后清库再导入,行内容一致、package 字节完全一致、引用管理员的列为 NULL。
func TestImportRestorePluginsRoundTrip(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	db := st.DB()
	siteModelID, pluginPackage := seedPluginBackupStore(t, db)
	service := NewService(st, confFile, "master-key")

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase",
		[]string{sectionGatewaySites, sectionGatewayKeys, sectionPlugins})
	if err != nil {
		t.Fatalf("ExportSections: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	// 清空全部备份表(含级联),再导入备份。
	if err := db.Exec("TRUNCATE TABLE " + quotedBackupTables() + " CASCADE").Error; err != nil {
		t.Fatalf("truncate backup tables: %v", err)
	}

	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !slices.Equal(summary.Sections, []string{sectionGatewaySites, sectionGatewayKeys, sectionPlugins}) {
		t.Fatalf("sections = %#v", summary.Sections)
	}

	var override struct {
		Mode   string
		Reason string
	}
	if err := db.Raw("SELECT mode, reason FROM site_model_endpoint_overrides WHERE site_model_id = ?", siteModelID).
		Scan(&override).Error; err != nil || override.Mode != "allowlist" || override.Reason != "测试覆盖" {
		t.Fatalf("restored site_model_endpoint_overrides mode=%q reason=%q err=%v", override.Mode, override.Reason, err)
	}

	var restoredPackage []byte
	if err := db.Raw("SELECT package FROM js_plugin_versions WHERE plugin_id = 'demo-plugin' AND version = '1.2.0'").
		Row().Scan(&restoredPackage); err != nil {
		t.Fatalf("read restored package: %v", err)
	}
	if string(restoredPackage) != string(pluginPackage) {
		t.Fatalf("package bytes differ:\ngot  %v\nwant %v", restoredPackage, pluginPackage)
	}
	for _, table := range []string{"js_plugins", "js_plugin_versions", "js_plugin_trusted_keys",
		"js_plugin_bindings", "js_plugin_binding_inputs", "js_plugin_market_subscriptions"} {
		if got := countTableRows(t, db, table); got != 1 {
			t.Fatalf("%s rows = %d, want 1", table, got)
		}
	}

	// 管理员账号不在备份里,引用管理员的列导入后必须为 NULL。
	for _, column := range []string{"uploaded_by", "created_by"} {
		table := "js_plugin_versions"
		if column == "created_by" {
			table = "js_plugin_trusted_keys"
		}
		var isNull bool
		if err := db.Raw("SELECT " + column + " IS NULL FROM " + table + " LIMIT 1").Scan(&isNull).Error; err != nil {
			t.Fatalf("read %s.%s: %v", table, column, err)
		}
		if !isNull {
			t.Fatalf("%s.%s is not NULL, want NULL", table, column)
		}
	}

	var config string
	if err := db.Raw("SELECT config FROM js_plugin_bindings LIMIT 1").Scan(&config).Error; err != nil {
		t.Fatalf("read binding config: %v", err)
	}
	// JSONB 会规范化空白,按 JSON 语义比较。
	var parsed map[string]any
	if err := json.Unmarshal([]byte(config), &parsed); err != nil || parsed["timeout"] != float64(30) || len(parsed) != 1 {
		t.Fatalf("restored binding config = %q err=%v", config, err)
	}
}

// TestImportRestoreSitesAndPluginsOnly 只导出 [gateway_sites, plugins] 再导入:
// site_model_endpoint_overrides 恢复;api_keys 保留;插件表保持不变
// (插件表与站点表之间没有外键,不会被级联清空)。
func TestImportRestoreSitesAndPluginsOnly(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	db := st.DB()
	seedPluginBackupStore(t, db)
	service := NewService(st, confFile, "master-key")

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase",
		[]string{sectionGatewaySites, sectionPlugins})
	if err != nil {
		t.Fatalf("ExportSections: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	summary, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := countTableRows(t, db, "site_model_endpoint_overrides"); got != 1 {
		t.Fatalf("site_model_endpoint_overrides rows = %d, want 1 (restored)", got)
	}
	// site_models 被截断,api_keys 保留;密钥级端点覆盖保存在 site_api_key_models.raw
	// 里,随 site_models 一起被级联清空。
	if got := countTableRows(t, db, "api_keys"); got != 1 {
		t.Fatalf("api_keys rows = %d, want 1 (retained)", got)
	}
	for _, table := range []string{"js_plugins", "js_plugin_versions", "js_plugin_trusted_keys",
		"js_plugin_bindings", "js_plugin_binding_inputs", "js_plugin_market_subscriptions"} {
		if got := countTableRows(t, db, table); got != 1 {
			t.Fatalf("%s rows = %d, want 1 (plugins unaffected)", table, got)
		}
	}
	want := []SectionImpact{
		{ID: sectionGatewayKeys, Partial: true},
		{ID: sectionGatewayLogs, Partial: false},
	}
	if !slices.Equal(summary.ClearedSections, want) {
		t.Fatalf("cleared sections = %#v, want %#v", summary.ClearedSections, want)
	}
}

// quotedBackupTables 返回全部备份表的带引号逗号连接,供 TRUNCATE 使用。
func quotedBackupTables() string {
	quoted := make([]string, 0, len(backupTables))
	for _, table := range backupTables {
		quoted = append(quoted, `"`+table.Name+`"`)
	}
	return strings.Join(quoted, ", ")
}

// TestImportRestoreSitesPrunesDanglingPluginInputs 只恢复 [gateway_sites](备份里没有
// 插件类别)时,插件自动化输入里指向已不存在对象的行被清理,仍然有效的行保留。
func TestImportRestoreSitesPrunesDanglingPluginInputs(t *testing.T) {
	st := openAgentBackupTestStore(t)
	confFile := backupConfigFile(t)
	db := st.DB()
	seedPluginBackupStore(t, db)
	service := NewService(st, confFile, "master-key")

	archivePath, _, err := service.ExportSections(context.Background(), "passphrase", []string{sectionGatewaySites})
	if err != nil {
		t.Fatalf("ExportSections: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(archivePath) })
	encrypted, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read encrypted backup: %v", err)
	}

	// 导出之后新增一条指向“备份里不存在的站点”的输入,以及一条指向仍存在的 API 密钥的输入。
	bindingID := uuid.MustParse("00000000-0000-0000-0000-0000000000d5")
	missingSite := uuid.New()
	if err := db.Exec(`INSERT INTO js_plugin_binding_inputs (binding_id, input_name, entity_type, entity_id, event_subject, applied)
		VALUES (?, 'ghost', 'site', ?, false, '{}')`, bindingID, missingSite).Error; err != nil {
		t.Fatalf("insert dangling input: %v", err)
	}
	if err := db.Exec(`INSERT INTO js_plugin_binding_inputs (binding_id, input_name, entity_type, entity_id, event_subject, applied)
		VALUES (?, 'keys', 'api_key', ?, false, '{}')`, bindingID, "00000000-0000-0000-0000-0000000000d2").Error; err != nil {
		t.Fatalf("insert api key input: %v", err)
	}

	if _, err := service.Import(context.Background(), "passphrase", encrypted, ImportOptions{}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	var names []string
	if err := db.Raw("SELECT input_name FROM js_plugin_binding_inputs ORDER BY input_name").Scan(&names).Error; err != nil {
		t.Fatalf("read inputs: %v", err)
	}
	// ghost(站点已不存在)被清理;accounts(站点在备份里)与 keys(api_keys 没被替换)保留。
	if !slices.Equal(names, []string{"accounts", "keys"}) {
		t.Fatalf("remaining plugin inputs = %v, want [accounts keys]", names)
	}
}
