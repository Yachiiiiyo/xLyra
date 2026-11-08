// Package session 管理 xLyra 的 Agent 会话存储:每个会话一个 UUID 目录,
// 内含 meta.json(xLyra 元数据:标题、模型、审批记录、用户消息与 Pi 条目的
// 绑定)、pi/(Pi 的 --session-dir)与 workspace/(默认的会话工作目录;也可
// 配置为所有会话共用的全局目录),并负责把 Pi 会话文件(JSONL,树结构)
// 组装成前端 /agent 页消费的 transcript。本包只做文件存取,不启动 Pi 进程。
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	metaFile    = "meta.json"
	piDirName   = "pi"
	workDirName = "workspace"
	dirPerm     = 0o750
	filePerm    = 0o600
)

// ErrNotFound 表示会话(或其 meta.json)不存在。
var ErrNotFound = errors.New("session: 会话不存在")

// ErrInvalidID 表示会话 id 不是规范形式的 UUID(防路径穿越)。
var ErrInvalidID = errors.New("session: 非法会话 id")

// Meta 是 xLyra 侧的会话元数据;Pi 侧上下文在 pi/ 目录的 JSONL 里。
type Meta struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Model          string        `json:"model"`
	PermissionMode string        `json:"permission_mode"`
	Slot           int           `json:"slot,omitempty"` // 隔离身份槽位(1..MaxSlots-2);0 = 未分配(旧会话/关闭隔离)
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	PiSessionID    string        `json:"pi_session_id"` // 当前 Pi 会话 id(fork 后会变),空表示尚未创建
	Messages       []UserMessage `json:"messages"`      // 用户消息:message_id ↔ Pi 条目 id 绑定
	Escalations    []Escalation  `json:"escalations"`   // 审批记录(Pi 不记决定,xLyra 自己存)
	SessionAllow   []string      `json:"session_allow"` // 会话内允许的工具名(write/edit/bash),空表示逐条审批
	RunErrors      []RunError    `json:"run_errors"`    // run 失败记录,刷新后由 transcript 回放
	RunChanges     []RunChange   `json:"run_changes"`   // 每轮文件变更摘要
	Questions      []Question    `json:"questions"`     // question 工具的提问记录(含未回答)
}

// UserMessage 记录一条用户消息及其绑定的 Pi 条目。
type UserMessage struct {
	MessageID   string       `json:"message_id"`
	EntryID     string       `json:"entry_id"`
	Text        string       `json:"text"`
	CreatedAt   time.Time    `json:"created_at"`
	Attachments []Attachment `json:"attachments,omitempty"`
	// References 记录本条消息发送时引用的其他会话(只读上下文);
	// 发给 Pi 的提示里拼了引用块,但 Text 仍只存用户原文。
	References []Reference `json:"references,omitempty"`
}

// Reference 描述一条用户消息引用的会话。
type Reference struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

// Attachment 只记名字与类型;图片本体已在 Pi 会话文件里,不重复存 data_url。
type Attachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
}

// Escalation 是一次权限审批;Granted 为 nil 表示未决。Scope 为 "session"
// 表示这条审批以「会话内允许」作出(同时把工具写进 SessionAllow);
// Auto 为 true 表示由会话内允许或 auto 分类器自动决定,未经用户逐条确认。
// ClassifierReason 记录分类器的判定理由;Reviewing 表示 auto 分类尚未完成,
// 此时审批对用户不可见,也不计入等待用户。
type Escalation struct {
	EscalationID     string    `json:"escalation_id"`
	ToolName         string    `json:"tool_name"`
	RequestedPath    string    `json:"requested_path"`
	ResolvedPath     string    `json:"resolved_path"`
	RequestedCommand []string  `json:"requested_command,omitempty"`
	Granted          *bool     `json:"granted"`
	At               time.Time `json:"at"`
	AfterEntryID     string    `json:"after_entry_id"` // 审批发生时的锚点:最后已见的 Pi 条目 id,事件缺失时取 Pi 会话文件末尾条目
	Scope            string    `json:"scope,omitempty"`
	Auto             bool      `json:"auto,omitempty"`
	ClassifierReason string    `json:"classifier_reason,omitempty"`
	Reviewing        bool      `json:"reviewing,omitempty"`
}

