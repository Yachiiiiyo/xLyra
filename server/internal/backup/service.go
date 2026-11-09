package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"xlyra/server/internal/config"
	"xlyra/server/internal/store"
)

const (
	currentFormatVersion         = 4
	minImportFormatVersion       = 2
	backupAppName                = "xlyra"
	MaxImportBytes         int64 = 4 << 30
)

var ErrOperationInProgress = errors.New("another backup or restore operation is in progress")

var operationGuards sync.Map

func operationGuardFor(db *store.Store) *atomic.Bool {
	guard, _ := operationGuards.LoadOrStore(db, &atomic.Bool{})
	return guard.(*atomic.Bool)
}

func beginSharedOperation(db *store.Store) bool {
	if db == nil {
		return true
	}
	return operationGuardFor(db).CompareAndSwap(false, true)
}

func endSharedOperation(db *store.Store) {
	if db == nil {
		return
	}
	operationGuardFor(db).Store(false)
}

type Service struct {
	db                 *store.Store
	confFile           *config.ConfigFile
	masterKey          string
	agentRoot          string
	agentWorkspaceRoot string
	preRestore         func(context.Context) error
	postRestore        func(context.Context) error
	databaseRestored   func(context.Context) error
	now                func() time.Time
	timeZone           config.TimeZone
}

type ImportSummary struct {
	Tables              int             `json:"tables"`
	Sections            []string        `json:"sections"`
	ClearedSections     []SectionImpact `json:"cleared_sections"`
	Rows                int             `json:"rows"`
	ConfigKeys          int             `json:"config_keys"`
	FormatVersion       int             `json:"format_version"`
	AgentFiles          int             `json:"agent_files"`
	AgentSessions       int             `json:"agent_sessions"`
	AgentWorkspaceFiles int             `json:"agent_workspace_files"`
}

type ImportOptions struct {
	AdminID uuid.UUID
}

type ProgressEvent struct {
	Step      string         `json:"step"`
	Status    string         `json:"status"`
	Rows      int            `json:"rows,omitempty"`
	TotalRows int            `json:"total_rows,omitempty"`
	Table     string         `json:"table,omitempty"`
	Bytes     int64          `json:"bytes,omitempty"`
	Total     int64          `json:"total_bytes,omitempty"`
	Summary   *ImportSummary `json:"summary,omitempty"`
	Message   string         `json:"message,omitempty"`
}

type ProgressFunc func(ProgressEvent)

func NewService(db *store.Store, confFile *config.ConfigFile, masterKey string, timeZones ...config.TimeZone) Service {
	return Service{
		db:        db,
		confFile:  confFile,
		masterKey: masterKey,
		now:       func() time.Time { return time.Now().UTC() },
		timeZone:  backupTimeZone(timeZones...),
	}
}

func (s Service) WithAgentRoot(root string) Service {
	s.agentRoot = root
	return s
}

// WithAgentWorkspaceRoot 设置 Agent 共享工作区目录,供 agent_workspace
// 类别的导出/恢复使用;返回修改后的 Service 拷贝。
func (s Service) WithAgentWorkspaceRoot(dir string) Service {
	s.agentWorkspaceRoot = dir
	return s
}

func (s Service) WithRestoreHooks(pre func(context.Context) error, post func(context.Context) error) Service {
	s.preRestore = pre
	s.postRestore = post
	return s
}

func (s Service) WithDatabaseRestored(fn func(context.Context) error) Service {
	s.databaseRestored = fn
	return s
}

func (s Service) finishRestore(ctx context.Context) error {
	var hookErr error
	if s.postRestore != nil {
		hookErr = s.postRestore(ctx)
	}
	if s.databaseRestored != nil {
		_ = s.databaseRestored(ctx)
	}
	if hookErr != nil {
		return fmt.Errorf("finish restore: %w", hookErr)
	}
	return nil
}

// defaultExportSections 返回手动导出/自动备份的默认类别:数据库与配置的全部
// 已实现类别,加上 agentRoot 非空时的 agent_capabilities。不使用
// DefaultSections(),它还包含尚未实现的 agent_sessions。
func (s Service) defaultExportSections() []string {
	ids := []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionPlugins,
		sectionAgentConfig,
	}
	if strings.TrimSpace(s.agentRoot) != "" {
		ids = append(ids, sectionAgentCapabilities)
	}
	return ids
}

