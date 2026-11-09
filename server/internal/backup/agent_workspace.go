package backup

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const agentWorkspaceArchivePrefix = "agent_workspace/"

// 上限声明为 var,便于测试中调小以覆盖超限分支。
var (
	maxAgentWorkspaceFileBytes  int64 = 1 << 30
	maxAgentWorkspaceTotalBytes int64 = 3 << 30
)

// agentWorkspaceSkipDirs 是导出时整棵跳过的目录名(任意深度):
// 均为可重建的依赖/缓存目录,备份体积大且无保留价值。
var agentWorkspaceSkipDirs = []string{"node_modules", ".cache", "__pycache__"}

// agentWorkspaceTempFilePattern 匹配恢复/导出流程遗留的临时文件,导出时跳过。
var agentWorkspaceTempFilePattern = regexp.MustCompile(`^\..*\.tmp-|\.restore-tmp-`)

// exportAgentWorkspace 把 Agent 共享工作区(用户真实工作文件)写入 zip 的
// agent_workspace/ 前缀下。root 不存在返回 0,nil;返回写入的文件数。
func exportAgentWorkspace(zw *zip.Writer, root string) (files int, err error) {
	var total int64
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 遍历中途文件消失视为跳过。
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("walk agent workspace %s: %w", p, err)
		}
		if d.IsDir() {
			if slices.Contains(agentWorkspaceSkipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// 不跟随符号链接:只导出普通文件,目录由文件路径隐式携带。
		if !d.Type().IsRegular() {
			return nil
		}
		if agentWorkspaceTempFilePattern.MatchString(d.Name()) {
			return nil
		}
		relPath, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("resolve agent workspace path %s: %w", p, err)
		}
		name := agentWorkspaceArchivePrefix + filepath.ToSlash(relPath)
		written, err := zipAgentWorkspaceFile(zw, name, p)
		if err != nil {
			return err
		}
		files++
		total += written
		if total > maxAgentWorkspaceTotalBytes {
			return fmt.Errorf("agent workspace exceeds maximum total size")
		}
		return nil
	})
	if err != nil {
		return files, fmt.Errorf("export agent workspace: %w", err)
	}
	return files, nil
}

// zipAgentWorkspaceFile 把单个工作区文件写入 zip,并保留权限位;
// 校验单文件与总量上限。
func zipAgentWorkspaceFile(zw *zip.Writer, name string, absPath string) (int64, error) {
	info, err := os.Lstat(absPath)
	if err != nil {
		return 0, fmt.Errorf("stat agent workspace file %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	if info.Size() > maxAgentWorkspaceFileBytes {
		return 0, fmt.Errorf("agent workspace file %s exceeds maximum size", name)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return 0, fmt.Errorf("open agent workspace file %s: %w", name, err)
	}
	defer file.Close()

	header := &zip.FileHeader{Name: name, Method: zipMethodForFile(name), Modified: info.ModTime()}
	header.SetMode(info.Mode().Perm())
	w, err := zw.CreateHeader(header)
	if err != nil {
		return 0, fmt.Errorf("create archive entry %s: %w", name, err)
	}
	written, err := io.Copy(w, io.LimitReader(file, maxAgentWorkspaceFileBytes+1))
	if err != nil {
		return written, fmt.Errorf("copy agent workspace file %s: %w", name, err)
	}
	if written > maxAgentWorkspaceFileBytes {
		return written, fmt.Errorf("agent workspace file %s exceeds maximum size", name)
	}
	return written, nil
}

// restoreAgentWorkspace 从备份 zip 恢复 agent_workspace/ 前缀条目到 root。
// 恢复语义是按文件覆盖,不删除备份里没有的文件;返回恢复的文件数与字节数。
func restoreAgentWorkspace(archivePath string, root string, progress func(int64)) (files int, bytes int64, err error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, 0, fmt.Errorf("create agent workspace root: %w", err)
	}
	for _, entry := range zr.File {
		if !strings.HasPrefix(entry.Name, agentWorkspaceArchivePrefix) {
			continue
		}
		written, isFile, err := extractAgentWorkspaceEntry(entry, root)
		if err != nil {
			return files, bytes, err
		}
		if !isFile {
			continue
		}
		files++
		bytes += written
		if bytes > maxAgentWorkspaceTotalBytes {
			return files, bytes, fmt.Errorf("agent workspace exceeds maximum total size")
		}
		if progress != nil {
			progress(bytes)
		}
	}
	return files, bytes, nil
}

