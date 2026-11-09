package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// 类别目录大小估算的整体预算:超时与目录项上限。超预算时返回的数值只是
// 下限,并标记 SizeTruncated。
const (
	sectionSizeTimeout    = 3 * time.Second
	sectionSizeMaxEntries = 200000
)

// errSectionSizeLimit 表示大小估算预算耗尽(超时或目录项超上限),此时
// 已累计的字节数仍然可用。
var errSectionSizeLimit = errors.New("section size estimate budget exceeded")

// SectionInfo 是类别目录(GET /settings/backup/sections)里单个类别的
// 描述:依赖、默认勾选、当前可用性,以及数据库行数或文件体积的估算值。
type SectionInfo struct {
	ID                string   `json:"id"`
	Group             string   `json:"group"`
	Requires          []string `json:"requires"`
	Recommends        []string `json:"recommends"`
	DefaultManual     bool     `json:"default_manual"`
	DefaultAuto       bool     `json:"default_auto"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	Rows              *int64   `json:"rows,omitempty"`           // 数据库类别:估算行数
	SizeBytes         *int64   `json:"size_bytes,omitempty"`     // 文件类别:估算字节数
	SizeTruncated     bool     `json:"size_truncated,omitempty"` // 估算超预算,数值只是下限
}

// SectionInfos 按注册表顺序返回全部类别的目录信息;数据库类别附带估算
// 行数,文件类别(当前可用时)附带估算体积。
func (s Service) SectionInfos(ctx context.Context) []SectionInfo {
	infos := make([]SectionInfo, 0, len(sections))
	for _, section := range sections {
		info := SectionInfo{
			ID:            section.ID,
			Group:         section.Group,
			Requires:      slicesClone(section.Requires),
			Recommends:    slicesClone(section.Recommends),
			DefaultManual: section.DefaultManual,
			DefaultAuto:   section.DefaultAuto,
		}
		info.Available, info.UnavailableReason = s.sectionAvailability(section.ID)
		switch section.ID {
		case sectionAgentCapabilities, sectionAgentSessions, sectionAgentWorkspace:
			if info.Available {
				info.SizeBytes, info.SizeTruncated = s.sectionSize(ctx, section.ID)
			}
		default:
			// 数据库类别:只在有表且查询成功时附带估算行数。
			if len(section.Tables) > 0 && s.db != nil && s.db.DB() != nil {
				if rows, ok := s.sectionRows(section.Tables); ok {
					info.Rows = &rows
				}
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// slicesClone 返回切片拷贝,nil 保持 nil,避免目录 JSON 里出现 null 之外的
// 差异(Requires/Recommends 在注册表里本就是 nil 或非空切片)。
func slicesClone(values []string) []string {
	// 始终返回非 nil 切片:JSON 里要是 [] 而不是 null,前端按数组遍历。
	return append([]string{}, values...)
}

// sectionAvailability 判断类别在当前 Service 下是否可用:agent 数据类
// 别需要 agentRoot,工作区类别需要 agentWorkspaceRoot,其余恒可用;
// 不可用时第二返回值是原因。
func (s Service) sectionAvailability(id string) (bool, string) {
	switch id {
	case sectionAgentCapabilities, sectionAgentSessions:
		if strings.TrimSpace(s.agentRoot) == "" {
			return false, "agent data directory is not configured"
		}
	case sectionAgentWorkspace:
		if strings.TrimSpace(s.agentWorkspaceRoot) == "" {
			return false, "agent workspace directory is not configured"
		}
	}
	return true, ""
}

// sectionRows 用 pg_stat_user_tables 的 n_live_tup 估算指定表的行数之和;
// 查询失败时返回 ok=false,由调用方决定不填。
func (s Service) sectionRows(tables []string) (int64, bool) {
	placeholders := make([]string, len(tables))
	args := make([]any, len(tables))
	for i, table := range tables {
		placeholders[i] = "?"
		args[i] = table
	}
	query := fmt.Sprintf(
		"SELECT relname, n_live_tup FROM pg_stat_user_tables WHERE relname IN (%s)",
		strings.Join(placeholders, ","),
	)
	var rows []struct {
		Relname  string `gorm:"column:relname"`
		NLiveTup int64  `gorm:"column:n_live_tup"`
	}
	if err := s.db.DB().Raw(query, args...).Scan(&rows).Error; err != nil {
		return 0, false
	}
	var total int64
	for _, row := range rows {
		total += row.NLiveTup
	}
	return total, true
}

// sectionSize 估算文件类别的体积;根目录不存在时第一返回值为 nil(不填)。
// 超出预算时返回已累计值并标记 truncated。
func (s Service) sectionSize(ctx context.Context, id string) (*int64, bool) {
	estimateCtx, cancel := context.WithTimeout(ctx, sectionSizeTimeout)
	defer cancel()

	counter := &sectionSizeCounter{ctx: estimateCtx}
	var total int64
	var walkErr error

	addFile := func(path string, d fs.DirEntry) {
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		total += info.Size()
	}

	switch id {
	case sectionAgentCapabilities:
		if !dirExists(s.agentRoot) {
			return nil, false
		}
		walkErr = counter.walkFile(filepath.Join(s.agentRoot, "skills"), nil, agentTempFilePattern, addFile)
		for _, name := range []string{"AGENTS.md", "config.json"} {
			if walkErr != nil {
				break
			}
			if info, err := os.Lstat(filepath.Join(s.agentRoot, name)); err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
		}
	case sectionAgentSessions:
		sessionsRoot := filepath.Join(s.agentRoot, "sessions")
		if !dirExists(sessionsRoot) {
			return nil, false
		}
		entries, err := os.ReadDir(sessionsRoot)
		if err != nil {
			return nil, false
		}
		for _, entry := range entries {
			if walkErr != nil {
				break
			}
			if !entry.IsDir() || !validAgentSessionID(entry.Name()) {
				continue
			}
			sessionDir := filepath.Join(sessionsRoot, entry.Name())
			metaPath := filepath.Join(sessionDir, "meta.json")
			if info, err := os.Lstat(metaPath); err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
			for _, sub := range agentSessionSubdirs {
				walkErr = counter.walkFile(filepath.Join(sessionDir, sub), nil, agentTempFilePattern, addFile)
				if walkErr != nil {
					break
				}
			}
		}
	case sectionAgentWorkspace:
		if !dirExists(s.agentWorkspaceRoot) {
			return nil, false
		}
		walkErr = counter.walkFile(s.agentWorkspaceRoot, agentWorkspaceSkipDirs, agentWorkspaceTempFilePattern, addFile)
	}

	if walkErr != nil && !errors.Is(walkErr, errSectionSizeLimit) && !errors.Is(walkErr, context.DeadlineExceeded) {
		return nil, false
	}
	return &total, counter.truncated
}

// sectionSizeCounter 限制大小估算的耗时与目录项数量;预算耗尽时通过
// errSectionSizeLimit 提前终止遍历。
type sectionSizeCounter struct {
	ctx       context.Context
	entries   int
	truncated bool
}

// check 在访问每个目录项前校验预算;超限标记 truncated 并返回终止错误。
func (c *sectionSizeCounter) check() error {
	if err := c.ctx.Err(); err != nil {
		c.truncated = true
		return errSectionSizeLimit
	}
	if c.entries >= sectionSizeMaxEntries {
		c.truncated = true
		return errSectionSizeLimit
	}
	c.entries++
	return nil
}

// walkFile 遍历 root 一棵子树:跳过 skipDirs 里名字命中的目录、临时文件
// 与非常规文件,普通文件调用 onFile;预算耗尽时返回 errSectionSizeLimit。
// 遍历中途文件消失视为跳过。root 不存在时直接返回 nil(不贡献体积)。
func (c *sectionSizeCounter) walkFile(root string, skipDirs []string, tempPattern *regexp.Regexp, onFile func(path string, d fs.DirEntry)) error {
	if root == "" || !dirExists(root) {
		return nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := c.check(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && slices.Contains(skipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if tempPattern != nil && tempPattern.MatchString(d.Name()) {
			return nil
		}
		onFile(p, d)
		return nil
	})
	if err != nil {
		return fmt.Errorf("estimate section size: %w", err)
	}
	return nil
}

// AvailableDefaultSections 返回手动导出或自动备份默认类别中当前可用的
// 子集(注册表顺序);自动备份执行与类别目录接口共用。
func (s Service) AvailableDefaultSections(auto bool) []string {
	var out []string
	for _, id := range DefaultSections(auto) {
		if available, _ := s.sectionAvailability(id); available {
			out = append(out, id)
		}
	}
	return out
}

// resolveAutomaticSections 返回自动备份应使用的类别:配置里显式保存了就
// 原样使用(不可用的类别交给导出报错,不静默丢弃);没保存则用自动默认
// 类别中当前可用的。
func (s Service) resolveAutomaticSections(saved []string) []string {
	if saved != nil {
		return saved
	}
	return s.AvailableDefaultSections(true)
}
