package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"xlyra/server/internal/crypto"
	"xlyra/server/internal/store"
)

type databaseDump struct {
	Tables    map[string][]map[string]any `json:"tables"`
	TotalRows int
	// Present 是备份包里实际存在的表(nil 表示全部备份表,零值保持旧行为)。
	Present []string
	// Clear 是导入前需要清空的表(nil 表示按 importDeleteOrder 全部清空)。
	Clear        []string
	beforeCommit func() error
	// archivePath, when set, points at the on-disk archive so the large detail
	// tables (streamedImportTables) can be streamed from it during import instead
	// of being held in Tables. Empty means everything lives in Tables (tests).
	archivePath string
}

const (
	exportBatchSize       = 1000
	importBatchSize       = 2000
	importStreamChunkSize = 20000
	importParameterBudget = 60000
)

// streamedImportTables are the large detail tables imported by streaming from
// the archive in chunks rather than loading every row into memory at once.
var streamedImportTables = map[string]struct{}{
	"request_logs":  {},
	"usage_records": {},
}

var exportTables = []string{
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
	"request_logs",
	"usage_records",
	"request_usage_daily_summaries",
	"request_usage_summary_days",
	"js_plugins",
	"js_plugin_versions",
	"js_plugin_trusted_keys",
	"js_plugin_bindings",
	"js_plugin_binding_inputs",
	"js_plugin_market_subscriptions",
}

type backupTable struct {
	Name     string
	Model    any
	NewSlice func() any
}

var backupTables = []backupTable{
	{Name: "sites", Model: &store.Site{}, NewSlice: func() any { return &[]store.Site{} }},
	{Name: "site_groups", Model: &store.SiteGroup{}, NewSlice: func() any { return &[]store.SiteGroup{} }},
	{Name: "site_group_sites", Model: &store.SiteGroupSite{}, NewSlice: func() any { return &[]store.SiteGroupSite{} }},
	{Name: "site_states", Model: &store.SiteState{}, NewSlice: func() any { return &[]store.SiteState{} }},
	{Name: "oauth_connections", Model: &store.OAuthConnection{}, NewSlice: func() any { return &[]store.OAuthConnection{} }},
	{Name: "site_credentials", Model: &store.SiteCredential{}, NewSlice: func() any { return &[]store.SiteCredential{} }},
	{Name: "site_api_key_states", Model: &store.SiteAPIKeyState{}, NewSlice: func() any { return &[]store.SiteAPIKeyState{} }},
	{Name: "canonical_models", Model: &store.CanonicalModel{}, NewSlice: func() any { return &[]store.CanonicalModel{} }},
	{Name: "api_keys", Model: &store.APIKey{}, NewSlice: func() any { return &[]store.APIKey{} }},
	{Name: "api_key_site_permissions", Model: &store.APIKeySitePermission{}, NewSlice: func() any { return &[]store.APIKeySitePermission{} }},
	{Name: "api_key_site_group_permissions", Model: &store.APIKeySiteGroupPermission{}, NewSlice: func() any { return &[]store.APIKeySiteGroupPermission{} }},
	{Name: "site_models", Model: &store.SiteModel{}, NewSlice: func() any { return &[]store.SiteModel{} }},
	{Name: "site_model_endpoint_overrides", Model: &store.SiteModelEndpointOverride{}, NewSlice: func() any { return &[]store.SiteModelEndpointOverride{} }},
	{Name: "api_key_site_model_permissions", Model: &store.APIKeySiteModelPermission{}, NewSlice: func() any { return &[]store.APIKeySiteModelPermission{} }},
	{Name: "canonical_model_aliases", Model: &store.CanonicalModelAlias{}, NewSlice: func() any { return &[]store.CanonicalModelAlias{} }},
	{Name: "site_api_key_models", Model: &store.SiteAPIKeyModel{}, NewSlice: func() any { return &[]store.SiteAPIKeyModel{} }},
	{Name: "site_pricing_groups", Model: &store.SitePricingGroup{}, NewSlice: func() any { return &[]store.SitePricingGroup{} }},
	{Name: "site_model_pricings", Model: &store.SiteModelPricing{}, NewSlice: func() any { return &[]store.SiteModelPricing{} }},
	{Name: "gateway_rate_limits", Model: &store.GatewayRateLimit{}, NewSlice: func() any { return &[]store.GatewayRateLimit{} }},
	{Name: "request_logs", Model: &store.RequestLog{}, NewSlice: func() any { return &[]store.RequestLog{} }},
	{Name: "usage_records", Model: &store.UsageRecord{}, NewSlice: func() any { return &[]store.UsageRecord{} }},
	{Name: "request_usage_daily_summaries", Model: &store.RequestUsageDailySummary{}, NewSlice: func() any { return &[]store.RequestUsageDailySummary{} }},
	{Name: "request_usage_summary_days", Model: &store.RequestUsageSummaryDay{}, NewSlice: func() any { return &[]store.RequestUsageSummaryDay{} }},
	{Name: "js_plugins", Model: &store.JSPlugin{}, NewSlice: func() any { return &[]store.JSPlugin{} }},
	{Name: "js_plugin_versions", Model: &store.JSPluginVersion{}, NewSlice: func() any { return &[]store.JSPluginVersion{} }},
	{Name: "js_plugin_trusted_keys", Model: &store.JSPluginTrustedKey{}, NewSlice: func() any { return &[]store.JSPluginTrustedKey{} }},
	{Name: "js_plugin_bindings", Model: &store.JSPluginBinding{}, NewSlice: func() any { return &[]store.JSPluginBinding{} }},
	{Name: "js_plugin_binding_inputs", Model: &store.JSPluginBindingInput{}, NewSlice: func() any { return &[]store.JSPluginBindingInput{} }},
	{Name: "js_plugin_market_subscriptions", Model: &store.JSPluginMarketSubscription{}, NewSlice: func() any { return &[]store.JSPluginMarketSubscription{} }},
}

