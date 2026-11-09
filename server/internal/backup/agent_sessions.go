package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const agentSessionsArchivePrefix = "agent_sessions/"

// 会话目录下参与备份的子树;pi-agent/、exec*、workspace/ 等不进备份。
var agentSessionSubdirs = []string{"pi", ".xlyra"}

// 上限声明为 var,便于测试中调小以覆盖超限分支。
var (
	maxAgentSessionFileBytes   int64 = 512 << 20
	maxAgentSessionsTotalBytes int64 = 2 << 30
)

// renameFn 声明为包级 var,便于测试注入失败,覆盖切换回滚分支。
var renameFn = os.Rename

// exportAgentSessions 把 <root>/sessions 下各会话目录里的 meta.json、pi/、
// .xlyra/ 写入 zip 的 agent_sessions/ 前缀下。sessions 目录不存在返回 0,nil;
// 只导出规范 UUID 目录下的普通文件,符号链接与临时文件跳过;不导出
// pi-agent/、exec*、workspace/ 等运行时与旧版遗留内容。
func exportAgentSessions(zw *zip.Writer, root string) (int, error) {
	files := 0
	var total int64

	sessionsRoot := filepath.Join(root, "sessions")
	if !dirExists(sessionsRoot) {
		return 0, nil
	}
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return 0, fmt.Errorf("read agent sessions root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validAgentSessionID(entry.Name()) {
			continue
		}
		id := entry.Name()
		sessionDir := filepath.Join(sessionsRoot, id)
		// meta.json:只导普通文件。
		metaPath := filepath.Join(sessionDir, "meta.json")
		if info, err := os.Lstat(metaPath); err == nil && info.Mode().IsRegular() {
			written, err := zipAgentSessionFile(zw, agentSessionsArchivePrefix+id+"/meta.json", metaPath)
			if err != nil {
				return files, err
			}
			files++
			total += written
			if total > maxAgentSessionsTotalBytes {
				return files, fmt.Errorf("agent sessions exceed maximum total size")
			}
		}
		// pi/ 与 .xlyra/:整棵子树,只导普通文件。
		for _, sub := range agentSessionSubdirs {
			subRoot := filepath.Join(sessionDir, sub)
			if !dirExists(subRoot) {
				continue
			}
			err := filepath.WalkDir(subRoot, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// 不跟随符号链接:只导出普通文件,目录由文件路径隐式携带。
				if !d.Type().IsRegular() {
					return nil
				}
				if agentTempFilePattern.MatchString(d.Name()) {
					return nil
				}
				relPath, err := filepath.Rel(subRoot, p)
				if err != nil {
					return fmt.Errorf("resolve agent session path %s: %w", p, err)
				}
				name := agentSessionsArchivePrefix + id + "/" + sub + "/" + filepath.ToSlash(relPath)
				written, err := zipAgentSessionFile(zw, name, p)
				if err != nil {
					return err
				}
				files++
				total += written
				if total > maxAgentSessionsTotalBytes {
					return fmt.Errorf("agent sessions exceed maximum total size")
				}
				return nil
			})
			if err != nil {
				return files, fmt.Errorf("export agent session %s: %w", id, err)
			}
		}
	}
	return files, nil
}

// validAgentSessionID 要求目录名是规范化 UUID(与 session.validID 同规则)。
func validAgentSessionID(name string) bool {
	parsed, err := uuid.Parse(name)
	return err == nil && parsed.String() == name
}

// zipAgentSessionFile 把单个会话文件写入 zip,并校验单文件大小上限。
func zipAgentSessionFile(zw *zip.Writer, name string, absPath string) (int64, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return 0, fmt.Errorf("open agent session file %s: %w", name, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat agent session file %s: %w", name, err)
	}
	if info.Size() > maxAgentSessionFileBytes {
		return 0, fmt.Errorf("agent session file %s exceeds maximum size", name)
	}

	w, err := createArchiveEntry(zw, name, zipMethodForFile(name))
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(w, io.LimitReader(file, maxAgentSessionFileBytes+1))
	if err != nil {
		return written, fmt.Errorf("copy agent session file %s: %w", name, err)
	}
	if written > maxAgentSessionFileBytes {
		return written, fmt.Errorf("agent session file %s exceeds maximum size", name)
	}
	return written, nil
}

