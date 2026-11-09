package capabilities

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const scopeManaged = "managed"

// Skill 对应前端 AgentSkill:技能摘要,列表与详情共用。
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"`          // 固定 "managed":由 xLyra 托管
	Enabled     bool   `json:"enabled"`        // 受 config.enable_skills 与 disabled_skills 影响
	Path        string `json:"path,omitempty"` // 相对 Root,如 "skills/pdf-tools"
	License     string `json:"license,omitempty"`
	Ownership   string `json:"ownership"` // 固定 "agent"
	Editable    bool   `json:"editable"`
	Deletable   bool   `json:"deletable"`
}

// SkillsInfo 对应前端 AgentSkillsInfo。
type SkillsInfo struct {
	Enabled  bool     `json:"enabled"`
	Skills   []Skill  `json:"skills"` // 按 name 排序;无技能时是空切片
	Warnings []string `json:"warnings,omitempty"`
}

// SkillDetail 对应前端 AgentSkillDetail,附完整 SKILL.md 与资源清单。
type SkillDetail struct {
	Skill
	Metadata         map[string]string `json:"metadata,omitempty"`
	Content          string            `json:"content"` // 完整 SKILL.md,超过 256KB 截断
	ContentTruncated bool              `json:"contentTruncated,omitempty"`
	Resources        []string          `json:"resources"` // 相对技能根目录,排序,最多 500 个
}

func (s *Store) skillsRoot() string { return filepath.Join(s.root, skillsDirName) }

func (s *Store) skillDir(name string) string { return filepath.Join(s.root, skillsDirName, name) }

// validSkillName 按 Agent Skills 规范校验技能名:1–64 个小写字母/数字/连字符,
// 不以连字符开头或结尾、不含连续连字符;这同时排除了路径穿越。
func validSkillName(name string) bool {
	if name == "" || len(name) > nameMaxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c != '-' {
			return false
		}
	}
	return !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "-") && !strings.Contains(name, "--")
}

// ListSkills 列出磁盘上的合法技能;非法条目(坏名字、坏 frontmatter、符号
// 链接)不进列表,而是记入 Warnings —— Pi 对非法技能静默不加载,这里把原因
// 告诉用户。
func (s *Store) ListSkills() (SkillsInfo, error) {
	config, err := s.GetConfig()
	if err != nil {
		return SkillsInfo{}, err
	}
	found, warnings, err := s.scanSkills()
	if err != nil {
		return SkillsInfo{}, err
	}
	info := SkillsInfo{Enabled: config.EnableSkills, Skills: []Skill{}, Warnings: warnings}
	for _, f := range found {
		info.Skills = append(info.Skills, newSkill(config, f.name, f.doc))
	}
	return info, nil
}

// newSkill 按配置计算 Enabled 等派生字段。
func newSkill(config Config, name string, doc skillDoc) Skill {
	enabled := config.EnableSkills
	if enabled {
		for _, n := range config.DisabledSkills {
			if n == name {
				enabled = false
				break
			}
		}
	}
	return Skill{
		Name:        name,
		Description: doc.description,
		Scope:       scopeManaged,
		Enabled:     enabled,
		Path:        skillsDirName + "/" + name,
		License:     doc.license,
		Ownership:   "agent",
		Editable:    true,
		Deletable:   true,
	}
}

// scannedSkill 是 scanSkills 的合法条目。
type scannedSkill struct {
	name string
	doc  skillDoc
}

// scanSkills 扫描 skills/ 目录,返回合法技能(按 name 排序)与非法条目的说明
// ("skills/<dir>: 原因" 由调用方拼装)。点开头的条目(文件系统噪声、原子写
// 留下的临时文件)直接跳过,不计入警告。
func (s *Store) scanSkills() ([]scannedSkill, []string, error) {
	entries, err := os.ReadDir(s.skillsRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var found []scannedSkill
	var warnings []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !validSkillName(name) {
			warnings = append(warnings, fmt.Sprintf("skills/%s: 非法技能名", name))
			continue
		}
		doc, _, reason, err := s.inspectSkill(name)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			warnings = append(warnings, fmt.Sprintf("skills/%s: %s", name, reason))
			continue
		}
		found = append(found, scannedSkill{name: name, doc: doc})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	return found, warnings, nil
}

// inspectSkill 读取并校验单个技能目录。reason 非空表示非法(不含
// "skills/<name>" 前缀,由调用方拼);目录不存在时返回 ErrNotFound;
// 其余 IO 错误原样返回。
func (s *Store) inspectSkill(name string) (skillDoc, string, string, error) {
	dir := s.skillDir(name)
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return skillDoc{}, "", "", ErrNotFound
		}
		return skillDoc{}, "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return skillDoc{}, "", "技能目录是符号链接", nil
	}
	if !info.IsDir() {
		return skillDoc{}, "", "不是目录", nil
	}
	skillPath := filepath.Join(dir, skillFileName)
	fi, err := os.Lstat(skillPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return skillDoc{}, "", "缺少 SKILL.md", nil
		}
		return skillDoc{}, "", "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return skillDoc{}, "", "SKILL.md 不是普通文件", nil
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return skillDoc{}, "", "", err
	}
	doc, verr := validateSkillContent(name, string(data))
	if verr != nil {
		return skillDoc{}, "", verr.Error(), nil
	}
	return doc, string(data), "", nil
}