var importDeleteOrder = []string{
	"request_usage_summary_days",
	"request_usage_daily_summaries",
	"usage_records",
	"request_logs",
	"gateway_rate_limits",
	"site_model_pricings",
	"site_model_endpoint_overrides",
	"site_pricing_groups",
	"site_api_key_models",
	"canonical_model_aliases",
	"api_key_site_model_permissions",
	"site_models",
	"api_key_site_group_permissions",
	"api_key_site_permissions",
	"api_keys",
	"canonical_models",
	"site_api_key_states",
	"site_credentials",
	"oauth_connections",
	"site_states",
	"site_group_sites",
	"site_groups",
	"sites",
	// 插件表与站点表没有外键,子表在前、父表在后。
	"js_plugin_binding_inputs",
	"js_plugin_bindings",
	"js_plugin_versions",
	"js_plugin_market_subscriptions",
	"js_plugin_trusted_keys",
	"js_plugins",
}

var textSecretColumns = map[string][]string{
	"api_keys":          {"encrypted_secret"},
	"site_credentials":  {"encrypted_secret"},
	"oauth_connections": {"encrypted_access_token", "encrypted_refresh_token", "encrypted_id_token"},
}

// binaryColumns 列出二进制(BYTEA)列:导出时编码为 base64 字符串,导入时解码
// 回字节,保证经 JSONL 往返后字节完全一致(JSON 无法无损携带任意二进制)。
var binaryColumns = map[string][]string{
	"js_plugin_versions": {"package"},
}

// adminReferenceColumns 列出引用 admins 表的外键列:管理员账号不在备份里,
// 导入时一律置空(参照 api_keys.created_by_admin_id 的做法)。
var adminReferenceColumns = map[string][]string{
	"js_plugin_versions":     {"uploaded_by"},
	"js_plugin_trusted_keys": {"created_by"},
}

// exportDatabase 只导出 tables 里列出的表(按 backupTables 顺序);tables 为空
// 时不开启事务,直接返回空 row_counts。
func exportDatabase(ctx context.Context, db *gorm.DB, masterKey string, tables []string, zw *zip.Writer) (map[string]int, error) {
	if len(tables) == 0 {
		return map[string]int{}, nil
	}
	var rowCounts map[string]int
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		rowCounts, err = exportDatabaseSnapshot(ctx, tx, masterKey, tables, zw)
		return err
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return rowCounts, nil
}

func exportDatabaseSnapshot(ctx context.Context, db *gorm.DB, masterKey string, tables []string, zw *zip.Writer) (map[string]int, error) {
	wanted := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		wanted[table] = struct{}{}
	}
	rowCounts := make(map[string]int, len(wanted))
	for _, table := range backupTables {
		if _, ok := wanted[table.Name]; !ok {
			continue
		}
		rows, err := exportTable(ctx, db, table, masterKey, zw)
		if err != nil {
			return nil, err
		}
		rowCounts[table.Name] = rows
	}
	return rowCounts, nil
}

