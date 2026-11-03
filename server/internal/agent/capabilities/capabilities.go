// Package capabilities 管理 xLyra 托管给内置 Pi 的能力文件:
//
//	<Root>/skills/<name>/SKILL.md   技能(含资源文件)
//	<Root>/AGENTS.md                全局人格/指令
//	<Root>/config.json              能力开关配置
//
// 本包只做磁盘存取与校验(frontmatter 用手写的最小解析器,不引入 YAML 依赖),
// 不启动 Pi 进程、不涉及 HTTP。所有写入走「同目录临时文件 + rename」原子替换;
// 目录与文件分两类权限:技能相关(目录 0o755、文件 0o644——技能是管理员写的
// 指令而非秘密,隔离后的 Pi 身份要能读)与配置相关(目录 0o750、文件 0o640,
// 含 config.json 与 AGENTS.md);技能目录及其内部出现符号链接一律拒绝、绝不跟随。
package capabilities

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"unicode/utf8"
)

const (
	// 配置类(config.json、AGENTS.md 及能力根目录):对其他用户封闭。
	dirPerm  = 0o750
	filePerm = 0o640
	// 技能类(skills/ 及其下的目录与文件):对所有用户可读——Pi 以非 root
	// 身份运行时也要能读技能;技能内容是管理员指令,不是秘密。
	skillDirPerm  = 0o755
	skillFilePerm = 0o644

	skillsDirName  = "skills"
	skillFileName  = "SKILL.md"
	agentsMDName   = "AGENTS.md"
	configFileName = "config.json"

	skillMaxBytes       = 128 * 1024 // SKILL.md 整体上限
	fileReadMaxBytes    = 256 * 1024 // 详情/资源读取的截断阈值
	agentsMDMaxBytes    = 64 * 1024
	resourceListMax     = 500
	descriptionMaxRunes = 1024
	nameMaxLen          = 64

	defaultContextWindow = 200000
	defaultCompactRatio  = 0.9
	minCompactRatio      = 0.5
	maxCompactRatio      = 0.95
	minContextWindow     = 4096
	maxContextWindow     = 10_000_000
)

// ErrNotFound 表示技能或文件不存在。
var ErrNotFound = errors.New("capabilities: 不存在")

// ErrInvalid 表示名字、内容或配置值非法。
var ErrInvalid = errors.New("capabilities: 非法输入")

// Store 按 root 管理能力文件;写操作共用一把互斥(读操作只读单个文件,
// 依赖 rename 的原子性,无需加锁)。
type Store struct {
	root string
	mu   sync.Mutex
}

