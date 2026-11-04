// fork.go 实现会话分叉:把源会话(整个,或到某条用户消息所在轮次结束为止)
// 复制成一个全新的会话。复制的是 Pi 会话文件的当前分支与对应的 meta 绑定;
// workspace 不复制(v1 只复制对话历史,避免跨会话身份的属主/权限问题)。
package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Fork 把源会话复制成一个新会话并返回新会话的 meta。
//   - messageID 为空:复制当前分支的全部条目;
//   - messageID 非空:复制到该用户消息所在那一轮结束为止(含这条用户消息、
//     其后的 assistant/toolResult 等条目,不含下一条用户消息及其后);
//     消息不存在或其条目不在当前分支上时返回 ErrNotFound。
//
// 新会话沿用源的 Model/PermissionMode,标题为「分叉 · <源标题>」,Pi 会话 id
// 换成新 UUID 并按 Pi 的文件命名习惯写入新会话的 pi/ 目录;文件头复制源头,
// 只改 id、timestamp 与 cwd(指向新会话的 workspace)。meta 迁移复制 EntryID
// 落在被复制条目里的用户消息与锚点落在被复制条目里的审批记录和 RunChanges;
// SessionAllow、RunErrors、Slot、StateDir 不复制。任何一步失败都会删除已创建的
// 新会话目录,不留半成品。
//
// 并发:全程持源会话的会话锁且只读;新会话是全新 id,与其他会话互不影响。
// 源会话是否有进行中的 run 由调用方(handler)先行拦截,这里不做检查。
func (s *Store) Fork(sourceID, messageID string) (Meta, error) {
	if !validID(sourceID) {
		return Meta{}, ErrInvalidID
	}
	unlock := s.lockSession(sourceID)
	defer unlock()
	meta, err := readMeta(filepath.Join(s.root, sourceID, metaFile))
	if err != nil {
		return Meta{}, err
	}
	// messageID 先在 meta 里预检:不存在直接报错,此时还没有任何副作用。
	// 消息存在但还没绑定 Pi 条目时同样拒绝:没有可定位的一轮。
	anchorEntryID := ""
	if messageID != "" {
		found := false
		for i := range meta.Messages {
			if meta.Messages[i].MessageID == messageID {
				anchorEntryID = meta.Messages[i].EntryID
				found = true
				break
			}
		}
		if !found || anchorEntryID == "" {
			return Meta{}, fmt.Errorf("%w: 用户消息 %s 不在当前分支上", ErrNotFound, messageID)
		}
	}
	created, err := s.Create(meta.Model, meta.PermissionMode)
	if err != nil {
		return Meta{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(s.Dir(created.ID))
		}
	}()
	// 从这里起任何失败都触发回滚;源文件的读取与选择放在创建之后,
	// 失败面覆盖读取/选择/写入各步。
	content, selected, piSessionID, err := s.forkFileContent(meta, anchorEntryID, s.WorkDir(created.ID))
	if err != nil {
		return Meta{}, err
	}
	if len(content) > 0 {
		name := piFileName(time.Now(), piSessionID)
		if err := writeFileSync(filepath.Join(s.PiDir(created.ID), name), content); err != nil {
			return Meta{}, err
		}
	}
	updated, err := s.Update(created.ID, func(m *Meta) error {
		m.Title = "分叉 · " + meta.DisplayTitle()
		m.PiSessionID = piSessionID
		for _, message := range meta.Messages {
			if message.EntryID != "" && selected[message.EntryID] {
				m.Messages = append(m.Messages, message)
			}
		}
		for _, esc := range meta.Escalations {
			if esc.AfterEntryID != "" && selected[esc.AfterEntryID] {
				m.Escalations = append(m.Escalations, esc)
			}
		}
		for _, rc := range meta.RunChanges {
			if rc.AfterEntryID != "" && selected[rc.AfterEntryID] {
				m.RunChanges = append(m.RunChanges, rc)
			}
		}
		return nil
	})
	if err != nil {
		return Meta{}, err
	}
	done = true
	return updated, nil
}