func importDatabase(ctx context.Context, db *gorm.DB, masterKey string, dump databaseDump, adminID uuid.UUID, progress ...ProgressFunc) (int, int, error) {
	if err := validateDumpTables(dump); err != nil {
		return 0, 0, err
	}
	// 要插入的表 = Present(nil 表示全部);要清空的表 = Clear(nil 表示全部)。
	// 每个要插入的表必须在清空集合里,否则插入后会与库内旧数据主键冲突。
	present := importPresentTables(dump)
	presentSet := make(map[string]struct{}, len(present))
	for _, table := range present {
		presentSet[table] = struct{}{}
	}
	clear := importClearTables(dump)
	clearSet := make(map[string]struct{}, len(clear))
	for _, table := range clear {
		if _, ok := backupTableByName(table); !ok {
			return 0, 0, fmt.Errorf("backup table %s was not registered", table)
		}
		clearSet[table] = struct{}{}
	}
	for _, table := range present {
		if _, ok := clearSet[table]; !ok {
			return 0, 0, fmt.Errorf("backup table %s is present but not cleared", table)
		}
	}
	streaming := dump.archivePath != ""
	if !streaming {
		dump = normalizeImportDump(dump)
	}
	progressTotal := dump.TotalRows
	if !streaming {
		progressTotal = 0
		for _, table := range backupTables {
			progressTotal += len(dump.Tables[table.Name])
		}
	}

	var prog ProgressFunc
	if len(progress) > 0 {
		prog = progress[0]
	}
	emit := func(ev ProgressEvent) {
		if prog != nil {
			prog(ev)
		}
	}

	// Reference sets for the streamed detail tables' integrity filtering. The
	// dimension tables are small and always held in memory.
	requestLogRefs := map[string]map[string]struct{}{
		"api_key_id":         rowIDSet(dump.Tables["api_keys"]),
		"site_id":            rowIDSet(dump.Tables["sites"]),
		"canonical_model_id": rowIDSet(dump.Tables["canonical_models"]),
		"site_model_id":      rowIDSet(dump.Tables["site_models"]),
	}
	usageRecordRefs := map[string]map[string]struct{}{
		"api_key_id":         requestLogRefs["api_key_id"],
		"site_id":            requestLogRefs["site_id"],
		"canonical_model_id": requestLogRefs["canonical_model_id"],
	}
	requestLogIDs := make(map[string]struct{})

	requestLogTransform := func(rows []map[string]any) []map[string]any {
		stripCacheObservationRows(rows)
		rowsWithValidNullableReferences(rows, requestLogRefs)
		for _, row := range rows {
			if id := stringValue(row["id"]); id != "" {
				requestLogIDs[id] = struct{}{}
			}
		}
		return rows
	}
	usageRecordTransform := func(rows []map[string]any) []map[string]any {
		rows = rowsWithExistingParent(rows, "request_log_id", requestLogIDs)
		rowsWithValidNullableReferences(rows, usageRecordRefs)
		return rows
	}

	total := 0
	skippedTotal := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 清空与插入都为空(例如只恢复配置)时,不动库内任何数据。
		if len(clear) == 0 && len(present) == 0 {
			if dump.beforeCommit != nil {
				return dump.beforeCommit()
			}
			return nil
		}
		if _, ok := clearSet["request_logs"]; ok {
			if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&store.CacheObservation{}).Error; err != nil {
				return fmt.Errorf("clear cache observations: %w", err)
			}
		}
		// Clear every backup table before reloading. A single TRUNCATE (vs a
		// per-table DELETE) reclaims storage immediately instead of leaving dead
		// tuples that bloat each table on every restore, and is near-instant.
		// CASCADE also clears the transient non-backup tables that reference these
		// via ON DELETE CASCADE foreign keys (route_cooldowns, health_snapshots,
		// site_health_states, oauth_sessions) — matching the DELETE's prior cascade
		// behavior. TRUNCATE has no GORM API, so it is issued directly; table names
		// come from the fixed Clear/importDeleteOrder list, never user input.
		if len(clear) > 0 {
			quoted := make([]string, 0, len(clear))
			for _, table := range clear {
				quoted = append(quoted, `"`+table+`"`)
			}
			if err := tx.Exec("TRUNCATE TABLE " + strings.Join(quoted, ", ") + " CASCADE").Error; err != nil {
				return fmt.Errorf("clear backup tables: %w", err)
			}
		}
		for _, table := range backupTables {
			if _, ok := presentSet[table.Name]; !ok {
				continue
			}
			switch table.Name {
			case "request_logs", "usage_records":
				transform := requestLogTransform
				if table.Name == "usage_records" {
					transform = usageRecordTransform
				}
				baseSkipped := skippedTotal
				n, skipped, err := importStreamedTable(ctx, tx, dump, table, transform, func(rows int, skipped int) {
					emit(ProgressEvent{Step: "import", Status: "in_progress", Rows: total + rows, TotalRows: progressTotal - baseSkipped - skipped, Table: table.Name})
				})
				if err != nil {
					return err
				}
				total += n
				skippedTotal += skipped
			default:
				rows := cloneRows(dump.Tables[table.Name])
				if err := decodeBinaryColumns(table.Name, rows); err != nil {
					return err
				}
				if err := encryptTableSecrets(table.Name, rows, masterKey); err != nil {
					return err
				}
				if table.Name == "api_keys" {
					for _, row := range rows {
						row["created_by_admin_id"] = nil
					}
				}
				// 管理员账号不在备份里,引用管理员的外键列导入时一律置空。
				for _, column := range adminReferenceColumns[table.Name] {
					for _, row := range rows {
						row[column] = nil
					}
				}
				if err := importTable(ctx, tx, table, rows, func(imported int) {
					emit(ProgressEvent{Step: "import", Status: "in_progress", Rows: total + imported, TotalRows: progressTotal - skippedTotal, Table: table.Name})
				}); err != nil {
					return err
				}
				if table.Name == "api_keys" {
					if err := restoreAPIKeyPeriodicQuotaFlags(ctx, tx, rows); err != nil {
						return err
					}
				}
				total += len(rows)
			}
		}
		// 站点/密钥/OAuth 连接被替换后,备份之外的插件自动化输入可能还引用着已不存在
		// 的对象(文本 id,没有外键):只清理这些悬空引用,不动仍然有效的输入。
		if err := pruneDanglingPluginInputs(tx, clearSet, presentSet); err != nil {
			return err
		}
		if dump.beforeCommit != nil {
			if err := dump.beforeCommit(); err != nil {
				return err
			}
		}
		return nil
	})
	return total, progressTotal - skippedTotal, err
}