// GetSkill 返回技能详情;名字非法返回 ErrInvalid,不存在返回 ErrNotFound,
// 磁盘上的技能不合法返回 ErrInvalid。
func (s *Store) GetSkill(name string) (SkillDetail, error) {
	if !validSkillName(name) {
		return SkillDetail{}, fmt.Errorf("%w: 非法技能名 %q", ErrInvalid, name)
	}
	doc, content, reason, err := s.inspectSkill(name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return SkillDetail{}, fmt.Errorf("%w: 技能 %q 不存在", ErrNotFound, name)
		}
		return SkillDetail{}, err
	}
	if reason != "" {
		return SkillDetail{}, fmt.Errorf("%w: skills/%s: %s", ErrInvalid, name, reason)
	}
	config, err := s.GetConfig()
	if err != nil {
		return SkillDetail{}, err
	}
	content, truncated := truncateUTF8(content, fileReadMaxBytes)
	return SkillDetail{
		Skill:            newSkill(config, name, doc),
		Metadata:         doc.metadata,
		Content:          content,
		ContentTruncated: truncated,
		Resources:        listResources(s.skillDir(name)),
	}, nil
}

// FixPermissions 一次性修正 skills/ 目录树的权限位:目录 0755、普通文件
// 0644——旧部署可能保留了更严的权限,而隔离后的会话身份(非 root)必须
// 能读技能。不跟随符号链接(链接本身跳过),其余特殊条目跳过;skills
// 不存在时忽略。返回实际修改的条目数。
func (s *Store) FixPermissions() (int, error) {
	root := s.skillsRoot()
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	changed := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		var want os.FileMode
		switch {
		case d.IsDir():
			want = skillDirPerm
		case d.Type().IsRegular():
			want = skillFilePerm
		default:
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm() == want {
			return nil
		}
		if err := os.Chmod(p, want); err != nil {
			return err
		}
		changed++
		return nil
	})
	return changed, err
}

// listResources 收集技能目录内的资源文件(不含 SKILL.md 本身):相对技能根
// 目录的路径,按序排列,最多 resourceListMax 个。符号链接与点开头的条目跳过
// (点开头同时避开原子写的临时文件),绝不跟随链接。
func listResources(dir string) []string {
	resources := []string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil // 根目录本身与读不了的条目跳过
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == skillFileName {
			return nil
		}
		if len(resources) >= resourceListMax {
			return fs.SkipAll
		}
		resources = append(resources, rel)
		return nil
	})
	sort.Strings(resources)
	return resources
}

// PutSkill 创建或覆盖技能的 SKILL.md;内容校验失败返回 ErrInvalid,内容未变
// 时不写文件并返回 changed=false。
func (s *Store) PutSkill(name, content string) (bool, error) {
	if !validSkillName(name) {
		return false, fmt.Errorf("%w: 非法技能名 %q", ErrInvalid, name)
	}
	if _, verr := validateSkillContent(name, content); verr != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalid, verr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.skillDir(name)
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, fmt.Errorf("%w: skills/%s 不是可写的技能目录", ErrInvalid, name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(dir, skillDirPerm); err != nil {
		return false, err
	}
	skillPath := filepath.Join(dir, skillFileName)
	if old, err := os.ReadFile(skillPath); err == nil && string(old) == content {
		return false, nil
	}
	if err := writeAtomic(skillPath, []byte(content), skillFilePerm); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteSkill 删除整个技能目录;不存在返回 (false, nil);目录是符号链接时
// 拒绝删除并返回 ErrInvalid(绝不跟随、不误删链接目标)。
func (s *Store) DeleteSkill(name string) (bool, error) {
	if !validSkillName(name) {
		return false, fmt.Errorf("%w: 非法技能名 %q", ErrInvalid, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.skillDir(name)
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: skills/%s 是符号链接,拒绝删除", ErrInvalid, name)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%w: skills/%s 不是技能目录", ErrInvalid, name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

// ReadSkillFile 读取技能内的资源文件,超过 fileReadMaxBytes 截断。rel 必须
// 是相对路径且清理后不含 ..;路径上任何一环(含技能目录)是符号链接都拒绝,
// 绝不跟随。
func (s *Store) ReadSkillFile(name, rel string) (string, bool, error) {
	if !validSkillName(name) {
		return "", false, fmt.Errorf("%w: 非法技能名 %q", ErrInvalid, name)
	}
	clean := path.Clean(rel)
	if rel == "" || path.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("%w: 非法文件路径 %q", ErrInvalid, rel)
	}
	dir := s.skillDir(name)
	fi, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("%w: 技能 %q 不存在", ErrNotFound, name)
		}
		return "", false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", false, fmt.Errorf("%w: skills/%s 不是技能目录", ErrInvalid, name)
	}
	// 逐段 Lstat,防止经由目录符号链接逃出技能目录
	full := dir
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		full = filepath.Join(full, part)
		fi, err := os.Lstat(full)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", false, fmt.Errorf("%w: 文件 %q 不存在", ErrNotFound, rel)
			}
			return "", false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("%w: 路径 %q 含符号链接", ErrInvalid, rel)
		}
		if i == len(parts)-1 {
			if !fi.Mode().IsRegular() {
				return "", false, fmt.Errorf("%w: %q 不是普通文件", ErrInvalid, rel)
			}
		} else if !fi.IsDir() {
			return "", false, fmt.Errorf("%w: 路径 %q 的中段不是目录", ErrInvalid, rel)
		}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", false, err
	}
	content, truncated := truncateUTF8(string(data), fileReadMaxBytes)
	return content, truncated, nil
}