// restoreAgentSessions 从备份 zip 中恢复 agent_sessions/ 前缀的会话数据到
// <root>/sessions。先解压到 root 下的 staging 目录并做改写(meta 去 slot、
// pi 文件头改 cwd),再按会话整体换入(目标旧内容先移到 trash,失败反向
// 回滚);备份里没有的会话不动。返回恢复的会话数、文件数与字节数。
func restoreAgentSessions(archivePath, root, workspaceDir string, progress func(int64)) (int, int, int64, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(root, 0o750); err != nil {
		return 0, 0, 0, fmt.Errorf("create agent root: %w", err)
	}
	staging, err := os.MkdirTemp(root, ".restore-staging-sessions-")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("create agent sessions restore staging: %w", err)
	}
	defer os.RemoveAll(staging)

	files := 0
	var total int64
	sessions := map[string]bool{}
	for _, entry := range zr.File {
		if !strings.HasPrefix(entry.Name, agentSessionsArchivePrefix) {
			continue
		}
		id, written, isFile, err := extractAgentSessionEntry(entry, staging)
		if err != nil {
			return 0, 0, 0, err
		}
		if !isFile {
			continue
		}
		sessions[id] = true
		files++
		total += written
		if total > maxAgentSessionsTotalBytes {
			return 0, 0, 0, fmt.Errorf("agent sessions exceed maximum total size")
		}
		if progress != nil {
			progress(total)
		}
	}

	if err := rewriteStagedSessions(staging, workspaceDir); err != nil {
		return 0, 0, 0, err
	}
	if err := chmodStagedSessions(staging); err != nil {
		return 0, 0, 0, err
	}
	if err := swapAgentSessions(root, staging, sessions); err != nil {
		return 0, 0, 0, err
	}
	return len(sessions), files, total, nil
}