func (s Service) Export(ctx context.Context, passphrase string) (string, string, error) {
	return s.ExportSections(ctx, passphrase, s.defaultExportSections())
}

// ExportSections 按所选类别导出备份;返回值与 Export 一致。
func (s Service) ExportSections(ctx context.Context, passphrase string, ids []string) (string, string, error) {
	if err := ValidateSelection(ids); err != nil {
		return "", "", err
	}
	for _, id := range ids {
		switch id {
		case sectionAgentCapabilities, sectionAgentSessions:
			if strings.TrimSpace(s.agentRoot) == "" {
				return "", "", fmt.Errorf("backup section %q requires agent data directory", id)
			}
		case sectionAgentWorkspace:
			if strings.TrimSpace(s.agentWorkspaceRoot) == "" {
				return "", "", fmt.Errorf("backup section %q requires agent workspace directory", id)
			}
		}
	}
	return s.exportSectionsAt(ctx, passphrase, sortedSectionIDs(ids), s.now())
}

// sortedSectionIDs 返回按注册表顺序排列的类别拷贝;未知 id 已由
// ValidateSelection 排除。
func sortedSectionIDs(ids []string) []string {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, section := range sections {
		if _, ok := wanted[section.ID]; ok {
			out = append(out, section.ID)
		}
	}
	return out
}

func (s Service) exportSectionsAt(ctx context.Context, passphrase string, sectionIDs []string, createdAt time.Time) (string, string, error) {
	if err := s.ready(); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(passphrase) == "" {
		return "", "", fmt.Errorf("backup passphrase is required")
	}

	encryptedFile, err := os.CreateTemp("", "xlyra-backup-*.xlyra")
	if err != nil {
		return "", "", fmt.Errorf("create encrypted backup: %w", err)
	}
	encryptedPath := encryptedFile.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(encryptedPath)
		}
	}()

	plainReader, plainWriter := io.Pipe()
	payload := archivePayload{
		Manifest: manifest{
			FormatVersion: currentFormatVersion,
			App:           backupAppName,
			Payload:       backupPayload,
			CreatedAt:     createdAt,
			Tables:        TablesOf(sectionIDs),
			Sections:      sectionIDs,
		},
		Config: filterConfigForSections(s.confFile.Data(), sectionIDs),
	}
	writeErrs := make(chan error, 1)
	go func() {
		writeErr := writeArchive(payload, func(zw *zip.Writer) (map[string]int, error) {
			rowCounts, err := exportDatabase(ctx, s.db.DB(), s.masterKey, TablesOf(sectionIDs), zw)
			if err != nil {
				return nil, err
			}
			if slices.Contains(sectionIDs, sectionAgentCapabilities) {
				if _, err := exportAgentCapabilities(zw, s.agentRoot); err != nil {
					return nil, fmt.Errorf("export agent capabilities: %w", err)
				}
			}
			if slices.Contains(sectionIDs, sectionAgentSessions) {
				if _, err := exportAgentSessions(zw, s.agentRoot); err != nil {
					return nil, fmt.Errorf("export agent sessions: %w", err)
				}
			}
			if slices.Contains(sectionIDs, sectionAgentWorkspace) {
				if _, err := exportAgentWorkspace(zw, s.agentWorkspaceRoot); err != nil {
					return nil, fmt.Errorf("export agent workspace: %w", err)
				}
			}
			return rowCounts, nil
		}, plainWriter)
		if writeErr != nil {
			_ = plainWriter.CloseWithError(writeErr)
			writeErrs <- writeErr
			return
		}
		if err := plainWriter.Close(); err != nil {
			writeErrs <- fmt.Errorf("close backup archive stream: %w", err)
			return
		}
		writeErrs <- nil
	}()

	encryptErr := encryptStream(passphrase, plainReader, encryptedFile)
	if encryptErr != nil {
		_ = plainReader.CloseWithError(encryptErr)
	}
	writeErr := <-writeErrs
	closeEncryptedErr := encryptedFile.Close()
	if writeErr != nil {
		return "", "", writeErr
	}
	if encryptErr != nil {
		return "", "", encryptErr
	}
	if closeEncryptedErr != nil {
		return "", "", fmt.Errorf("close encrypted backup: %w", closeEncryptedErr)
	}
	stat, err := os.Stat(encryptedPath)
	if err != nil {
		return "", "", fmt.Errorf("stat encrypted backup: %w", err)
	}
	if stat.Size() > MaxImportBytes {
		return "", "", fmt.Errorf("backup size %d exceeds maximum restorable size %d", stat.Size(), MaxImportBytes)
	}

	success = true
	return encryptedPath, s.backupFilename(createdAt), nil
}