// pluginInputEntityTables 是插件自动化输入(js_plugin_binding_inputs.entity_type)
// 引用的对象类型与其所在的表。
var pluginInputEntityTables = map[string]string{
	"site":             "sites",
	"api_key":          "api_keys",
	"oauth_connection": "oauth_connections",
}

// pruneDanglingPluginInputs 在恢复替换了站点/密钥/OAuth 连接之后,删除插件自动化
// 输入里指向已不存在对象的行。备份本身带了插件输入表(present)时不处理——备份内
// 容自洽;没带时(只恢复站点等)才清理悬空引用。
func pruneDanglingPluginInputs(tx *gorm.DB, clearSet map[string]struct{}, presentSet map[string]struct{}) error {
	if _, ok := presentSet["js_plugin_binding_inputs"]; ok {
		return nil
	}
	for entityType, table := range pluginInputEntityTables {
		if _, replaced := clearSet[table]; !replaced {
			continue
		}
		query := "DELETE FROM js_plugin_binding_inputs WHERE entity_type = ? AND entity_id NOT IN (SELECT id::text FROM " + table + ")"
		if err := tx.Exec(query, entityType).Error; err != nil {
			return fmt.Errorf("prune plugin inputs referencing %s: %w", table, err)
		}
	}
	return nil
}