// MaxRunErrors 是 meta 里保留的 run 失败记录条数上限,超出丢弃最旧的。
const MaxRunErrors = 20

// RunError 记录一次以 agent_error 收尾的 run:Pi 会话文件里没有失败信息,
// 前端刷新后靠 transcript 里的 run_error 条目回放。AfterEntryID 与审批的
// 锚点同义(优先 run 事件循环里最后见到的 Pi 条目 id,为空时取 Pi 会话
// 文件末尾条目,没有则空)。
type RunError struct {
	RunID        string    `json:"run_id"`
	Message      string    `json:"message"`
	AfterEntryID string    `json:"after_entry_id"`
	At           time.Time `json:"at"`
}

// FileChange 是一轮里单个文件的变更摘要。
type FileChange struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	HasDiff   bool   `json:"has_diff"`
}

// RunChange 是一轮(一条用户消息)的文件变更。
type RunChange struct {
	TurnID       string       `json:"turn_id"`
	RunIDs       []string     `json:"run_ids"`
	AfterEntryID string       `json:"after_entry_id"`
	Incomplete   bool         `json:"incomplete,omitempty"`
	Total        int          `json:"total"`
	Files        []FileChange `json:"files"`
	At           time.Time    `json:"at"`
}

const MaxRunChanges = 50
const MaxRunChangeFiles = 200

// UpsertRunChange 替换同轮记录并合并 run id;只保留最近的轮次。
func (m *Meta) UpsertRunChange(rc RunChange) {
	for i := range m.RunChanges {
		if m.RunChanges[i].TurnID != rc.TurnID {
			continue
		}
		seen := map[string]bool{}
		for _, id := range m.RunChanges[i].RunIDs {
			seen[id] = true
		}
		for _, id := range rc.RunIDs {
			if !seen[id] {
				m.RunChanges[i].RunIDs = append(m.RunChanges[i].RunIDs, id)
				seen[id] = true
			}
		}
		rc.RunIDs = m.RunChanges[i].RunIDs
		m.RunChanges[i] = rc
		return
	}
	m.RunChanges = append(m.RunChanges, rc)
	if len(m.RunChanges) > MaxRunChanges {
		m.RunChanges = m.RunChanges[len(m.RunChanges)-MaxRunChanges:]
	}
}

func (m *Meta) RemoveRunChange(turnID string) {
	for i := range m.RunChanges {
		if m.RunChanges[i].TurnID == turnID {
			m.RunChanges = append(m.RunChanges[:i], m.RunChanges[i+1:]...)
			return
		}
	}
}

// AppendRunError 追加一条 run 失败记录,只保留最近 MaxRunErrors 条。
func (m *Meta) AppendRunError(entry RunError) {
	m.RunErrors = append(m.RunErrors, entry)
	if len(m.RunErrors) > MaxRunErrors {
		m.RunErrors = m.RunErrors[len(m.RunErrors)-MaxRunErrors:]
	}
}

// Store 按根目录管理全部会话。meta.json 读写按会话加互斥(不同会话互不阻塞),
// 写入走临时文件 + rename 的原子替换;AssignSlot 额外持 Store 级互斥,
// 保证并发分配的槽位两两不同。
type Store struct {
	root      string
	workspace string

	mu     sync.Mutex
	locks  map[string]*sessionLock
	slotMu sync.Mutex
}

// sessionLock 是单个会话的互斥锁;refs 引用计数用于用完后从 map 回收,
// 避免 Store 长期运行下锁表随历史会话无限增长。
type sessionLock struct {
	mu   sync.Mutex
	refs int
}

// NewStore 建立会话存储;root 不存在时在首次写入时创建。
func NewStore(root string) *Store {
	return &Store{root: root, locks: map[string]*sessionLock{}}
}