func (s Service) backupFilename(createdAt time.Time) string {
	return fmt.Sprintf("xlyra-backup-%s.zip.xlyra", s.timeZone.Format(createdAt, "20060102-150405"))
}

func backupTimeZone(timeZones ...config.TimeZone) config.TimeZone {
	return config.TimeZoneOrDefault(timeZones...)
}

func (s Service) Import(ctx context.Context, passphrase string, encrypted []byte, opts ImportOptions, progress ...ProgressFunc) (ImportSummary, error) {
	if len(encrypted) == 0 {
		return ImportSummary{}, fmt.Errorf("backup file is required")
	}
	return s.ImportReader(ctx, passphrase, bytes.NewReader(encrypted), opts, progress...)
}

func (s Service) ImportReader(ctx context.Context, passphrase string, encrypted io.Reader, opts ImportOptions, progress ...ProgressFunc) (ImportSummary, error) {
	return s.importReader(ctx, passphrase, encrypted, opts, nil, progress...)
}

func (s Service) importReader(ctx context.Context, passphrase string, encrypted io.Reader, opts ImportOptions, beforeCommit func() error, progress ...ProgressFunc) (ImportSummary, error) {
	if !beginSharedOperation(s.db) {
		return ImportSummary{}, ErrOperationInProgress
	}
	defer endSharedOperation(s.db)
	return s.importReaderLocked(ctx, passphrase, encrypted, opts, beforeCommit, progress...)
}