// importStreamedTable imports a large detail table. With an archive path it
// decodes the table's rows in bounded chunks straight from the archive (peak
// memory ~one chunk plus the reference sets) instead of materializing every row;
// without one it falls back to the in-memory dump (already normalized). The
// transform applies referential-integrity filtering per chunk.
func importStreamedTable(ctx context.Context, tx *gorm.DB, dump databaseDump, table backupTable, transform func([]map[string]any) []map[string]any, progress func(int, int)) (int, int, error) {
	if dump.archivePath == "" {
		rows := dump.Tables[table.Name]
		if len(rows) == 0 {
			return 0, 0, nil
		}
		if transform != nil {
			rows = transform(rows)
		}
		if err := importTable(ctx, tx, table, rows, func(imported int) {
			if progress != nil {
				progress(imported, 0)
			}
		}); err != nil {
			return 0, 0, err
		}
		return len(rows), 0, nil
	}

	total := 0
	skipped := 0
	err := forEachArchiveTableChunk(dump.archivePath, table.Name, importStreamChunkSize, func(chunk []map[string]any) error {
		rows := chunk
		if transform != nil {
			rows = transform(rows)
		}
		skipped += len(chunk) - len(rows)
		if len(rows) == 0 {
			if progress != nil {
				progress(total, skipped)
			}
			return nil
		}
		if err := importTable(ctx, tx, table, rows, func(imported int) {
			if progress != nil {
				progress(total+imported, skipped)
			}
		}); err != nil {
			return err
		}
		total += len(rows)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return total, skipped, nil
}

// forEachArchiveTableChunk decodes a table's JSONL entry from the archive and
// invokes fn with successive chunks of up to chunkSize rows, bounding peak
// memory to a single chunk. The chunk slice is reused between calls, so fn must
// not retain it beyond the call.
func forEachArchiveTableChunk(archivePath string, tableName string, chunkSize int, fn func([]map[string]any) error) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()
	name := path.Join("database", tableName+".jsonl")
	file := archiveFile(zr.File, name)
	if file == nil {
		return nil
	}
	reader, err := file.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer reader.Close()

	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	chunk := make([]map[string]any, 0, chunkSize)
	for {
		var row map[string]any
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode %s: %w", name, err)
		}
		chunk = append(chunk, row)
		if len(chunk) >= chunkSize {
			if err := fn(chunk); err != nil {
				return err
			}
			chunk = chunk[:0]
		}
	}
	if len(chunk) > 0 {
		return fn(chunk)
	}
	return nil
}

func exportTable(ctx context.Context, db *gorm.DB, table backupTable, masterKey string, zw *zip.Writer) (int, error) {
	w, err := createArchiveEntry(zw, path.Join("database", table.Name+".jsonl"), zip.Deflate)
	if err != nil {
		return 0, err
	}
	encoder := json.NewEncoder(w)
	rows, err := exportTableRows(ctx, db, table, masterKey, encoder)
	if err != nil {
		return 0, fmt.Errorf("export %s: %w", table.Name, err)
	}
	return rows, nil
}

func exportTableRows(ctx context.Context, db *gorm.DB, table backupTable, masterKey string, encoder *json.Encoder) (int, error) {
	slice := table.NewSlice()
	query := db.WithContext(ctx).Model(table.Model)
	parsed, err := schema.Parse(table.Model, &sync.Map{}, db.NamingStrategy)
	if err != nil {
		return 0, err
	}
	if order, err := primaryOrderClause(db, table.Model); err != nil {
		return 0, err
	} else if len(order.Columns) > 0 {
		query = query.Clauses(order)
	}
	total := 0
	err = query.FindInBatches(slice, exportBatchSize, func(_ *gorm.DB, _ int) error {
		rows, err := modelRows(ctx, parsed, slice)
		if err != nil {
			return err
		}
		if table.Name == "request_logs" {
			stripCacheObservationRows(rows)
		}
		encodeBinaryColumns(table.Name, rows)
		if err := decryptTableSecrets(table.Name, rows, masterKey); err != nil {
			return err
		}
		for _, row := range rows {
			if err := encoder.Encode(row); err != nil {
				return err
			}
		}
		total += len(rows)
		return nil
	}).Error
	return total, err
}

func importTable(ctx context.Context, db *gorm.DB, table backupTable, rows []map[string]any, progress ...func(int)) error {
	if len(rows) == 0 {
		return nil
	}
	parsed, err := schema.Parse(table.Model, &sync.Map{}, db.NamingStrategy)
	if err != nil {
		return err
	}
	items := table.NewSlice()
	slice := reflect.ValueOf(items)
	if slice.Kind() != reflect.Pointer || slice.IsNil() || slice.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("backup table %s must provide a slice pointer", table.Name)
	}
	sliceValue := slice.Elem()
	itemType := sliceValue.Type().Elem()
	for _, row := range rows {
		item := reflect.New(itemType).Elem()
		if err := applyRowToModel(ctx, parsed, item, row); err != nil {
			return fmt.Errorf("prepare %s row: %w", table.Name, err)
		}
		if item.IsZero() {
			continue
		}
		sliceValue.Set(reflect.Append(sliceValue, item))
	}
	if sliceValue.Len() == 0 {
		return nil
	}
	if keys, ok := items.(*[]store.APIKey); ok {
		store.BackfillAPIKeyOrder(*keys)
	}
	var report func(int)
	if len(progress) > 0 {
		report = progress[0]
	}
	batchSize := importBatchSizeForSchema(parsed)
	for start := 0; start < sliceValue.Len(); start += batchSize {
		end := min(start+batchSize, sliceValue.Len())
		if err := db.WithContext(ctx).Create(sliceValue.Slice(start, end).Interface()).Error; err != nil {
			return fmt.Errorf("insert %s: %w", table.Name, err)
		}
		if report != nil {
			report(end)
		}
	}
	return nil
}