// extractAgentWorkspaceEntry 恢复单个 agent_workspace/ 条目到 root:
// 文件先写临时文件再原子覆盖;目录条目只确保目录存在。
func extractAgentWorkspaceEntry(entry *zip.File, root string) (int64, bool, error) {
	relPath, isDir, err := cleanArchiveAgentWorkspacePath(entry.Name)
	if err != nil {
		return 0, false, err
	}
	if isDir || relPath == "" {
		if isDir {
			if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relPath)), 0o755); err != nil {
				return 0, false, fmt.Errorf("create agent workspace directory %s: %w", relPath, err)
			}
		}
		return 0, false, nil
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return 0, false, fmt.Errorf("backup archive contains agent workspace symlink %q", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxAgentWorkspaceFileBytes) {
		return 0, false, fmt.Errorf("agent workspace file %s exceeds maximum size", relPath)
	}

	target := filepath.Join(root, filepath.FromSlash(relPath))
	// 目标路径上已有目录挡住:报错且不删除任何现有内容。
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		return 0, false, fmt.Errorf("agent workspace restore target %s is a directory", relPath)
	}
	parent := filepath.Dir(target)
	// 父路径上有普通文件挡着:报错且不改动它。
	if info, err := os.Lstat(parent); err == nil && !info.IsDir() {
		return 0, false, fmt.Errorf("agent workspace restore parent %s is not a directory", filepath.ToSlash(filepath.Dir(relPath)))
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return 0, false, fmt.Errorf("create agent workspace directory for %s: %w", relPath, err)
	}

	reader, err := entry.Open()
	if err != nil {
		return 0, false, fmt.Errorf("open archived agent workspace file %s: %w", relPath, err)
	}
	defer reader.Close()

	// 同目录临时文件,写完后 rename 原子覆盖目标。
	tmp, err := os.CreateTemp(parent, "."+filepath.Base(target)+".restore-tmp-")
	if err != nil {
		return 0, false, fmt.Errorf("create temp file for %s: %w", relPath, err)
	}
	tmpPath := tmp.Name()
	written, copyErr := io.Copy(tmp, io.LimitReader(reader, maxAgentWorkspaceFileBytes+1))
	closeErr := tmp.Close()
	if copyErr == nil && closeErr == nil && written > maxAgentWorkspaceFileBytes {
		copyErr = fmt.Errorf("agent workspace file %s exceeds maximum size", relPath)
	}
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		if copyErr != nil {
			return 0, false, fmt.Errorf("extract agent workspace file %s: %w", relPath, copyErr)
		}
		return 0, false, fmt.Errorf("close agent workspace file %s: %w", relPath, closeErr)
	}
	perm := entry.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return 0, false, fmt.Errorf("chmod agent workspace file %s: %w", relPath, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return 0, false, fmt.Errorf("install agent workspace file %s: %w", relPath, err)
	}
	return written, true, nil
}

// cleanArchiveAgentWorkspacePath 校验 agent_workspace/ 前缀条目名:
// 拒绝空路径、越界路径、绝对路径与反斜杠。
func cleanArchiveAgentWorkspacePath(name string) (string, bool, error) {
	relPath := strings.TrimPrefix(name, agentWorkspaceArchivePrefix)
	clean := path.Clean(relPath)
	if clean == "." {
		return "", false, nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsRune(relPath, '\\') {
		return "", false, fmt.Errorf("backup archive contains invalid agent workspace path %q", name)
	}
	if strings.HasSuffix(name, "/") {
		return clean, true, nil
	}
	return clean, false, nil
}