// SetWorkspace 设置所有会话共用的工作目录;空串恢复按会话使用 workspace/。
func (s *Store) SetWorkspace(dir string) *Store {
	if dir == "" {
		s.workspace = ""
		return s
	}
	s.workspace = filepath.Clean(dir)
	return s
}

// lockSession 获取会话级互斥,返回解锁函数(同时递减引用计数)。
func (s *Store) lockSession(id string) func() {
	s.mu.Lock()
	entry := s.locks[id]
	if entry == nil {
		entry = &sessionLock{}
		s.locks[id] = entry
	}
	entry.refs++
	s.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, id)
		}
		s.mu.Unlock()
	}
}

// validID 要求会话 id 是规范化 UUID(小写连字符形式):uuid.Parse 也接受
// urn:uuid: 前缀、大括号等变体,统一以 Parse 后 String() 相等为准,
// 杜绝任何非规范形式进入文件路径。
func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// Create 新建会话:生成 UUID 目录、pi/、工作目录和初始 meta.json。
func (s *Store) Create(model, permissionMode string) (Meta, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	meta := Meta{
		ID:             id,
		Model:          model,
		PermissionMode: permissionMode,
		CreatedAt:      now,
		UpdatedAt:      now,
		Messages:       []UserMessage{},
		Escalations:    []Escalation{},
		SessionAllow:   []string{},
		RunErrors:      []RunError{},
		RunChanges:     []RunChange{},
	}
	if err := s.EnsureDirs(id); err != nil {
		return Meta{}, err
	}
	if err := s.writeMeta(id, meta); err != nil {
		// 半成品目录不留:writeMeta 失败时目录已建但 meta.json 未落盘。
		_ = os.RemoveAll(s.Dir(id))
		return Meta{}, err
	}
	return meta, nil
}

// Get 读取会话元数据;不存在返回 ErrNotFound。
func (s *Store) Get(id string) (Meta, error) {
	if !validID(id) {
		return Meta{}, ErrInvalidID
	}
	return readMeta(filepath.Join(s.root, id, metaFile))
}

// Update 读-改-写会话元数据:同一会话串行(互斥)、整体原子替换;fn 返回
// 错误则放弃修改。成功写入后自动刷新 UpdatedAt(List 按其倒序)。
func (s *Store) Update(id string, fn func(*Meta) error) (Meta, error) {
	if !validID(id) {
		return Meta{}, ErrInvalidID
	}
	unlock := s.lockSession(id)
	defer unlock()
	meta, err := readMeta(filepath.Join(s.root, id, metaFile))
	if err != nil {
		return Meta{}, err
	}
	if err := fn(&meta); err != nil {
		return Meta{}, err
	}
	meta.UpdatedAt = time.Now().UTC()
	if err := s.writeMeta(id, meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// AssignSlot 为会话分配隔离槽位并写回 meta,幂等:已有槽位(非 0)直接返回,
// 不再调用 pick。整个过程持 Store 级互斥(slotMu),先扫描全部会话 meta 的
// Slot 汇总 used 再交给 pick 选位,并发分配不会得到相同槽位;扫描时跳过坏
// meta。pick 返回错误时该会话 meta 保持不变。
func (s *Store) AssignSlot(id string, pick func(used map[int]bool) (int, error)) (int, error) {
	if !validID(id) {
		return 0, ErrInvalidID
	}
	if pick == nil {
		return 0, errors.New("session: pick 不能为空")
	}
	s.slotMu.Lock()
	defer s.slotMu.Unlock()
	// 会话锁与写 meta 的路径(Update)一致,避免与同会话的读-改-写交错;
	// slotMu 先于会话锁获取,其余路径不会反向加锁,无死锁风险。
	unlock := s.lockSession(id)
	defer unlock()
	meta, err := readMeta(filepath.Join(s.root, id, metaFile))
	if err != nil {
		return 0, err
	}
	if meta.Slot != 0 {
		return meta.Slot, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, err
	}
	used := make(map[int]bool)
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		other, err := readMeta(filepath.Join(s.root, entry.Name(), metaFile))
		if err != nil {
			continue // 坏 meta 跳过,不阻断分配
		}
		if other.Slot != 0 {
			used[other.Slot] = true
		}
	}
	slot, err := pick(used)
	if err != nil {
		return 0, err
	}
	meta.Slot = slot
	if err := s.writeMeta(id, meta); err != nil {
		return 0, err
	}
	return slot, nil
}

// Delete 删除整个会话目录,幂等(不存在也返回 nil)。
// 会话的隔离槽位随之自然回收:槽位占用只来自现存会话的 meta。
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return ErrInvalidID
	}
	unlock := s.lockSession(id)
	defer unlock()
	return os.RemoveAll(s.Dir(id))
}