func importBatchSizeForSchema(parsed *schema.Schema) int {
	columns := len(parsed.DBNames)
	if columns == 0 {
		return importBatchSize
	}
	return max(1, min(importBatchSize, importParameterBudget/columns))
}

func restoreAPIKeyPeriodicQuotaFlags(ctx context.Context, db *gorm.DB, rows []map[string]any) error {
	for _, column := range []string{"quota_daily_unlimited", "quota_weekly_unlimited"} {
		ids, err := apiKeyIDsWithExplicitFalse(rows, column)
		if err != nil {
			return err
		}
		for start := 0; start < len(ids); start += importBatchSize {
			end := min(start+importBatchSize, len(ids))
			values := make([]any, end-start)
			for i, id := range ids[start:end] {
				values[i] = id
			}
			result := db.WithContext(ctx).Model(&store.APIKey{}).Clauses(clause.Where{Exprs: []clause.Expression{
				clause.IN{Column: clause.Column{Name: "id"}, Values: values},
			}}).Updates(map[string]any{column: false})
			if result.Error != nil {
				return fmt.Errorf("restore api_keys.%s: %w", column, result.Error)
			}
			if result.RowsAffected != int64(len(values)) {
				return fmt.Errorf("restore api_keys.%s: updated %d rows, want %d", column, result.RowsAffected, len(values))
			}
		}
	}
	return nil
}