// NewStore 建立能力存储;root 不存在时在首次写入时创建。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// writeAtomic 以「同目录临时文件 + rename」原子替换写入,并强制文件权限。
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// truncateUTF8 在 n 字节处截断;落在多字节字符中间时退回到完整 UTF-8 边界。
func truncateUTF8(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// ---- AGENTS.md ----

func (s *Store) agentsMDPath() string { return filepath.Join(s.root, agentsMDName) }

// GetAgentsMD 读取全局人格文件;不存在时 exists=false。
func (s *Store) GetAgentsMD() (string, bool, error) {
	data, err := os.ReadFile(s.agentsMDPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// PutAgentsMD 写入 AGENTS.md,超过 64KB 拒绝。
func (s *Store) PutAgentsMD(content string) error {
	if len(content) > agentsMDMaxBytes {
		return fmt.Errorf("%w: AGENTS.md 超过 %d 字节上限", ErrInvalid, agentsMDMaxBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, dirPerm); err != nil {
		return err
	}
	return writeAtomic(s.agentsMDPath(), []byte(content), filePerm)
}

// DeleteAgentsMD 删除 AGENTS.md,幂等。
func (s *Store) DeleteAgentsMD() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.agentsMDPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ---- 配置 ----

// Config 是 <Root>/config.json 的能力配置,对应前端 AgentCapabilitiesConfig。
type Config struct {
	EnableAgentsMD      bool     `json:"enable_agents_md"`
	EnableSkills        bool     `json:"enable_skills"`
	DisabledSkills      []string `json:"disabled_skills"`
	CompactTriggerRatio float64  `json:"compact_trigger_ratio"`    // 自动压缩水位线,[0.5, 0.95]
	ContextWindow       int      `json:"context_window,omitempty"` // 0 表示未设置,运行时按 200000 兜底
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		EnableAgentsMD:      true,
		EnableSkills:        true,
		DisabledSkills:      []string{},
		CompactTriggerRatio: defaultCompactRatio,
	}
}

func (s *Store) configPath() string { return filepath.Join(s.root, configFileName) }

// GetConfig 读取配置;文件不存在或损坏(JSON 解析失败)时回退默认配置,
// 不报错也不改写原文件。
func (s *Store) GetConfig() (Config, error) {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}
	// 以默认值为底反序列化:文件里缺省的字段保持默认
	config := DefaultConfig()
	if err := json.Unmarshal(data, &config); err != nil {
		return DefaultConfig(), nil
	}
	if config.DisabledSkills == nil {
		config.DisabledSkills = []string{}
	}
	return config, nil
}

// PutConfig 校验并原子写入配置,返回规范化后的值;DisabledSkills 去重、
// 排序,每项须是合法技能名,越界字段返回 ErrInvalid 并说明字段。
func (s *Store) PutConfig(c Config) (Config, error) {
	if c.CompactTriggerRatio < minCompactRatio || c.CompactTriggerRatio > maxCompactRatio {
		return Config{}, fmt.Errorf("%w: compact_trigger_ratio 必须在 [%v, %v] 之间,当前 %v",
			ErrInvalid, minCompactRatio, maxCompactRatio, c.CompactTriggerRatio)
	}
	if c.ContextWindow != 0 && (c.ContextWindow < minContextWindow || c.ContextWindow > maxContextWindow) {
		return Config{}, fmt.Errorf("%w: context_window 必须在 [%d, %d] 之间或为 0(未设置),当前 %d",
			ErrInvalid, minContextWindow, maxContextWindow, c.ContextWindow)
	}
	for _, name := range c.DisabledSkills {
		if !validSkillName(name) {
			return Config{}, fmt.Errorf("%w: disabled_skills 含非法技能名 %q", ErrInvalid, name)
		}
	}
	seen := make(map[string]bool, len(c.DisabledSkills))
	disabled := make([]string, 0, len(c.DisabledSkills))
	for _, name := range c.DisabledSkills {
		if !seen[name] {
			seen[name] = true
			disabled = append(disabled, name)
		}
	}
	sort.Strings(disabled)
	c.DisabledSkills = disabled

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, dirPerm); err != nil {
		return Config{}, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return Config{}, err
	}
	data = append(data, '\n')
	if err := writeAtomic(s.configPath(), data, filePerm); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ---- 快照 ----

// Snapshot 是 runner 每次 run 取的能力快照:启用且合法的技能目录、生效的
// AGENTS.md 内容与上下文参数,均已应用开关与兜底逻辑。
type Snapshot struct {
	EnableSkills        bool
	EnableAgentsMD      bool
	SkillDirs           []string // 启用且合法的技能目录绝对路径,按 name 排序
	AgentsMD            string   // AGENTS.md 内容,不存在或被关闭时为空
	CompactTriggerRatio float64
	ContextWindow       int // 已应用 200000 兜底
}

// Snapshot 汇总当前配置与磁盘状态,供 runner 组装 Pi 启动参数。
func (s *Store) Snapshot() (Snapshot, error) {
	config, err := s.GetConfig()
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		EnableSkills:        config.EnableSkills,
		EnableAgentsMD:      config.EnableAgentsMD,
		SkillDirs:           []string{},
		CompactTriggerRatio: config.CompactTriggerRatio,
		ContextWindow:       config.ContextWindow,
	}
	if snap.ContextWindow == 0 {
		snap.ContextWindow = defaultContextWindow
	}
	if config.EnableSkills {
		found, _, err := s.scanSkills()
		if err != nil {
			return Snapshot{}, err
		}
		disabled := make(map[string]bool, len(config.DisabledSkills))
		for _, name := range config.DisabledSkills {
			disabled[name] = true
		}
		for _, f := range found {
			if disabled[f.name] {
				continue
			}
			abs, err := filepath.Abs(s.skillDir(f.name))
			if err != nil {
				return Snapshot{}, err
			}
			snap.SkillDirs = append(snap.SkillDirs, abs)
		}
	}
	if config.EnableAgentsMD {
		if content, _, err := s.GetAgentsMD(); err == nil {
			snap.AgentsMD = content
		}
	}
	return snap, nil
}