// ---- SKILL.md frontmatter 手写最小解析器 ----
//
// 只支持本包需要的语法:key: value(值可带成对单/双引号)、metadata: 下的
// 缩进子项、标量键的 >、| 块写法(多行折叠为一行);其余键与行忽略。

// skillDoc 是从 frontmatter 提取的字段。
type skillDoc struct {
	name        string
	description string
	license     string
	metadata    map[string]string
}

// set 记录一个已知标量键;未知键忽略。
func (d *skillDoc) set(key, value string) {
	switch key {
	case "name":
		d.name = value
	case "description":
		d.description = value
	case "license":
		d.license = value
	}
}

// parseFrontmatter 解析以 --- 开头、第二个 --- 结束的 frontmatter 块。
func parseFrontmatter(content string) (skillDoc, error) {
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return skillDoc{}, errors.New("缺少 frontmatter(须以 --- 开头)")
	}
	doc := skillDoc{}
	var blockKey string // 正在收集的块标量键,空表示不在块中
	var blockLines []string
	inMetadata := false
	finishBlock := func() {
		if blockKey != "" {
			doc.set(blockKey, foldLines(blockLines))
			blockKey, blockLines = "", nil
		}
	}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		if !indented && strings.TrimSpace(line) == "---" {
			finishBlock()
			return doc, nil
		}
		if blockKey != "" {
			if indented || strings.TrimSpace(line) == "" {
				if t := strings.TrimSpace(line); t != "" {
					blockLines = append(blockLines, t)
				}
				continue
			}
			finishBlock() // 非缩进行结束块标量,该行按普通顶层行处理
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if indented {
			if inMetadata {
				if key, value, ok := splitKeyValue(line); ok {
					if doc.metadata == nil {
						doc.metadata = map[string]string{}
					}
					doc.metadata[key] = unquote(value)
				}
			}
			continue
		}
		inMetadata = false
		key, value, ok := splitKeyValue(line)
		if !ok {
			continue
		}
		if isBlockIndicator(value) {
			blockKey, blockLines = key, nil
			continue
		}
		value = unquote(value)
		if value == "" {
			if key == "metadata" {
				inMetadata = true
			}
			continue
		}
		doc.set(key, value)
	}
	return skillDoc{}, errors.New("frontmatter 未闭合(缺少第二个 ---)")
}

// splitKeyValue 按第一个冒号切出键值;无冒号或键为空视为不可解析。
func splitKeyValue(line string) (string, string, bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:i])
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(line[i+1:]), true
}

// isBlockIndicator 判断值是否是 >、|(含 >-、|+ 之类的 chomping/缩进指示)块标量。
func isBlockIndicator(v string) bool {
	if v == "" || (v[0] != '>' && v[0] != '|') {
		return false
	}
	rest := v[1:]
	return rest == strings.Trim(rest, "+-0123456789")
}

// foldLines 把块标量的多行内容合并为一行并折叠空白。
func foldLines(lines []string) string {
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// unquote 去掉成对的单/双引号。
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// validateSkillContent 校验 SKILL.md:体积上限、frontmatter 存在且闭合、
// name 必填且等于技能名、description 必填非空且不超过 1024 字符。
func validateSkillContent(name, content string) (skillDoc, error) {
	if len(content) > skillMaxBytes {
		return skillDoc{}, fmt.Errorf("SKILL.md 超过 128KB 上限")
	}
	doc, err := parseFrontmatter(content)
	if err != nil {
		return skillDoc{}, err
	}
	if doc.name == "" {
		return skillDoc{}, errors.New("frontmatter 缺少 name")
	}
	if doc.name != name {
		return skillDoc{}, fmt.Errorf("frontmatter name %q 与技能名 %q 不一致", doc.name, name)
	}
	if doc.description == "" {
		return skillDoc{}, errors.New("frontmatter 缺少 description")
	}
	if utf8.RuneCountInString(doc.description) > descriptionMaxRunes {
		return skillDoc{}, fmt.Errorf("description 超过 1024 字符上限")
	}
	return doc, nil
}