func apiKeyIDsWithExplicitFalse(rows []map[string]any, column string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0)
	for _, row := range rows {
		value, ok := row[column].(bool)
		if !ok || value {
			continue
		}
		id, err := uuid.Parse(stringValue(row["id"]))
		if err != nil {
			return nil, fmt.Errorf("restore api_keys.%s: invalid api key id: %w", column, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func applyRowToModel(ctx context.Context, parsed *schema.Schema, item reflect.Value, row map[string]any) error {
	for col, value := range row {
		field, ok := parsed.FieldsByDBName[col]
		if !ok {
			continue
		}
		if err := field.Set(ctx, item, modelFieldValue(value, field)); err != nil {
			return fmt.Errorf("set %s: %w", col, err)
		}
	}
	return nil
}

func modelFieldValue(value any, field *schema.Field) any {
	if field.FieldType == reflect.TypeOf(store.JSON{}) {
		return rowValue(value, true)
	}
	if field.FieldType == reflect.TypeOf(sql.NullTime{}) || field.FieldType == reflect.TypeOf(time.Time{}) || field.FieldType == reflect.TypeOf((*time.Time)(nil)) {
		if parsed, ok := parseTimeValue(value); ok {
			return parsed
		}
	}
	return rowValue(value, false)
}

func parseTimeValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false
		}
		return v, true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return parsed, true
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsed, true
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

func validateDumpTables(dump databaseDump) error {
	if dump.Tables == nil {
		return fmt.Errorf("backup database payload is missing tables")
	}
	// Present 为 nil 时视为全部备份表(旧行为);否则只为 present 的表补空切片。
	presentSet := make(map[string]struct{}, len(dump.Present))
	for _, table := range dump.Present {
		presentSet[table] = struct{}{}
	}
	for _, table := range backupTables {
		if dump.Present != nil {
			if _, ok := presentSet[table.Name]; !ok {
				continue
			}
		}
		if _, streamed := streamedImportTables[table.Name]; streamed {
			// Streamed tables are validated against the archive in parseArchive.
			continue
		}
		if _, ok := dump.Tables[table.Name]; !ok {
			dump.Tables[table.Name] = []map[string]any{}
		}
	}
	return nil
}

// importPresentTables 返回要插入的表:Present 为 nil 时视为全部备份表(旧行为)。
func importPresentTables(dump databaseDump) []string {
	if dump.Present == nil {
		present := make([]string, 0, len(backupTables))
		for _, table := range backupTables {
			present = append(present, table.Name)
		}
		return present
	}
	return dump.Present
}

// importClearTables 返回导入前要清空的表:Clear 为 nil 时视为 importDeleteOrder
// 全部(旧行为);否则逐个校验必须是已知备份表。
func importClearTables(dump databaseDump) []string {
	if dump.Clear == nil {
		return importDeleteOrder
	}
	return dump.Clear
}

func normalizeImportDump(dump databaseDump) databaseDump {
	// 只归一化 present 的大表明细;Present 为 nil 时视为全部(旧行为)。
	presentSet := make(map[string]struct{}, len(dump.Present))
	for _, table := range dump.Present {
		presentSet[table] = struct{}{}
	}
	isPresent := func(table string) bool {
		if dump.Present == nil {
			return true
		}
		_, ok := presentSet[table]
		return ok
	}
	if isPresent("request_logs") {
		dump.Tables["request_logs"] = rowsWithValidNullableReferences(dump.Tables["request_logs"], map[string]map[string]struct{}{
			"api_key_id":         rowIDSet(dump.Tables["api_keys"]),
			"site_id":            rowIDSet(dump.Tables["sites"]),
			"canonical_model_id": rowIDSet(dump.Tables["canonical_models"]),
			"site_model_id":      rowIDSet(dump.Tables["site_models"]),
		})
	}
	if isPresent("usage_records") {
		dump.Tables["usage_records"] = rowsWithExistingParent(dump.Tables["usage_records"], "request_log_id", rowIDSet(dump.Tables["request_logs"]))
		dump.Tables["usage_records"] = rowsWithValidNullableReferences(dump.Tables["usage_records"], map[string]map[string]struct{}{
			"api_key_id":         rowIDSet(dump.Tables["api_keys"]),
			"site_id":            rowIDSet(dump.Tables["sites"]),
			"canonical_model_id": rowIDSet(dump.Tables["canonical_models"]),
		})
	}
	return dump
}

func rowsWithExistingParent(rows []map[string]any, column string, parentIDs map[string]struct{}) []map[string]any {
	if len(rows) == 0 {
		return rows
	}
	filtered := rows[:0]
	for _, row := range rows {
		id := stringValue(row[column])
		if id == "" {
			continue
		}
		if _, ok := parentIDs[id]; !ok {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func rowsWithValidNullableReferences(rows []map[string]any, parentsByColumn map[string]map[string]struct{}) []map[string]any {
	for _, row := range rows {
		for column, parentIDs := range parentsByColumn {
			id := stringValue(row[column])
			if id == "" {
				row[column] = nil
				continue
			}
			if _, ok := parentIDs[id]; !ok {
				row[column] = nil
			}
		}
	}
	return rows
}

func rowIDSet(rows []map[string]any) map[string]struct{} {
	ids := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		id := stringValue(row["id"])
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func stringValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case uuid.UUID:
		if v == uuid.Nil {
			return ""
		}
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func decryptTableSecrets(table string, rows []map[string]any, masterKey string) error {
	for _, column := range textSecretColumns[table] {
		plainColumn := plainSecretColumn(column)
		for _, row := range rows {
			encrypted, _ := row[column].(string)
			delete(row, column)
			if strings.TrimSpace(encrypted) == "" {
				row[plainColumn] = ""
				continue
			}
			plain, err := crypto.DecryptSecret(masterKey, encrypted)
			if err != nil {
				return fmt.Errorf("decrypt %s.%s: %w", table, column, err)
			}
			row[plainColumn] = plain
		}
	}
	return nil
}

func encryptTableSecrets(table string, rows []map[string]any, masterKey string) error {
	for _, column := range textSecretColumns[table] {
		plainColumn := plainSecretColumn(column)
		for _, row := range rows {
			plain, _ := row[plainColumn].(string)
			delete(row, plainColumn)
			if strings.TrimSpace(plain) == "" {
				row[column] = ""
			} else {
				encrypted, err := crypto.EncryptSecret(masterKey, plain)
				if err != nil {
					return fmt.Errorf("encrypt %s.%s: %w", table, column, err)
				}
				row[column] = encrypted
			}
			switch table {
			case "api_keys":
				if plain != "" {
					row["masked_key"] = crypto.MaskSecret(plain)
				}
			case "site_credentials":
				if plain != "" {
					row["masked_secret"] = crypto.MaskSecret(plain)
				}
			case "oauth_connections":
				maskedColumn := strings.Replace(column, "encrypted_", "masked_", 1)
				if plain != "" {
					row[maskedColumn] = crypto.MaskSecret(plain)
				}
			}
		}
	}
	return nil
}

func plainSecretColumn(column string) string {
	return strings.TrimPrefix(column, "encrypted_")
}

// encodeBinaryColumns 把二进制列编码为 base64 字符串:任意二进制无法经 JSON
// 字符串无损往返(非法 UTF-8 会被替换),base64 保证字节完全一致。zip 包不是
// 合法 JSON,decodeJSONValue 的回退分支返回原始字符串,这里统一编码。
func encodeBinaryColumns(table string, rows []map[string]any) {
	for _, column := range binaryColumns[table] {
		for _, row := range rows {
			switch value := row[column].(type) {
			case nil:
			case []byte:
				row[column] = base64.StdEncoding.EncodeToString(value)
			case string:
				row[column] = base64.StdEncoding.EncodeToString([]byte(value))
			}
		}
	}
}

// decodeBinaryColumns 把 base64 字符串解码回字节;非字符串值(已解码的字节)
// 原样保留,供直接构造 dump 的测试使用。
func decodeBinaryColumns(table string, rows []map[string]any) error {
	for _, column := range binaryColumns[table] {
		for _, row := range rows {
			encoded, ok := row[column].(string)
			if !ok {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return fmt.Errorf("decode %s.%s: %w", table, column, err)
			}
			row[column] = decoded
		}
	}
	return nil
}

func backupTableByName(name string) (backupTable, bool) {
	for _, table := range backupTables {
		if table.Name == name {
			return table, true
		}
	}
	return backupTable{}, false
}

func modelRows(ctx context.Context, parsed *schema.Schema, slice any) ([]map[string]any, error) {
	value := reflect.ValueOf(slice)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil, fmt.Errorf("backup slice must be a non-nil pointer")
	}
	items := value.Elem()
	if items.Kind() != reflect.Slice {
		return nil, fmt.Errorf("backup slice must point to a slice")
	}
	rows := make([]map[string]any, 0, items.Len())
	for i := 0; i < items.Len(); i++ {
		item := items.Index(i)
		if item.Kind() == reflect.Pointer {
			item = item.Elem()
		}
		row := make(map[string]any, len(parsed.Fields))
		for _, field := range parsed.Fields {
			if field.DBName == "" {
				continue
			}
			raw, _ := field.ValueOf(ctx, item)
			row[field.DBName] = backupValue(raw)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func stripCacheObservationRows(rows []map[string]any) {
	for _, row := range rows {
		value, ok := row["metadata"]
		if !ok {
			continue
		}
		var metadata map[string]any
		switch item := value.(type) {
		case json.RawMessage:
			if json.Unmarshal(item, &metadata) != nil {
				continue
			}
		case []byte:
			if json.Unmarshal(item, &metadata) != nil {
				continue
			}
		case map[string]any:
			metadata = item
		default:
			continue
		}
		if _, ok := metadata["cache_observation"]; !ok {
			continue
		}
		delete(metadata, "cache_observation")
		row["metadata"] = metadata
	}
}

func backupValue(value any) any {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case store.JSON:
		return json.RawMessage(v)
	case []byte:
		return decodeJSONValue(v)
	case uuid.UUID:
		if v == uuid.Nil {
			return nil
		}
		return v.String()
	case uuid.NullUUID:
		if !v.Valid || v.UUID == uuid.Nil {
			return nil
		}
		return v.UUID.String()
	case time.Time:
		if v.IsZero() {
			return nil
		}
		return v.Format(time.RFC3339Nano)
	case *time.Time:
		if v == nil || v.IsZero() {
			return nil
		}
		return v.Format(time.RFC3339Nano)
	case *uuid.UUID:
		if v == nil || *v == uuid.Nil {
			return nil
		}
		return v.String()
	case driver.Valuer:
		driverValue, err := v.Value()
		if err != nil {
			return nil
		}
		return backupValue(driverValue)
	default:
		return value
	}
}

func decodeJSONValue(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return string(raw)
	}
	return decoded
}

func primaryOrderClause(db *gorm.DB, model any) (clause.OrderBy, error) {
	parsed, err := schema.Parse(model, &sync.Map{}, db.NamingStrategy)
	if err != nil {
		return clause.OrderBy{}, err
	}
	columns := make([]clause.OrderByColumn, 0, len(parsed.PrimaryFields))
	for _, field := range parsed.PrimaryFields {
		if field.DBName == "" {
			continue
		}
		columns = append(columns, clause.OrderByColumn{Column: clause.Column{Name: field.DBName}})
	}
	return clause.OrderBy{Columns: columns}, nil
}

func rowValue(value any, asJSON bool) any {
	if asJSON {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "null"
		}
		return string(encoded)
	}
	switch v := value.(type) {
	case map[string]any, []any:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	default:
		return v
	}
}

func cloneRows(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := make(map[string]any, len(row))
		for k, v := range row {
			next[k] = v
		}
		out = append(out, next)
	}
	return out
}