// forkFileContent 读取源 Pi 会话文件,选出一轮(或全部)当前分支条目,拼出
// 新会话文件的全部字节(改写过的文件头 + 按分支顺序排列的原始条目行),
// 并返回被复制条目的 id 集合与新的 Pi 会话 id。源尚未跑过 Pi(没有会话
// 文件)时返回空内容与空 id 集合,不视为错误。
func (s *Store) forkFileContent(meta Meta, anchorEntryID, newWorkDir string) ([]byte, map[string]bool, string, error) {
	file, err := PiSessionFile(s.PiDir(meta.ID), meta.PiSessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 源尚未跑过 Pi:没有可复制的条目,Pi 会话 id 留空,
			// 新会话首个 run 从空白会话开始。
			return nil, map[string]bool{}, "", nil
		}
		return nil, nil, "", err
	}
	piSessionID := uuid.NewString()
	header, rawLines, entries, err := readPiFileRaw(file)
	if err != nil {
		return nil, nil, "", err
	}
	path := activePath(entries)
	selectedPath, err := selectForkRange(path, anchorEntryID)
	if err != nil {
		return nil, nil, "", err
	}
	selected := make(map[string]bool, len(selectedPath))
	for _, entry := range selectedPath {
		if entry.ID != "" {
			selected[entry.ID] = true
		}
	}
	var buf bytes.Buffer
	buf.Write(forkHeaderLine(header, piSessionID, newWorkDir, time.Now()))
	if anchorEntryID == "" && !anyEntryID(path) {
		// v1 线性格式(条目没有 id):activePath 按文件顺序全取,这里同样
		// 按文件顺序复制除文件头外的全部原始行。
		for _, line := range rawLines {
			buf.Write(line)
			buf.WriteByte('\n')
		}
	} else {
		for _, entry := range selectedPath {
			if line, ok := rawLineByID(rawLines, entries, entry.ID); ok {
				buf.Write(line)
				buf.WriteByte('\n')
			}
		}
	}
	return buf.Bytes(), selected, piSessionID, nil
}

// selectForkRange 返回被复制的分支条目:anchorEntryID 为空取全部;否则从
// 分支开头取到该用户消息条目所在轮次结束(其后到下一条用户消息之前的
// assistant/toolResult/compaction 等条目都算这一轮)。
func selectForkRange(path []piEntry, anchorEntryID string) ([]piEntry, error) {
	if anchorEntryID == "" {
		return path, nil
	}
	start := -1
	for i, entry := range path {
		if entry.ID == anchorEntryID {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%w: 用户消息的条目 %s 不在当前分支上", ErrNotFound, anchorEntryID)
	}
	end := start
	for end+1 < len(path) && !isUserEntry(path[end+1]) {
		end++
	}
	return path[:end+1], nil
}

func isUserEntry(entry piEntry) bool {
	return entry.Type == "message" && entry.Message != nil && entry.Message.Role == "user"
}

// anyEntryID 报告分支里是否出现带 id 的条目(区分 v3 与 v1 线性格式)。
func anyEntryID(path []piEntry) bool {
	for _, entry := range path {
		if entry.ID != "" {
			return true
		}
	}
	return false
}

// rawLineByID 在文件顺序的原始行里定位指定 id 条目所在的行。
func rawLineByID(rawLines [][]byte, entries []piEntry, id string) ([]byte, bool) {
	if id == "" {
		return nil, false
	}
	for i, entry := range entries {
		if i < len(rawLines) && entry.ID == id {
			return rawLines[i], true
		}
	}
	return nil, false
}

// readPiFileRaw 逐行读取 Pi 会话文件:返回文件头原始行(没有则空)、除文件
// 头外的原始行(按文件顺序)与对应解析出的条目。与 readPiEntries 一致,
// 非法行跳过;最后一行没有换行符也能读到。
func readPiFileRaw(path string) ([]byte, [][]byte, []piEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	var header []byte
	var rawLines [][]byte
	var entries []piEntry
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var entry piEntry
		if json.Unmarshal(trimmed, &entry) != nil {
			continue
		}
		if entry.Type == "session" {
			header = trimmed
			continue
		}
		if entry.Type == "" {
			continue
		}
		rawLines = append(rawLines, trimmed)
		entries = append(entries, entry)
	}
	return header, rawLines, entries, nil
}

// forkHeaderLine 复制源文件头并改写 id、timestamp、cwd;其余字段(version、
// parentSession 等)原样保留。源文件缺文件头时生成最小头。
func forkHeaderLine(raw []byte, piSessionID, workDir string, now time.Time) []byte {
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &fields)
	}
	set := func(key string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			return
		}
		fields[key] = data
	}
	if _, ok := fields["type"]; !ok {
		set("type", "session")
	}
	if _, ok := fields["version"]; !ok {
		set("version", 3)
	}
	set("id", piSessionID)
	set("timestamp", now.UTC().Format("2006-01-02T15:04:05.000Z"))
	set("cwd", workDir)
	out, err := json.Marshal(fields)
	if err != nil {
		out = []byte(`{"type":"session","version":3}`)
	}
	return append(out, '\n')
}

// piFileName 按 Pi 的命名习惯生成会话文件名:
// <UTC 时间戳(ISO 里的 : 与 . 换成 -)>_<piSessionID>.jsonl。
func piFileName(now time.Time, piSessionID string) string {
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(now.UTC().Format("2006-01-02T15:04:05.000Z"))
	return stamp + "_" + piSessionID + ".jsonl"
}
