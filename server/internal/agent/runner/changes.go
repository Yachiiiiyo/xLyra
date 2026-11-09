package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/agent/workspace"
)

var (
	beginTurnTimeout  = 5 * time.Second
	finishTurnTimeout = 2 * time.Second
)

type turnSnapshot struct {
	TurnID   string              `json:"turn_id"`
	Manifest *workspace.Manifest `json:"manifest"`
}

func turnPaths(stateDir string) (beforePath, lastPath string) {
	return filepath.Join(stateDir, "before.json"), filepath.Join(stateDir, "last.json")
}

func loadTurnSnapshot(path string) (*turnSnapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot turnSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func saveTurnSnapshot(path string, snapshot turnSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".before-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func blobKeep(manifests ...*workspace.Manifest) map[string]bool {
	keep := map[string]bool{}
	for _, manifest := range manifests {
		if manifest == nil {
			continue
		}
		for _, entry := range manifest.Entries {
			if entry.Text && entry.SHA256 != "" {
				keep[entry.SHA256] = true
			}
		}
	}
	return keep
}

// beginTurn 为新一轮保存修改前清单与文本 blob。同一轮的后续 run 复用快照;
// 任一步失败都撤掉基准,让收尾安静降级,不影响对话本身。
func (r *Runner) beginTurn(ar *activeRun, prep *prepared) {
	if prep == nil || ar.kind == "compact" || ar.turnID == "" {
		return
	}
	stateDir := r.cfg.Store.StateDir(ar.sessionID)
	beforePath, lastPath := turnPaths(stateDir)
	fail := func(step string, err error) {
		r.logger.Warn("保存轮开始快照失败", "session_id", ar.sessionID, "run_id", ar.runID, "step", step, "error", err)
		_ = os.Remove(beforePath)
	}
	before, err := loadTurnSnapshot(beforePath)
	if err != nil {
		fail("读取 before.json", err)
		return
	}
	if before != nil && before.TurnID == ar.turnID {
		return
	}
	last, err := workspace.LoadManifest(lastPath)
	if err != nil {
		fail("读取 last.json", err)
		return
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		fail("创建状态目录", err)
		return
	}
	// MkdirAll 不改已有目录权限;隔离布局下也始终由 root 独占。
	if err := os.Chmod(stateDir, 0o700); err != nil {
		fail("收紧状态目录权限", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), beginTurnTimeout)
	manifest, err := workspace.Scan(ctx, r.cfg.Store.WorkDir(ar.sessionID), last)
	cancel()
	if err != nil {
		fail("扫描工作区", err)
		return
	}
	if err := saveTurnSnapshot(beforePath, turnSnapshot{TurnID: ar.turnID, Manifest: manifest}); err != nil {
		fail("写入 before.json", err)
		return
	}
	blobs := workspace.NewStore(filepath.Join(stateDir, "blobs"))
	for _, entry := range manifest.Entries {
		if _, err := blobs.Put(r.cfg.Store.WorkDir(ar.sessionID), entry); err != nil {
			fail("保存修改前 blob", err)
			return
		}
	}
	if err := blobs.Prune(blobKeep(manifest, last)); err != nil {
		fail("清理旧 blob", err)
		return
	}
	if err := manifest.Save(lastPath); err != nil {
		fail("写入 last.json", err)
	}
}

// finishTurn 计算本轮开始到当前的整体差集,覆盖同轮记录并直接发布事件。
// 它位于终态之后、run.Finish 之前,因此不能经过 singleTerminal 限流的 publish。
func (r *Runner) finishTurn(ar *activeRun, prep *prepared) {
	if prep == nil || ar.kind == "compact" || ar.turnID == "" {
		return
	}
	stateDir := r.cfg.Store.StateDir(ar.sessionID)
	beforePath, lastPath := turnPaths(stateDir)
	before, err := loadTurnSnapshot(beforePath)
	if err != nil {
		r.logger.Warn("读取轮开始快照失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		return
	}
	if before == nil || before.TurnID != ar.turnID || before.Manifest == nil {
		return
	}
	last, err := workspace.LoadManifest(lastPath)
	if err != nil {
		r.logger.Warn("读取上次工作区清单失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), finishTurnTimeout)
	defer cancel()
	workDir := r.cfg.Store.WorkDir(ar.sessionID)
	after, err := workspace.Scan(ctx, workDir, last)
	if err != nil {
		r.logger.Warn("扫描收尾工作区失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		return
	}
	changes := workspace.Diff(before.Manifest, after)
	if len(changes) == 0 {
		if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
			meta.RemoveRunChange(ar.turnID)
			return nil
		}); err != nil {
			r.logger.Warn("移除空文件变更失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
		if err := after.Save(lastPath); err != nil {
			r.logger.Warn("保存收尾工作区清单失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
		}
		return
	}

	blobs := workspace.NewStore(filepath.Join(stateDir, "blobs"))
	// 只有清单确实没扫全(触顶/超时)才标不完整。未开 Landlock 时后台进程可能仍在写文件只是理论风险,
	// 本地/开发环境都是这种配置,不能因此让每一轮都带上「可能不完整」
	incomplete := after.Truncated || ctx.Err() != nil
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	files := make([]session.FileChange, 0, min(len(changes), session.MaxRunChangeFiles))
	for _, change := range changes {
		if len(files) >= session.MaxRunChangeFiles {
			break
		}
		oldEntry := before.Manifest.Entries[change.Path]
		newEntry := after.Entries[change.Path]
		entry := newEntry
		if change.Kind == "deleted" {
			entry = oldEntry
		}
		file := session.FileChange{Path: change.Path, Kind: change.Kind, Size: entry.Size, Binary: entry.Binary}
		var oldText, newText []byte
		oldOK, newOK := false, false
		if change.Kind != "added" && oldEntry.Text && oldEntry.SHA256 != "" {
			oldText, oldOK = blobs.Get(oldEntry.SHA256)
		}
		if change.Kind != "deleted" && newEntry.Text {
			// Put/Get 与 Scan 共用安全打开,同时留下新增/修改后的内容 blob。
			_, _ = blobs.Put(workDir, newEntry)
			newText, newOK = blobs.Get(newEntry.SHA256)
		}
		switch change.Kind {
		case "added":
			file.HasDiff = newEntry.Text
			oldOK = true // added 的修改前内容按空文本处理
		case "modified":
			file.HasDiff = oldOK && newEntry.Text
		case "deleted":
			file.HasDiff = oldOK && oldEntry.Text
			newOK = true // deleted 的修改后内容按空文本处理
		}
		if ctx.Err() != nil {
			incomplete = true
		} else if file.HasDiff && oldOK && newOK {
			file.Additions, file.Deletions = workspace.Stats(string(oldText), string(newText))
		}
		files = append(files, file)
	}

	ar.stateMu.Lock()
	anchor := ar.lastEntryID
	ar.stateMu.Unlock()
	if anchor == "" {
		anchor = r.anchorFromSessionFile(ar)
	}
	record := session.RunChange{
		TurnID: ar.turnID, RunIDs: []string{ar.runID}, AfterEntryID: anchor,
		Incomplete: incomplete, Total: len(changes), Files: files, At: time.Now().UTC(),
	}
	stored := false
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		meta.UpsertRunChange(record)
		for _, current := range meta.RunChanges {
			if current.TurnID == ar.turnID {
				record = current
			}
		}
		return nil
	}); err != nil {
		r.logger.Warn("记录文件变更失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	} else {
		stored = true
	}
	// 超过展示上限的变更也补 blob,供下一轮复用;Put 受内容存储上限约束。
	for _, change := range changes {
		if entry, ok := after.Entries[change.Path]; ok && entry.Text {
			_, _ = blobs.Put(workDir, entry)
		}
	}
	if err := after.Save(lastPath); err != nil {
		r.logger.Warn("保存收尾工作区清单失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	if err := blobs.Prune(blobKeep(before.Manifest, after)); err != nil {
		r.logger.Warn("清理收尾 blob 失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
	if !stored {
		return
	}
	data := map[string]any{
		"turn_id": record.TurnID, "run_ids": record.RunIDs,
		"total": record.Total, "files": record.Files,
	}
	if record.Incomplete {
		data["incomplete"] = true
	}
	if run := ar.finishedRun(); run != nil {
		run.Publish(translate.Event{Type: "run_changes", Data: data})
	}
}