// extractAgentSessionEntry 解压单个 agent_sessions/ 条目到 staging,
// 返回会话 id、写入字节数与是否为文件。
func extractAgentSessionEntry(entry *zip.File, staging string) (string, int64, bool, error) {
	id, relPath, isDir, err := cleanArchiveAgentSessionPath(entry.Name)
	if err != nil {
		return "", 0, false, err
	}
	if isDir || relPath == "" {
		return id, 0, false, nil
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return "", 0, false, fmt.Errorf("backup archive contains agent session symlink %q", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxAgentSessionFileBytes) {
		return "", 0, false, fmt.Errorf("agent session file %s exceeds maximum size", relPath)
	}
	reader, err := entry.Open()
	if err != nil {
		return "", 0, false, fmt.Errorf("open archived agent session file %s: %w", relPath, err)
	}
	defer reader.Close()

	target := filepath.Join(staging, id, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return "", 0, false, fmt.Errorf("create agent session directory for %s: %w", relPath, err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, false, fmt.Errorf("create agent session file %s: %w", relPath, err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, maxAgentSessionFileBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return "", 0, false, fmt.Errorf("extract agent session file %s: %w", relPath, copyErr)
	}
	if closeErr != nil {
		return "", 0, false, fmt.Errorf("close agent session file %s: %w", relPath, closeErr)
	}
	if written > maxAgentSessionFileBytes {
		return "", 0, false, fmt.Errorf("agent session file %s exceeds maximum size", relPath)
	}
	return id, written, true, nil
}

// cleanArchiveAgentSessionPath 校验 agent_sessions/ 前缀条目名:
// 必须是 agent_sessions/<规范uuid>/meta.json、…/pi/… 或 …/.xlyra/…,
// 拒绝越界路径、反斜杠与其它顶层名字。
func cleanArchiveAgentSessionPath(name string) (string, string, bool, error) {
	relPath := strings.TrimPrefix(name, agentSessionsArchivePrefix)
	clean := path.Clean(relPath)
	if clean == "." {
		return "", "", false, nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsRune(relPath, '\\') {
		return "", "", false, fmt.Errorf("backup archive contains invalid agent session path %q", name)
	}
	id, rest, found := strings.Cut(clean, "/")
	if !found || !validAgentSessionID(id) {
		return "", "", false, fmt.Errorf("backup archive contains invalid agent session path %q", name)
	}
	if rest == "meta.json" {
		if strings.HasSuffix(name, "/") {
			return "", "", false, fmt.Errorf("backup archive contains agent session directory %q", name)
		}
		return id, "meta.json", false, nil
	}
	top, sub, hasSub := strings.Cut(rest, "/")
	if top != "pi" && top != ".xlyra" {
		return "", "", false, fmt.Errorf("backup archive contains unexpected agent session path %q", name)
	}
	if !hasSub {
		// pi/.xlyra 本身必须是目录,不允许作为文件条目。
		return "", "", false, fmt.Errorf("backup archive contains unexpected agent session path %q", name)
	}
	_ = sub
	if strings.HasSuffix(name, "/") {
		return id, rest, true, nil
	}
	return id, rest, false, nil
}

// rewriteStagedSessions 解压后在 staging 里改写:meta.json 删除 slot 键
// (隔离槽位换机器后无意义,下次运行重新分配);pi/*.jsonl 若指定了
// workspaceDir,把第一行文件头的 cwd 改写为新目录,其余内容原样保留。
func rewriteStagedSessions(staging, workspaceDir string) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return fmt.Errorf("read agent sessions staging: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validAgentSessionID(entry.Name()) {
			continue
		}
		id := entry.Name()
		metaPath := filepath.Join(staging, id, "meta.json")
		if err := stripSlotFromMeta(metaPath); err != nil {
			return fmt.Errorf("rewrite agent session %s meta: %w", id, err)
		}
		if workspaceDir == "" {
			continue
		}
		piDir := filepath.Join(staging, id, "pi")
		if !dirExists(piDir) {
			continue
		}
		piEntries, err := os.ReadDir(piDir)
		if err != nil {
			return fmt.Errorf("read agent session %s pi dir: %w", id, err)
		}
		for _, pe := range piEntries {
			if !pe.Type().IsRegular() {
				continue
			}
			jsonl := filepath.Join(piDir, pe.Name())
			if err := rewritePiHeaderCwd(jsonl, workspaceDir); err != nil {
				return fmt.Errorf("rewrite agent session %s pi file %s: %w", id, pe.Name(), err)
			}
		}
	}
	return nil
}

// stripSlotFromMeta 按 map 解析 meta.json 并删除 slot 键后写回(0o600),
// 其余键原样保留;文件不存在时跳过,解析失败报错。
func stripSlotFromMeta(metaPath string) error {
	data, err := os.ReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("parse meta.json: %w", err)
	}
	if _, ok := meta["slot"]; !ok {
		return nil
	}
	delete(meta, "slot")
	out, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, out, 0o600)
}

// rewritePiHeaderCwd 改写 Pi 会话文件第一行(文件头)的 cwd:
// 第一行是合法 JSON 对象时只替换 cwd 字段(其余字段含 id/timestamp 原样
// 保留),第一行之后的内容流式原样复制(文件可能很大,不整体读入内存),
// 临时文件 + rename 原子替换;第一行不是合法 JSON 对象则整个文件原样
// 保留,不报错。
func rewritePiHeaderCwd(jsonlPath, workspaceDir string) error {
	file, err := os.Open(jsonlPath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	first, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read first line: %w", err)
	}
	atEOF := err == io.EOF
	header := bytes.TrimSpace(first)
	if len(header) == 0 || !json.Valid(header) {
		// 第一行不是合法 JSON:原样保留,不改写。
		return nil
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(header, &probe) != nil {
		return nil
	}
	if _, ok := probe["cwd"]; !ok {
		return nil
	}
	cwd, err := json.Marshal(workspaceDir)
	if err != nil {
		return err
	}
	probe["cwd"] = cwd
	out, err := json.Marshal(probe)
	if err != nil {
		return err
	}

	tmpPath := jsonlPath + ".tmp-cwd"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := tmpFile.Write(append(out, '\n')); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if !atEOF {
		if _, err := io.Copy(tmpFile, reader); err != nil {
			tmpFile.Close()
			_ = os.Remove(tmpPath)
			return fmt.Errorf("copy remaining lines: %w", err)
		}
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := renameFn(tmpPath, jsonlPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace pi file: %w", err)
	}
	return nil
}

// chmodStagedSessions 显式设置 staging 内权限(不依赖 umask):
// 会话目录与 pi/、.xlyra/ 目录 0o750,文件 0o600。
func chmodStagedSessions(staging string) error {
	return filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if err := os.Chmod(p, 0o750); err != nil {
				return fmt.Errorf("chmod staged agent session dir: %w", err)
			}
			return nil
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return fmt.Errorf("chmod staged agent session file: %w", err)
		}
		return nil
	})
}

