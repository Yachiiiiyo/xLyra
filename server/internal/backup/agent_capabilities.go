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

const (
	sectionAgentCapabilities = "agent_capabilities"
	agentArchivePrefix       = "agent/"
)

// 上限声明为 var,便于测试中调小以覆盖超限分支。
var (
	maxAgentFileBytes  int64 = 64 << 20
	maxAgentTotalBytes int64 = 512 << 20
)

var agentTopLevel = []string{"skills", "AGENTS.md", "config.json"}

// agentTempFilePattern 匹配能力写入流程遗留的临时文件(如同目录原子替换产生的
// `.SKILL.md.tmp-123`),导出时直接跳过。
var agentTempFilePattern = regexp.MustCompile(`^\..*\.tmp-`)

// exportAgentCapabilities 把 Agent 能力目录(skills/、AGENTS.md、config.json)
// 写入 zip 的 agent/ 前缀下。不存在的条目直接跳过;返回写入的文件数。
func exportAgentCapabilities(zw *zip.Writer, root string) (int, error) {
	files := 0
	var total int64

	skillsRoot := filepath.Join(root, "skills")
	if dirExists(skillsRoot) {
		err := filepath.WalkDir(skillsRoot, func(p string, d fs.DirEntry, err error) error {
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
			relPath, err := filepath.Rel(skillsRoot, p)
			if err != nil {
				return fmt.Errorf("resolve skill path %s: %w", p, err)
			}
			name := agentArchivePrefix + "skills/" + filepath.ToSlash(relPath)
			written, err := zipAgentFile(zw, name, p)
			if err != nil {
				return err
			}
			files++
			total += written
			if total > maxAgentTotalBytes {
				return fmt.Errorf("agent capabilities exceed maximum total size")
			}
			return nil
		})
		if err != nil {
			return files, fmt.Errorf("export agent skills: %w", err)
		}
	}

	for _, name := range []string{"AGENTS.md", "config.json"} {
		absPath := filepath.Join(root, name)
		info, err := os.Lstat(absPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return files, fmt.Errorf("stat agent %s: %w", name, err)
		}
		// 符号链接等非常规文件跳过,绝不跟随。
		if !info.Mode().IsRegular() {
			continue
		}
		written, err := zipAgentFile(zw, agentArchivePrefix+name, absPath)
		if err != nil {
			return files, err
		}
		files++
		total += written
		if total > maxAgentTotalBytes {
			return files, fmt.Errorf("agent capabilities exceed maximum total size")
		}
	}
	return files, nil
}

// zipAgentFile 把单个能力文件写入 zip,并校验单文件与解压侧一致的大小上限。
func zipAgentFile(zw *zip.Writer, name string, absPath string) (int64, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return 0, fmt.Errorf("open agent file %s: %w", name, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat agent file %s: %w", name, err)
	}
	if info.Size() > maxAgentFileBytes {
		return 0, fmt.Errorf("agent file %s exceeds maximum size", name)
	}

	w, err := createArchiveEntry(zw, name, zipMethodForFile(name))
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(w, io.LimitReader(file, maxAgentFileBytes+1))
	if err != nil {
		return written, fmt.Errorf("copy agent file %s: %w", name, err)
	}
	if written > maxAgentFileBytes {
		return written, fmt.Errorf("agent file %s exceeds maximum size", name)
	}
	return written, nil
}

// restoreAgentCapabilities 从备份 zip 中恢复 agent/ 前缀下的能力文件到 root。
// 先解压到 root 内的临时 staging 目录,再原子切换;返回恢复的文件数与字节数。
func restoreAgentCapabilities(archivePath string, root string, progress func(int64)) (int, int64, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open backup archive: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(root, 0o750); err != nil {
		return 0, 0, fmt.Errorf("create agent capabilities root: %w", err)
	}
	staging, err := os.MkdirTemp(root, ".restore-staging-")
	if err != nil {
		return 0, 0, fmt.Errorf("create agent capabilities restore staging: %w", err)
	}
	defer os.RemoveAll(staging)

	files := 0
	var total int64
	for _, entry := range zr.File {
		if !strings.HasPrefix(entry.Name, agentArchivePrefix) {
			continue
		}
		written, isFile, err := extractAgentEntry(entry, staging)
		if err != nil {
			return files, total, err
		}
		if !isFile {
			continue
		}
		files++
		total += written
		if total > maxAgentTotalBytes {
			return files, total, fmt.Errorf("agent capabilities exceed maximum total size")
		}
		if progress != nil {
			progress(total)
		}
	}

	if err := chmodAgentTree(staging); err != nil {
		return files, total, err
	}
	if err := swapAgentCapabilities(root, staging); err != nil {
		return files, total, err
	}
	return files, total, nil
}