func (s Service) importReaderLocked(ctx context.Context, passphrase string, encrypted io.Reader, opts ImportOptions, beforeCommit func() error, progress ...ProgressFunc) (ImportSummary, error) {
	if err := s.ready(); err != nil {
		return ImportSummary{}, err
	}
	if strings.TrimSpace(passphrase) == "" {
		return ImportSummary{}, fmt.Errorf("backup passphrase is required")
	}
	if encrypted == nil {
		return ImportSummary{}, fmt.Errorf("backup file is required")
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
	emit(ProgressEvent{Step: "decrypt", Status: "in_progress", Message: "Decrypting backup archive"})

	archiveFile, err := os.CreateTemp("", "xlyra-backup-import-*.zip")
	if err != nil {
		return ImportSummary{}, fmt.Errorf("create backup archive: %w", err)
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)

	err = decryptStream(passphrase, &backupContextReader{ctx: ctx, reader: encrypted}, archiveFile)
	closeErr := archiveFile.Close()
	if err != nil {
		return ImportSummary{}, err
	}
	if closeErr != nil {
		return ImportSummary{}, fmt.Errorf("close backup archive: %w", closeErr)
	}

	emit(ProgressEvent{Step: "decrypt", Status: "complete"})
	emit(ProgressEvent{Step: "parse", Status: "in_progress", Message: "Parsing backup archive"})

	payload, dbDump, err := parseArchiveContext(ctx, archivePath)
	if err != nil {
		return ImportSummary{}, err
	}
	if err := validateManifest(payload.Manifest); err != nil {
		return ImportSummary{}, err
	}
	// 恢复按备份包实际包含的类别进行;会话与工作区依赖的目录必须在动
	// 数据库之前确认已配置,避免恢复到一半才发现。
	restored := effectiveSections(payload.Manifest)
	for _, id := range restored {
		switch id {
		case sectionAgentCapabilities:
			if strings.TrimSpace(s.agentRoot) == "" {
				return ImportSummary{}, fmt.Errorf("cannot restore backup section %q: agent data directory is not configured", id)
			}
		case sectionAgentSessions:
			if strings.TrimSpace(s.agentRoot) == "" {
				return ImportSummary{}, fmt.Errorf("cannot restore backup section %q: agent data directory is not configured", id)
			}
		case sectionAgentWorkspace:
			if strings.TrimSpace(s.agentWorkspaceRoot) == "" {
				return ImportSummary{}, fmt.Errorf("cannot restore backup section %q: agent workspace directory is not configured", id)
			}
		}
	}
	dbDump.beforeCommit = beforeCommit
	// Present 在解析时已设好;Clear 按类别计算(含外键级联与随行清空的表)。
	dbDump.Clear = ClearedTables(restored)
	preparedConfig, err := s.confFile.PrepareReplace(mergeConfigForRestore(s.confFile.Data(), payload.Config, restored))
	if err != nil {
		return ImportSummary{}, fmt.Errorf("prepare config replacement: %w", err)
	}
	defer preparedConfig.Discard()

	emit(ProgressEvent{Step: "parse", Status: "complete", TotalRows: dbDump.TotalRows})

	quiesced := false
	if s.preRestore != nil {
		if err := s.preRestore(ctx); err != nil {
			return ImportSummary{}, fmt.Errorf("prepare restore: %w", err)
		}
		quiesced = true
	}
	converge := func() {
		if quiesced {
			_ = s.finishRestore(ctx)
		}
	}

	// 只恢复配置等不含数据库表的备份时不发数据库相关的进度事件。
	databaseImported := len(dbDump.Present) > 0
	if databaseImported {
		emit(ProgressEvent{Step: "import", Status: "in_progress", TotalRows: dbDump.TotalRows, Message: "Writing backup data to database"})
	}

	importedRows, totalRows, err := importDatabase(ctx, s.db.DB(), s.masterKey, dbDump, opts.AdminID, prog)
	if err != nil {
		converge()
		return ImportSummary{}, err
	}
	// databaseRestored 由收尾的 finishRestore 统一触发,这里不重复调用。

	if databaseImported {
		emit(ProgressEvent{Step: "import", Status: "complete", Rows: importedRows, TotalRows: totalRows})
	}

	agentFiles := 0
	if slices.Contains(restored, sectionAgentCapabilities) {
		emit(ProgressEvent{Step: "agent", Status: "in_progress", Message: "Restoring agent capabilities"})
		lastAgentProgress := int64(0)
		var restoredAgentBytes int64
		agentFiles, restoredAgentBytes, err = restoreAgentCapabilities(archivePath, s.agentRoot, func(restored int64) {
			if restored-lastAgentProgress >= 4<<20 {
				lastAgentProgress = restored
				emit(ProgressEvent{Step: "agent", Status: "in_progress", Bytes: restored})
			}
		})
		if err != nil {
			converge()
			return ImportSummary{}, err
		}
		emit(ProgressEvent{Step: "agent", Status: "complete", Bytes: restoredAgentBytes})
	}

	agentSessions := 0
	if slices.Contains(restored, sectionAgentSessions) {
		emit(ProgressEvent{Step: "agent_sessions", Status: "in_progress", Message: "Restoring agent sessions"})
		lastSessionsProgress := int64(0)
		var restoredSessionBytes int64
		agentSessions, _, restoredSessionBytes, err = restoreAgentSessions(archivePath, s.agentRoot, s.agentWorkspaceRoot, func(restoredBytes int64) {
			if restoredBytes-lastSessionsProgress >= 4<<20 {
				lastSessionsProgress = restoredBytes
				emit(ProgressEvent{Step: "agent_sessions", Status: "in_progress", Bytes: restoredBytes})
			}
		})
		if err != nil {
			converge()
			return ImportSummary{}, err
		}
		emit(ProgressEvent{Step: "agent_sessions", Status: "complete", Bytes: restoredSessionBytes})
	}

	agentWorkspaceFiles := 0
	if slices.Contains(restored, sectionAgentWorkspace) {
		emit(ProgressEvent{Step: "agent_workspace", Status: "in_progress", Message: "Restoring agent workspace"})
		lastWorkspaceProgress := int64(0)
		var restoredWorkspaceBytes int64
		agentWorkspaceFiles, restoredWorkspaceBytes, err = restoreAgentWorkspace(archivePath, s.agentWorkspaceRoot, func(restoredBytes int64) {
			if restoredBytes-lastWorkspaceProgress >= 4<<20 {
				lastWorkspaceProgress = restoredBytes
				emit(ProgressEvent{Step: "agent_workspace", Status: "in_progress", Bytes: restoredBytes})
			}
		})
		if err != nil {
			converge()
			return ImportSummary{}, err
		}
		emit(ProgressEvent{Step: "agent_workspace", Status: "complete", Bytes: restoredWorkspaceBytes})
	}

	if err := s.finishRestore(ctx); err != nil {
		return ImportSummary{}, err
	}

	if err := preparedConfig.Commit(); err != nil {
		return ImportSummary{}, fmt.Errorf("replace config: %w", err)
	}

	clearedSections := RestoreImpact(restored)
	if clearedSections == nil {
		clearedSections = []SectionImpact{}
	}
	summary := ImportSummary{
		Tables:              len(dbDump.Present),
		Sections:            restored,
		ClearedSections:     clearedSections,
		Rows:                importedRows,
		ConfigKeys:          len(payload.Config),
		FormatVersion:       payload.Manifest.FormatVersion,
		AgentFiles:          agentFiles,
		AgentSessions:       agentSessions,
		AgentWorkspaceFiles: agentWorkspaceFiles,
	}
	emit(ProgressEvent{Step: "complete", Status: "complete", Rows: importedRows, TotalRows: totalRows, Summary: &summary})

	return summary, nil
}

type backupContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *backupContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func (s Service) ready() error {
	if s.db == nil || s.db.DB() == nil {
		return fmt.Errorf("database is not available")
	}
	if s.confFile == nil {
		return fmt.Errorf("config persistence is not available")
	}
	if strings.TrimSpace(s.masterKey) == "" {
		return fmt.Errorf("master key is not available")
	}
	return nil
}

func validateManifest(value manifest) error {
	if value.App != backupAppName {
		return fmt.Errorf("backup app mismatch")
	}
	if value.FormatVersion < minImportFormatVersion || value.FormatVersion > currentFormatVersion {
		return fmt.Errorf("unsupported backup format version %d", value.FormatVersion)
	}
	if value.Payload != backupPayload {
		return fmt.Errorf("unsupported backup payload %q", value.Payload)
	}
	switch value.FormatVersion {
	case 4:
		if err := ValidateSelection(value.Sections); err != nil {
			return err
		}
		want := TablesOf(value.Sections)
		if len(value.Tables) != len(want) {
			return fmt.Errorf("backup table list mismatch")
		}
		for i, table := range want {
			if value.Tables[i] != table {
				return fmt.Errorf("backup table list mismatch")
			}
		}
	case 3:
		if len(value.Tables) != len(legacyV3Tables) {
			return fmt.Errorf("backup table list mismatch")
		}
		for i, table := range legacyV3Tables {
			if value.Tables[i] != table {
				return fmt.Errorf("backup table list mismatch")
			}
		}
		for _, name := range value.Sections {
			if name != sectionAgentCapabilities {
				return fmt.Errorf("unsupported backup section %q", name)
			}
		}
	default:
		// v2:表清单只要求是已知备份表或已废弃的 playground 表;
		// sections 必须为空。
		for _, table := range value.Tables {
			if _, ok := backupTableByName(table); ok {
				continue
			}
			if !slices.Contains(legacyPlaygroundTables, table) {
				return fmt.Errorf("backup table list mismatch")
			}
		}
		if len(value.Sections) > 0 {
			return fmt.Errorf("unsupported backup section %q", value.Sections[0])
		}
	}
	return nil
}

// legacyPlaygroundTables 是 playground 功能废弃前备份包含的四张表;旧备份
// 里出现时直接忽略,不报错。
var legacyPlaygroundTables = []string{
	"playground_conversations",
	"playground_runs",
	"playground_turn_indexes",
	"playground_assets",
}

// legacyV3Tables 是 v3 时代备份 manifest 的完整表清单(顺序同旧 exportTables)。
var legacyV3Tables = []string{
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
	"playground_conversations",
	"playground_runs",
	"playground_turn_indexes",
	"playground_assets",
}

// effectiveSections 返回备份实际包含的类别:v4 直接使用 manifest 的 sections;
// v2/v3 视为「旧全量」(settings、gateway 全部、agent_config),再并入
// sections 里列出的 agent_capabilities(若有)。
func effectiveSections(m manifest) []string {
	if m.FormatVersion >= 4 {
		return m.Sections
	}
	out := []string{
		sectionSettings,
		sectionGatewaySites,
		sectionGatewayKeys,
		sectionGatewayLogs,
		sectionAgentConfig,
	}
	if slices.Contains(m.Sections, sectionAgentCapabilities) {
		out = append(out, sectionAgentCapabilities)
	}
	return out
}