// List 返回全部会话元数据,按 UpdatedAt 倒序(并列按 id);
// 坏目录/坏 meta.json 跳过不报错。
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Meta{}, nil
		}
		return nil, err
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		meta, err := readMeta(filepath.Join(s.root, entry.Name(), metaFile))
		if err != nil {
			continue
		}
		metas = append(metas, meta)
	}
	sort.SliceStable(metas, func(i, j int) bool {
		if !metas[i].UpdatedAt.Equal(metas[j].UpdatedAt) {
			return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
		}
		return metas[i].ID < metas[j].ID
	})
	return metas, nil
}

// ListItem 是会话列表项,对应前端 AgentSession。
type ListItem struct {
	SessionID    string
	Title        string
	Preview      string
	UpdatedAt    int64 // unix 毫秒
	Running      bool
	SessionAllow []string // 恒为非 nil,JSON 序列化为 [] 而非 null
}

// ListItems 组装会话列表:Title 取 meta.Title,为空时从第一条用户消息截取;
// Preview 读 Pi 会话文件尾部(最后一条 assistant/用户文本);running 由
// 调用方(runlog.Registry)传入判断函数。
func (s *Store) ListItems(running func(id string) bool) ([]ListItem, error) {
	return s.listItemsCached(running)
}

// firstUserText 返回第一条非空的用户消息文本。
func firstUserText(meta Meta) string {
	for _, msg := range meta.Messages {
		if strings.TrimSpace(msg.Text) != "" {
			return msg.Text
		}
	}
	return ""
}

// Preview 返回会话预览:Pi 会话文件里最后一条 assistant 文本或用户文本的
// 前 80 个 rune;读不到(尚未跑过 Pi / 尾部无可用文本)返回空串。
func (s *Store) Preview(meta Meta) string {
	if !validID(meta.ID) {
		return ""
	}
	file, err := PiSessionFile(s.PiDir(meta.ID), meta.PiSessionID)
	if err != nil {
		return ""
	}
	lines, err := readTailLines(file, previewWindow)
	if err != nil {
		return ""
	}
	return previewFromLines(lines)
}

// Dir/PiDir 返回会话目录路径;id 非法时返回空串
// (带校验的入口是 Create/Get/Update 等方法,路径助手只做兜底)。
func (s *Store) Dir(id string) string {
	if !validID(id) {
		return ""
	}
	return filepath.Join(s.root, id)
}

func (s *Store) PiDir(id string) string {
	if !validID(id) {
		return ""
	}
	return filepath.Join(s.root, id, piDirName)
}

// DisplayTitle 返回会话的展示标题:meta.Title 为空时从第一条非空用户消息
// 截取(与列表页的回退规则一致)。
func (m Meta) DisplayTitle() string {
	if m.Title != "" {
		return m.Title
	}
	return TitleFrom(firstUserText(m))
}

// WorkDir 返回会话工作目录:设置共享 workspace 后所有会话都返回该目录,
// 否则返回会话自己的 workspace/;id 非法时返回空串。
func (s *Store) WorkDir(id string) string {
	if !validID(id) {
		return ""
	}
	if s.workspace != "" {
		return s.workspace
	}
	return filepath.Join(s.root, id, workDirName)
}

// StateDir 返回会话私有的 xLyra 状态目录,不主动创建。
func (s *Store) StateDir(id string) string {
	if !validID(id) {
		return ""
	}
	return filepath.Join(s.root, id, ".xlyra")
}