// swapAgentSessions 把 staging 里的会话逐个换入 <root>/sessions:
// 目标会话目录下已有的 meta.json、pi、.xlyra 先移到
// <root>/.trash-sessions-*/<id>/ 再换入 staging 里的;任一步失败按已完成的
// 移动反向回滚(把 trash 里的移回、移走已换入的)并返回错误;全部成功后
// 删除 trash。目标会话目录不存在则创建;备份里没有的会话不动;不碰目标
// 会话目录下的 pi-agent/、exec*。
func swapAgentSessions(root, staging string, sessions map[string]bool) error {
	sessionsDir := filepath.Join(root, "sessions")
	trash, err := os.MkdirTemp(root, ".trash-sessions-")
	if err != nil {
		return fmt.Errorf("create agent sessions trash dir: %w", err)
	}
	defer os.RemoveAll(trash)

	// swapEntry 记录单个会话已完成的移动,失败时反向回滚。
	type swapEntry struct {
		id        string
		movedTo   []string // 已移到 trash 的旧条目(meta.json/pi/.xlyra)
		installed []string // 已从 staging 换入的新条目
		created   bool     // 是否新建了目标会话目录
	}
	var done []*swapEntry
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			targetDir := filepath.Join(sessionsDir, m.id)
			// 移走已换入的,放回 staging(回滚失败不阻断后续条目的回滚)。
			for _, name := range m.installed {
				if dirExists(filepath.Join(targetDir, name)) || fileExists(filepath.Join(targetDir, name)) {
					_ = renameFn(filepath.Join(targetDir, name), filepath.Join(staging, m.id, name))
				}
			}
			// 把 trash 里的旧内容移回。
			for _, name := range m.movedTo {
				if dirExists(filepath.Join(trash, m.id, name)) || fileExists(filepath.Join(trash, m.id, name)) {
					_ = renameFn(filepath.Join(trash, m.id, name), filepath.Join(targetDir, name))
				}
			}
			if m.created {
				_ = os.RemoveAll(targetDir)
			}
		}
	}

	ids := make([]string, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := &swapEntry{id: id}
		targetDir := filepath.Join(sessionsDir, id)
		stagedDir := filepath.Join(staging, id)
		if !dirExists(targetDir) {
			if err := os.MkdirAll(targetDir, 0o750); err != nil {
				return fmt.Errorf("create agent session dir %s: %w", id, err)
			}
			m.created = true
		}
		for _, name := range append([]string{"meta.json"}, agentSessionSubdirs...) {
			source := filepath.Join(targetDir, name)
			if !dirExists(source) && !fileExists(source) {
				continue
			}
			if err := os.MkdirAll(filepath.Join(trash, id), 0o750); err != nil {
				rollback()
				return fmt.Errorf("set aside existing agent session %s: %w", id, err)
			}
			if err := renameFn(source, filepath.Join(trash, id, name)); err != nil {
				rollback()
				return fmt.Errorf("set aside existing agent session %s: %w", id, err)
			}
			m.movedTo = append(m.movedTo, name)
		}
		for _, name := range append([]string{"meta.json"}, agentSessionSubdirs...) {
			staged := filepath.Join(stagedDir, name)
			if !dirExists(staged) && !fileExists(staged) {
				continue
			}
			if err := renameFn(staged, filepath.Join(targetDir, name)); err != nil {
				rollback()
				return fmt.Errorf("install restored agent session %s: %w", id, err)
			}
			m.installed = append(m.installed, name)
		}
		done = append(done, m)
	}
	return nil
}

// fileExists 判断普通文件是否存在,供切换时探测目标条目。
func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.Mode().IsRegular()
}