// extractAgentEntry 解压单个 agent/ 条目到 staging,返回写入字节数与是否为文件。
func extractAgentEntry(entry *zip.File, staging string) (int64, bool, error) {
	relPath, isDir, err := cleanArchiveAgentPath(entry.Name)
	if err != nil {
		return 0, false, err
	}
	if isDir || relPath == "" {
		return 0, false, nil
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return 0, false, fmt.Errorf("backup archive contains agent symlink %q", entry.Name)
	}
	if entry.UncompressedSize64 > uint64(maxAgentFileBytes) {
		return 0, false, fmt.Errorf("agent file %s exceeds maximum size", relPath)
	}
	reader, err := entry.Open()
	if err != nil {
		return 0, false, fmt.Errorf("open archived agent file %s: %w", relPath, err)
	}
	defer reader.Close()

	target := filepath.Join(staging, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, false, fmt.Errorf("create agent directory for %s: %w", relPath, err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, false, fmt.Errorf("create agent file %s: %w", relPath, err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, maxAgentFileBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return 0, false, fmt.Errorf("extract agent file %s: %w", relPath, copyErr)
	}
	if closeErr != nil {
		return 0, false, fmt.Errorf("close agent file %s: %w", relPath, closeErr)
	}
	if written > maxAgentFileBytes {
		return 0, false, fmt.Errorf("agent file %s exceeds maximum size", relPath)
	}
	return written, true, nil
}

// cleanArchiveAgentPath 校验 agent/ 前缀条目名:拒绝越界路径、反斜杠,
// 顶层只允许 skills、AGENTS.md、config.json。
func cleanArchiveAgentPath(name string) (string, bool, error) {
	relPath := strings.TrimPrefix(name, agentArchivePrefix)
	clean := path.Clean(relPath)
	if clean == "." {
		return "", false, nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsRune(relPath, '\\') {
		return "", false, fmt.Errorf("backup archive contains invalid agent path %q", name)
	}
	top, _, _ := strings.Cut(clean, "/")
	if !slices.Contains(agentTopLevel, top) {
		return "", false, fmt.Errorf("backup archive contains unexpected agent path %q", name)
	}
	if strings.HasSuffix(name, "/") {
		if top == "AGENTS.md" || top == "config.json" {
			return "", false, fmt.Errorf("backup archive contains agent directory %q", name)
		}
		return "", true, nil
	}
	return clean, false, nil
}

// chmodAgentTree 解压完成后按能力包约定显式设置权限(不依赖 umask):
// skills/ 下目录 0o755、文件 0o644;AGENTS.md 与 config.json 0o640。
func chmodAgentTree(staging string) error {
	return filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(staging, p)
		if err != nil {
			return fmt.Errorf("resolve staged agent path %s: %w", p, err)
		}
		top := strings.Split(filepath.ToSlash(relPath), "/")[0]
		var perm os.FileMode
		switch {
		case top == "skills":
			if d.IsDir() {
				perm = 0o755
			} else {
				perm = 0o644
			}
		case top == "AGENTS.md" || top == "config.json":
			if d.IsDir() {
				return fmt.Errorf("staged agent %s is a directory", top)
			}
			perm = 0o640
		default:
			return nil
		}
		if err := os.Chmod(p, perm); err != nil {
			return fmt.Errorf("chmod staged agent path %s: %w", relPath, err)
		}
		return nil
	})
}

// swapAgentCapabilities 把 staging 中的能力条目切换到 root:
// skills 整目录替换(先移到 .trash-skills 再换入,失败回滚);AGENTS.md 与
// config.json 逐个覆盖,备份中没有的则删除现有文件,使恢复后状态与备份一致。
func swapAgentCapabilities(root string, staging string) error {
	currentSkills := filepath.Join(root, "skills")
	trashSkills := filepath.Join(root, ".trash-skills")
	_ = os.RemoveAll(trashSkills)
	movedOld := false
	if dirExists(currentSkills) {
		if err := os.Rename(currentSkills, trashSkills); err != nil {
			return fmt.Errorf("set aside existing agent skills: %w", err)
		}
		movedOld = true
	}
	stagedSkills := filepath.Join(staging, "skills")
	if dirExists(stagedSkills) {
		if err := os.Rename(stagedSkills, currentSkills); err != nil {
			if movedOld {
				_ = os.Rename(trashSkills, currentSkills)
			}
			return fmt.Errorf("install restored agent skills: %w", err)
		}
	}
	if err := os.RemoveAll(trashSkills); err != nil {
		return fmt.Errorf("remove previous agent skills: %w", err)
	}

	for _, name := range []string{"AGENTS.md", "config.json"} {
		staged := filepath.Join(staging, name)
		if info, err := os.Lstat(staged); err == nil && info.Mode().IsRegular() {
			if err := os.Rename(staged, filepath.Join(root, name)); err != nil {
				return fmt.Errorf("install restored agent %s: %w", name, err)
			}
			continue
		}
		// 备份中没有该文件:删除现有内容,忽略不存在。
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("remove agent %s: %w", name, err)
		}
	}
	return nil
}