// EnsureDirs 创建会话目录、pi/ 与工作目录(权限 0o750),已存在则不动。
// 设置共享 workspace 后,工作目录就是这个全局目录,不再创建会话内 workspace/。
func (s *Store) EnsureDirs(id string) error {
	if !validID(id) {
		return ErrInvalidID
	}
	for _, dir := range []string{s.Dir(id), s.PiDir(id), s.WorkDir(id)} {
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return err
		}
	}
	return nil
}

// MigrateLegacyWorkspaces 把各会话非空的旧 workspace/ 移到共享目录的
// _legacy/<id> 下;空目录保持原样,已有目标不覆盖。单个会话失败不阻断其它
// 会话,错误汇总返回。未设置共享 workspace 或 root 不存在时不做处理。
func (s *Store) MigrateLegacyWorkspaces() (moved int, err error) {
	if s.workspace == "" {
		return 0, nil
	}
	entries, readErr := os.ReadDir(s.root)
	if errors.Is(readErr, os.ErrNotExist) {
		return 0, nil
	}
	if readErr != nil {
		return 0, fmt.Errorf("session: 读取会话根目录 %s: %w", s.root, readErr)
	}

	legacyRoot := filepath.Join(s.workspace, "_legacy")
	var errs []error
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !validID(id) {
			continue
		}
		oldDir := filepath.Join(s.root, id, workDirName)
		info, statErr := os.Stat(oldDir)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			errs = append(errs, fmt.Errorf("session: 检查会话 %s 的旧 workspace: %w", id, statErr))
			continue
		}
		if !info.IsDir() {
			continue
		}
		oldEntries, readErr := os.ReadDir(oldDir)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("session: 读取会话 %s 的旧 workspace: %w", id, readErr))
			continue
		}
		if len(oldEntries) == 0 {
			continue
		}
		if mkdirErr := os.MkdirAll(legacyRoot, dirPerm); mkdirErr != nil {
			errs = append(errs, fmt.Errorf("session: 创建旧 workspace 迁移目录 %s: %w", legacyRoot, mkdirErr))
			continue
		}
		target := filepath.Join(legacyRoot, id)
		if _, statErr := os.Lstat(target); statErr == nil {
			errs = append(errs, fmt.Errorf("session: 迁移会话 %s 的旧 workspace: 目标 %s 已存在: %w", id, target, os.ErrExist))
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("session: 检查旧 workspace 迁移目标 %s: %w", target, statErr))
			continue
		}
		if renameErr := os.Rename(oldDir, target); renameErr != nil {
			errs = append(errs, fmt.Errorf("session: 迁移会话 %s 的旧 workspace 到 %s: %w", id, target, renameErr))
			continue
		}
		moved++
	}
	return moved, errors.Join(errs...)
}

// TitleFrom 从第一条用户消息截取标题:折叠空白(含换行)、按 rune 截到
// 40 个字符,超出加 …;空消息用「新会话」。
func TitleFrom(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		return "新会话"
	}
	if utf8.RuneCountInString(collapsed) > titleMaxRunes {
		return string([]rune(collapsed)[:titleMaxRunes]) + "…"
	}
	return collapsed
}

const (
	titleMaxRunes   = 40
	previewMaxRunes = 80
)

// readMeta 读取单个 meta.json;文件缺失返回 ErrNotFound。
func readMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, ErrNotFound
	}
	if err != nil {
		return Meta{}, err
	}
	// 预置空切片,使旧 meta 缺少集合字段时反序列化仍得到 [] 而非 nil。
	meta := Meta{
		Messages: []UserMessage{}, Escalations: []Escalation{},
		SessionAllow: []string{}, RunErrors: []RunError{}, RunChanges: []RunChange{},
		Questions: []Question{},
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// writeMeta 原子写 meta.json:先写临时文件并落盘,再 rename 替换。
// 调用方(Update)已持有会话锁。
func (s *Store) writeMeta(id string, meta Meta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := s.Dir(id)
	tmp := filepath.Join(dir, metaFile+".tmp")
	if err := writeFileSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, metaFile)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeFileSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
