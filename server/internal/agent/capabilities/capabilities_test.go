package capabilities

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// ---- 测试助手 ----

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func skillMD(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n\n使用说明正文。\n", name, description, name)
}

func mustPutSkill(t *testing.T, store *Store, name, description string) {
	t.Helper()
	if changed, err := store.PutSkill(name, skillMD(name, description)); err != nil || !changed {
		t.Fatalf("PutSkill(%q): changed=%v err=%v", name, changed, err)
	}
}

// writeSkillFile 直接往技能目录里塞文件(模拟资源或手动放置的内容),
// 权限与技能写入路径一致。
func writeSkillFile(t *testing.T, store *Store, name, rel, content string) {
	t.Helper()
	path := filepath.Join(store.root, skillsDirName, name, rel)
	if err := os.MkdirAll(filepath.Dir(path), skillDirPerm); err != nil {
		t.Fatalf("创建目录 %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), skillFilePerm); err != nil {
		t.Fatalf("写入 %s: %v", path, err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前环境无法创建符号链接: %v", err)
	}
}

// ---- 技能 CRUD 全流程 ----

func TestSkillCRUDFlow(t *testing.T) {
	store := newTestStore(t)

	// 无技能时是空切片,JSON 序列化为 []
	info, err := store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if info.Skills == nil || len(info.Skills) != 0 {
		t.Fatalf("无技能时应为空切片,得到 %+v", info.Skills)
	}
	if encoded, _ := json.Marshal(info); !strings.Contains(string(encoded), `"skills":[]`) {
		t.Fatalf("空技能列表应序列化为 [],得到 %s", encoded)
	}

	content := skillMD("pdf-tools", "Extract text and tables from PDFs. Use when reading PDFs.")
	changed, err := store.PutSkill("pdf-tools", content)
	if err != nil || !changed {
		t.Fatalf("首次 PutSkill: changed=%v err=%v", changed, err)
	}
	mustPutSkill(t, store, "alpha-skill", "Alpha skill.")

	info, err = store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if !info.Enabled || len(info.Warnings) != 0 {
		t.Fatalf("默认配置应启用且无警告: %+v", info)
	}
	if len(info.Skills) != 2 || info.Skills[0].Name != "alpha-skill" || info.Skills[1].Name != "pdf-tools" {
		t.Fatalf("技能应按 name 排序: %+v", info.Skills)
	}
	skill := info.Skills[1]
	if skill.Description != "Extract text and tables from PDFs. Use when reading PDFs." ||
		skill.Scope != "managed" || !skill.Enabled || skill.Path != "skills/pdf-tools" ||
		skill.Ownership != "agent" || !skill.Editable || !skill.Deletable {
		t.Fatalf("技能字段不符: %+v", skill)
	}

	// 资源文件:写入、出现在详情、可读
	writeSkillFile(t, store, "pdf-tools", "references/formats.md", "# formats\n")
	detail, err := store.GetSkill("pdf-tools")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if detail.Content != content || detail.ContentTruncated {
		t.Fatalf("详情应含完整原文: truncated=%v len=%d", detail.ContentTruncated, len(detail.Content))
	}
	if !reflect.DeepEqual(detail.Resources, []string{"references/formats.md"}) {
		t.Fatalf("资源清单不符: %+v", detail.Resources)
	}
	got, truncated, err := store.ReadSkillFile("pdf-tools", "references/formats.md")
	if err != nil || truncated || got != "# formats\n" {
		t.Fatalf("ReadSkillFile: got=%q truncated=%v err=%v", got, truncated, err)
	}

	// 相同内容不重写
	changed, err = store.PutSkill("pdf-tools", content)
	if err != nil {
		t.Fatalf("重复 PutSkill: %v", err)
	}
	if changed {
		t.Fatal("内容未变时 changed 应为 false")
	}

	// 覆盖后详情更新
	updated := skillMD("pdf-tools", "Updated description.")
	if changed, err = store.PutSkill("pdf-tools", updated); err != nil || !changed {
		t.Fatalf("覆盖 PutSkill: changed=%v err=%v", changed, err)
	}
	if detail, err = store.GetSkill("pdf-tools"); err != nil || detail.Description != "Updated description." {
		t.Fatalf("覆盖后详情不符: %+v err=%v", detail, err)
	}

	// 删除
	deleted, err := store.DeleteSkill("pdf-tools")
	if err != nil || !deleted {
		t.Fatalf("DeleteSkill: deleted=%v err=%v", deleted, err)
	}
	if _, err := store.GetSkill("pdf-tools"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后 GetSkill 应为 ErrNotFound,得到 %v", err)
	}
	if deleted, err = store.DeleteSkill("pdf-tools"); err != nil || deleted {
		t.Fatalf("重复 DeleteSkill 应返回 (false, nil): deleted=%v err=%v", deleted, err)
	}
}

// ---- 非法技能名 ----

func TestSkillNameValidation(t *testing.T) {
	store := newTestStore(t)
	bad := []string{"../x", "Pdf-Tool", "pdf_tools", "a--b", "-a", "a-", strings.Repeat("a", 65), ""}
	for _, name := range bad {
		if _, err := store.PutSkill(name, skillMD("x", "d")); !errors.Is(err, ErrInvalid) {
			t.Errorf("PutSkill(%q) 应返回 ErrInvalid,得到 %v", name, err)
		}
		if _, err := store.GetSkill(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("GetSkill(%q) 应返回 ErrInvalid,得到 %v", name, err)
		}
		if _, err := store.DeleteSkill(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("DeleteSkill(%q) 应返回 ErrInvalid,得到 %v", name, err)
		}
		if _, _, err := store.ReadSkillFile(name, "a.md"); !errors.Is(err, ErrInvalid) {
			t.Errorf("ReadSkillFile(%q) 应返回 ErrInvalid,得到 %v", name, err)
		}
	}
	// 边界:恰好 64 字符合法
	long := strings.Repeat("a", 64)
	if _, err := store.PutSkill(long, skillMD(long, "合法的长名字。")); err != nil {
		t.Fatalf("64 字符的名字应合法: %v", err)
	}
}

// ---- SKILL.md 内容校验 ----

func TestPutSkillContentValidation(t *testing.T) {
	store := newTestStore(t)
	cases := []struct {
		name    string
		content string
		wantSub string // 错误信息里应包含的片段
	}{
		{"名字不一致", "---\nname: other-tools\ndescription: d\n---\n", "不一致"},
		{"缺 description", "---\nname: pdf-tools\n---\n", "description"},
		{"description 为空", "---\nname: pdf-tools\ndescription:\n---\n", "description"},
		{"缺 frontmatter", "# 只有正文\n", "frontmatter"},
		{"frontmatter 未闭合", "---\nname: pdf-tools\ndescription: d\n", "闭合"},
		{"description 超长", "---\nname: pdf-tools\ndescription: " + strings.Repeat("描", 1025) + "\n---\n", "1024"},
		{"整体超限", "---\nname: pdf-tools\ndescription: d\n---\n\n" + strings.Repeat("a", 130*1024), "128KB"},
	}
	for _, tc := range cases {
		_, err := store.PutSkill("pdf-tools", tc.content)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: 应返回 ErrInvalid,得到 %v", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: 错误信息 %q 应包含 %q", tc.name, err.Error(), tc.wantSub)
		}
	}
	// 校验失败不落盘
	info, err := store.ListSkills()
	if err != nil || len(info.Skills) != 0 {
		t.Fatalf("校验失败的技能不应落盘: %+v err=%v", info, err)
	}
	if _, err := store.GetSkill("pdf-tools"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未落盘的技能应为 ErrNotFound,得到 %v", err)
	}
	// 边界:恰好 1024 字符的 description 合法
	ok := "---\nname: pdf-tools\ndescription: " + strings.Repeat("描", 1024) + "\n---\n"
	if _, err := store.PutSkill("pdf-tools", ok); err != nil {
		t.Fatalf("1024 字符的 description 应合法: %v", err)
	}
}

// ---- frontmatter 写法:块标量、引号、metadata ----

func TestFrontmatterForms(t *testing.T) {
	store := newTestStore(t)

	folded := "---\nname: demo\ndescription: >\n  第一行。\n  第二行\n  第三行。\nlicense: MIT\nmetadata:\n  author: alice\n  version: \"1.0\"\ncompatibility: ignore-me\n无冒号的乱行\n: 无键的乱行\n---\n\n正文。\n"
	if changed, err := store.PutSkill("demo", folded); err != nil || !changed {
		t.Fatalf("PutSkill(folded): changed=%v err=%v", changed, err)
	}
	detail, err := store.GetSkill("demo")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if detail.Description != "第一行。 第二行 第三行。" {
		t.Fatalf("> 折叠描述不符: %q", detail.Description)
	}
	if detail.License != "MIT" {
		t.Fatalf("license 不符: %q", detail.License)
	}
	wantMeta := map[string]string{"author": "alice", "version": "1.0"}
	if !reflect.DeepEqual(detail.Metadata, wantMeta) {
		t.Fatalf("metadata 不符: %+v", detail.Metadata)
	}
	if detail.Content != folded {
		t.Fatal("详情应返回完整原文")
	}

	literal := "---\nname: demo\ndescription: |\n  A\n  B\n---\nx"
	if _, err := store.PutSkill("demo", literal); err != nil {
		t.Fatalf("PutSkill(literal): %v", err)
	}
	if detail, err = store.GetSkill("demo"); err != nil || detail.Description != "A B" {
		t.Fatalf("| 块描述应为 \"A B\": %+v err=%v", detail, err)
	}

	quoted := "---\nname: 'demo'\ndescription: \"带空格 的 描述\"\n---\nx"
	if _, err := store.PutSkill("demo", quoted); err != nil {
		t.Fatalf("PutSkill(quoted): %v", err)
	}
	if detail, err = store.GetSkill("demo"); err != nil || detail.Description != "带空格 的 描述" {
		t.Fatalf("引号值应去引号: %+v err=%v", detail, err)
	}
	if detail.Metadata != nil {
		t.Fatalf("无 metadata 时应为 nil: %+v", detail.Metadata)
	}
}

// ---- 磁盘上的非法技能进 Warnings,不进 Skills / SkillDirs ----

func TestInvalidSkillsOnDiskWarnings(t *testing.T) {
	store := newTestStore(t)
	writeSkillFile(t, store, "Bad-Name", "SKILL.md", skillMD("bad-name", "d"))
	writeSkillFile(t, store, "no-markdown", ".keep", "")
	writeSkillFile(t, store, "broken", "SKILL.md", "---\nname: broken\n---\n")
	writeSkillFile(t, store, "mismatch", "SKILL.md", "---\nname: other\ndescription: d\n---\n")
	// 名字合法但不是目录(普通文件)也应警告
	if err := os.WriteFile(filepath.Join(store.root, skillsDirName, "stray-file"), []byte("x"), filePerm); err != nil {
		t.Fatal(err)
	}
	// 点开头条目是文件系统噪声,静默跳过
	if err := os.MkdirAll(filepath.Join(store.root, skillsDirName, ".hidden"), dirPerm); err != nil {
		t.Fatal(err)
	}
	mustPutSkill(t, store, "good-one", "好的技能。")

	info, err := store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(info.Skills) != 1 || info.Skills[0].Name != "good-one" {
		t.Fatalf("只应列出合法技能: %+v", info.Skills)
	}
	if len(info.Warnings) != 5 {
		t.Fatalf("应有 5 条警告: %+v", info.Warnings)
	}
	joined := strings.Join(info.Warnings, "\n")
	for _, want := range []string{"skills/Bad-Name", "skills/no-markdown", "skills/broken", "skills/mismatch", "skills/stray-file"} {
		if !strings.Contains(joined, want) {
			t.Errorf("警告应包含 %q: %+v", want, info.Warnings)
		}
	}
	// 详情入口同样拒绝非法技能
	for _, name := range []string{"broken", "mismatch", "stray-file", "no-markdown"} {
		if _, err := store.GetSkill(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("GetSkill(%q) 磁盘非法技能应为 ErrInvalid,得到 %v", name, err)
		}
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.SkillDirs) != 1 || !strings.HasSuffix(snap.SkillDirs[0], "good-one") {
		t.Fatalf("SkillDirs 只应含合法技能: %+v", snap.SkillDirs)
	}
}

// ---- 权限分层 ----

// TestPermissionSplit:技能目录/文件对所有人可读(隔离后的 Pi 身份要能读),
// config.json 与 AGENTS.md 仍保持 0640。
func TestPermissionSplit(t *testing.T) {
	store := newTestStore(t)
	mustPutSkill(t, store, "pdf-tools", "PDF 技能。")
	if err := store.PutAgentsMD("全局人格"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConfig(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(store.root, skillsDirName),
		filepath.Join(store.root, skillsDirName, "pdf-tools"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != skillDirPerm {
			t.Fatalf("%s 权限 = %o, want %o(技能目录所有用户可读)", p, perm, skillDirPerm)
		}
	}
	skillMDPath := filepath.Join(store.root, skillsDirName, "pdf-tools", skillFileName)
	info, err := os.Stat(skillMDPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != skillFilePerm {
		t.Fatalf("SKILL.md 权限 = %o, want %o", perm, skillFilePerm)
	}
	for _, p := range []string{store.agentsMDPath(), store.configPath()} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != filePerm {
			t.Fatalf("%s 权限 = %o, want %o(配置文件保持原权限)", p, perm, filePerm)
		}
	}
}

// ---- 符号链接安全 ----

func TestSymlinkSafety(t *testing.T) {
	store := newTestStore(t)
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("SECRET"), filePerm); err != nil {
		t.Fatal(err)
	}

	mustPutSkill(t, store, "safe", "安全技能。")
	safeDir := filepath.Join(store.root, skillsDirName, "safe")
	symlink(t, secretPath, filepath.Join(safeDir, "link.txt"))
	symlink(t, outside, filepath.Join(safeDir, "dirlink"))

	// 资源清单不含符号链接;读链接资源一律拒绝
	detail, err := store.GetSkill("safe")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	for _, r := range detail.Resources {
		if strings.Contains(r, "link") {
			t.Fatalf("资源清单不应含符号链接: %+v", detail.Resources)
		}
	}
	reads := []string{"link.txt", "dirlink/secret.txt", "../../secret.txt", "/etc/passwd", ""}
	for _, rel := range reads {
		got, _, err := store.ReadSkillFile("safe", rel)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ReadSkillFile(%q) 应返回 ErrInvalid,得到 err=%v content=%q", rel, err, got)
		}
		if got != "" {
			t.Errorf("ReadSkillFile(%q) 不应读出内容", rel)
		}
	}

	// 技能目录本身是符号链接:列表警告、详情/读文件拒绝、删除拒绝且链接保留
	var evilContent = skillMD("linked-skill", "外部的技能。")
	evilDir := filepath.Join(outside, "evil-skill")
	if err := os.MkdirAll(evilDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evilDir, "SKILL.md"), []byte(evilContent), filePerm); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(store.root, skillsDirName, "linked-skill")
	symlink(t, evilDir, linkPath)

	info, err := store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	for _, sk := range info.Skills {
		if sk.Name == "linked-skill" {
			t.Fatal("符号链接技能不应出现在列表里")
		}
	}
	if !strings.Contains(strings.Join(info.Warnings, "\n"), "skills/linked-skill") {
		t.Fatalf("警告应包含符号链接技能: %+v", info.Warnings)
	}
	if _, err := store.GetSkill("linked-skill"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("GetSkill 符号链接技能应为 ErrInvalid,得到 %v", err)
	}
	if got, _, err := store.ReadSkillFile("linked-skill", "SKILL.md"); !errors.Is(err, ErrInvalid) || got != "" {
		t.Fatalf("ReadSkillFile 符号链接技能应拒绝: err=%v content=%q", err, got)
	}
	if deleted, err := store.DeleteSkill("linked-skill"); !errors.Is(err, ErrInvalid) || deleted {
		t.Fatalf("DeleteSkill 符号链接技能应拒绝: deleted=%v err=%v", deleted, err)
	}
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("链接本身不应被删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evilDir, "SKILL.md")); err != nil {
		t.Fatalf("链接目标内容不应被动到: %v", err)
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, dir := range snap.SkillDirs {
		if strings.HasSuffix(dir, "linked-skill") {
			t.Fatal("符号链接技能不应进入 SkillDirs")
		}
	}
}

// ---- 启用状态与 Snapshot ----

func TestDisabledSkillsAndToggles(t *testing.T) {
	store := newTestStore(t)
	mustPutSkill(t, store, "skill-a", "技能 A。")
	mustPutSkill(t, store, "skill-b", "技能 B。")

	if _, err := store.PutConfig(Config{EnableAgentsMD: true, EnableSkills: true, DisabledSkills: []string{"skill-a"}, CompactTriggerRatio: 0.9}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	info, err := store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if !info.Enabled || len(info.Skills) != 2 {
		t.Fatalf("列表应含两个技能: %+v", info)
	}
	enabled := map[string]bool{}
	for _, sk := range info.Skills {
		enabled[sk.Name] = sk.Enabled
	}
	if enabled["skill-a"] || !enabled["skill-b"] {
		t.Fatalf("skill-a 应被禁用: %+v", enabled)
	}
	detail, err := store.GetSkill("skill-a")
	if err != nil || detail.Enabled {
		t.Fatalf("详情里 skill-a 也应是禁用: %+v err=%v", detail, err)
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.SkillDirs) != 1 || snap.SkillDirs[0] != filepath.Join(store.root, skillsDirName, "skill-b") {
		t.Fatalf("SkillDirs 应只含启用的技能的绝对路径: %+v", snap.SkillDirs)
	}

	// 总开关关闭:列表全部禁用,快照 SkillDirs 为空
	if _, err := store.PutConfig(Config{EnableAgentsMD: true, EnableSkills: false, DisabledSkills: []string{"skill-a"}, CompactTriggerRatio: 0.9}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	info, err = store.ListSkills()
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if info.Enabled {
		t.Fatal("总开关关闭后 SkillsInfo.Enabled 应为 false")
	}
	for _, sk := range info.Skills {
		if sk.Enabled {
			t.Fatalf("总开关关闭后所有技能应禁用: %+v", sk)
		}
	}
	if snap, err = store.Snapshot(); err != nil || len(snap.SkillDirs) != 0 {
		t.Fatalf("总开关关闭后 SkillDirs 应为空: %+v err=%v", snap.SkillDirs, err)
	}
}

func TestConfigNormalization(t *testing.T) {
	store := newTestStore(t)
	cfg, err := store.PutConfig(Config{
		EnableAgentsMD: true, EnableSkills: true,
		DisabledSkills:      []string{"skill-b", "skill-a", "skill-b"},
		CompactTriggerRatio: 0.8,
	})
	if err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg.DisabledSkills, []string{"skill-a", "skill-b"}) {
		t.Fatalf("DisabledSkills 应去重排序: %+v", cfg.DisabledSkills)
	}
	saved, err := store.GetConfig()
	if err != nil || !reflect.DeepEqual(saved, cfg) {
		t.Fatalf("落盘后读取应一致: %+v err=%v", saved, err)
	}
}

// ---- AGENTS.md ----

func TestAgentsMDLifecycle(t *testing.T) {
	store := newTestStore(t)
	if content, exists, err := store.GetAgentsMD(); err != nil || exists || content != "" {
		t.Fatalf("初始应不存在: content=%q exists=%v err=%v", content, exists, err)
	}
	if err := store.PutAgentsMD("你是严谨的助手。"); err != nil {
		t.Fatalf("PutAgentsMD: %v", err)
	}
	if content, exists, err := store.GetAgentsMD(); err != nil || !exists || content != "你是严谨的助手。" {
		t.Fatalf("写入后读取不符: content=%q exists=%v err=%v", content, exists, err)
	}
	// 边界:恰好 64KB 合法(21845 个三字节汉字 + 1 字节)
	if err := store.PutAgentsMD(strings.Repeat("好", agentsMDMaxBytes/3) + "a"); err != nil {
		t.Fatalf("64KB 应合法: %v", err)
	}
	if err := store.PutAgentsMD(strings.Repeat("好", agentsMDMaxBytes/3+1)); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("超过 64KB 应返回 ErrInvalid,得到 %v", err)
	}
	if err := store.DeleteAgentsMD(); err != nil {
		t.Fatalf("DeleteAgentsMD: %v", err)
	}
	if content, exists, err := store.GetAgentsMD(); err != nil || exists || content != "" {
		t.Fatalf("删除后应不存在: content=%q exists=%v err=%v", content, exists, err)
	}
	if err := store.DeleteAgentsMD(); err != nil {
		t.Fatalf("DeleteAgentsMD 应幂等: %v", err)
	}

	// 快照:开关决定 AgentsMD 是否带出内容
	if err := store.PutAgentsMD("人格"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConfig(Config{EnableAgentsMD: true, EnableSkills: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.9}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Snapshot()
	if err != nil || snap.AgentsMD != "人格" {
		t.Fatalf("开启时快照应含 AGENTS.md: %q err=%v", snap.AgentsMD, err)
	}
	if _, err := store.PutConfig(Config{EnableAgentsMD: false, EnableSkills: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.9}); err != nil {
		t.Fatal(err)
	}
	if snap, err = store.Snapshot(); err != nil || snap.AgentsMD != "" {
		t.Fatalf("关闭时快照不应含 AGENTS.md: %q err=%v", snap.AgentsMD, err)
	}
}

// ---- 配置 ----

func TestConfigDefaultsAndValidation(t *testing.T) {
	store := newTestStore(t)
	cfg, err := store.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Fatalf("无文件时应返回默认配置: %+v", cfg)
	}
	if !cfg.EnableAgentsMD || !cfg.EnableSkills || cfg.CompactTriggerRatio != 0.9 || len(cfg.DisabledSkills) != 0 {
		t.Fatalf("默认值不符: %+v", cfg)
	}

	for _, ratio := range []float64{0.5, 0.95} {
		if _, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, CompactTriggerRatio: ratio}); err != nil {
			t.Fatalf("ratio %v 应合法: %v", ratio, err)
		}
	}
	for _, ratio := range []float64{0.4, 0.96} {
		_, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, CompactTriggerRatio: ratio})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "compact_trigger_ratio") {
			t.Fatalf("ratio %v 应拒绝并说明字段: %v", ratio, err)
		}
	}
	for _, window := range []int{0, 4096, 10_000_000} {
		if _, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, CompactTriggerRatio: 0.9, ContextWindow: window}); err != nil {
			t.Fatalf("context_window %d 应合法: %v", window, err)
		}
	}
	for _, window := range []int{100, 4095, 10_000_001} {
		_, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, CompactTriggerRatio: 0.9, ContextWindow: window})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "context_window") {
			t.Fatalf("context_window %d 应拒绝并说明字段: %v", window, err)
		}
	}
	for _, disabled := range [][]string{{"../evil"}, {"Bad_Name"}, {""}} {
		_, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, DisabledSkills: disabled, CompactTriggerRatio: 0.9})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "disabled_skills") {
			t.Fatalf("disabled_skills %q 应拒绝并说明字段: %v", disabled, err)
		}
	}

	// 完整往返
	full := Config{EnableAgentsMD: false, EnableSkills: true, DisabledSkills: []string{"zz"}, CompactTriggerRatio: 0.7, ContextWindow: 128000}
	saved, err := store.PutConfig(full)
	if err != nil || !reflect.DeepEqual(saved, full) {
		t.Fatalf("往返不符: %+v err=%v", saved, err)
	}
	got, err := store.GetConfig()
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("落盘读取不符: %+v err=%v", got, err)
	}
}

func TestConfigCorruptedFallback(t *testing.T) {
	store := newTestStore(t)
	configPath := filepath.Join(store.root, configFileName)

	for _, raw := range []string{"{oops", `{"enable_skills": "yes"}`} {
		if err := os.WriteFile(configPath, []byte(raw), filePerm); err != nil {
			t.Fatal(err)
		}
		cfg, err := store.GetConfig()
		if err != nil {
			t.Fatalf("损坏配置不应报错: %v", err)
		}
		if !reflect.DeepEqual(cfg, DefaultConfig()) {
			t.Fatalf("损坏配置应回退默认: %+v", cfg)
		}
		data, err := os.ReadFile(configPath)
		if err != nil || string(data) != raw {
			t.Fatalf("损坏文件应保持原样: %q err=%v", data, err)
		}
	}

	// 文件里缺省的字段保持默认
	if err := os.WriteFile(configPath, []byte(`{"enable_skills": false}`), filePerm); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig()
	if err != nil || cfg.EnableSkills || !cfg.EnableAgentsMD || cfg.CompactTriggerRatio != 0.9 {
		t.Fatalf("缺省字段应取默认: %+v err=%v", cfg, err)
	}
}

// ---- 快照的上下文参数 ----

func TestSnapshotContextDefaults(t *testing.T) {
	store := newTestStore(t)
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snap.EnableSkills || !snap.EnableAgentsMD || snap.ContextWindow != 200000 || snap.CompactTriggerRatio != 0.9 {
		t.Fatalf("默认快照不符: %+v", snap)
	}
	if len(snap.SkillDirs) != 0 || snap.AgentsMD != "" {
		t.Fatalf("空存储快照应无技能无人格: %+v", snap)
	}
	if _, err := store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.6, ContextWindow: 500000}); err != nil {
		t.Fatal(err)
	}
	if snap, err = store.Snapshot(); err != nil || snap.ContextWindow != 500000 || snap.CompactTriggerRatio != 0.6 {
		t.Fatalf("自定义上下文参数不符: %+v err=%v", snap, err)
	}
}

// ---- 读取截断与资源清单 ----

func TestReadSkillFileTruncation(t *testing.T) {
	store := newTestStore(t)
	mustPutSkill(t, store, "big", "大文件技能。")

	// 截断点落在多字节字符中间时退回到 UTF-8 边界
	boundary := fileReadMaxBytes - 2
	writeSkillFile(t, store, "big", "data.txt", strings.Repeat("a", boundary)+"中文")
	content, truncated, err := store.ReadSkillFile("big", "data.txt")
	if err != nil || !truncated {
		t.Fatalf("超限应截断: truncated=%v err=%v", truncated, err)
	}
	if len(content) != boundary || content != strings.Repeat("a", boundary) {
		t.Fatalf("应退回到 rune 边界: len=%d", len(content))
	}

	exact := strings.Repeat("a", fileReadMaxBytes)
	writeSkillFile(t, store, "big", "data.txt", exact)
	if content, truncated, err = store.ReadSkillFile("big", "data.txt"); err != nil || truncated || content != exact {
		t.Fatalf("恰好 256KB 不应截断: truncated=%v err=%v", truncated, err)
	}
}

func TestResourcesOrderAndCap(t *testing.T) {
	store := newTestStore(t)
	mustPutSkill(t, store, "many", "很多资源的技能。")
	for i := 0; i < resourceListMax+5; i++ {
		writeSkillFile(t, store, "many", fmt.Sprintf("f%03d.txt", i), "x")
	}
	writeSkillFile(t, store, "many", "nested/deep/file.md", "x")
	// 点开头条目(文件与目录)不进资源清单
	writeSkillFile(t, store, "many", ".hidden.txt", "x")
	writeSkillFile(t, store, "many", ".hiddendir/x.txt", "x")
	detail, err := store.GetSkill("many")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if len(detail.Resources) != resourceListMax {
		t.Fatalf("资源清单最多 %d 个: %d", resourceListMax, len(detail.Resources))
	}
	for _, r := range detail.Resources {
		if strings.HasPrefix(r, ".") || strings.HasPrefix(r, ".hiddendir") {
			t.Fatalf("点开头条目不应进入资源清单: %q", r)
		}
	}
	sorted := append([]string(nil), detail.Resources...)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] > sorted[i] {
			t.Fatalf("资源清单应按序: %+v", sorted)
		}
	}
	if detail.Resources[0] != "f000.txt" {
		t.Fatalf("词序靠前的应保留: %+v", detail.Resources[:3])
	}
}

// ---- 并发写(-race) ----

func TestConcurrentWrites(t *testing.T) {
	store := newTestStore(t)
	variants := []string{
		skillMD("shared", "版本一。"),
		skillMD("shared", "版本二。"),
		skillMD("shared", "版本三。"),
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store.PutSkill("shared", variants[i%len(variants)])
			store.PutConfig(Config{EnableSkills: true, EnableAgentsMD: true, DisabledSkills: []string{}, CompactTriggerRatio: 0.9})
			store.ListSkills()
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mustName := fmt.Sprintf("extra-%02d", i)
			store.PutSkill(mustName, skillMD(mustName, "并发技能。"))
		}(i)
	}
	wg.Wait()

	detail, err := store.GetSkill("shared")
	if err != nil {
		t.Fatalf("并发写入后 shared 应有效: %v", err)
	}
	found := false
	for _, v := range variants {
		if detail.Content == v {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("shared 内容应是某个完整版本: %q", detail.Content)
	}
	if _, err := store.GetConfig(); err != nil {
		t.Fatalf("并发写入后配置应可读: %v", err)
	}
	info, err := store.ListSkills()
	if err != nil || len(info.Skills) != 9 {
		t.Fatalf("应含 shared 与 8 个并发技能: %d err=%v", len(info.Skills), err)
	}
}

// TestFixPermissions:目录树统一 0755/0644,符号链接不跟随,不存在的 skills
// 忽略,已是目标权限的不重复计数。
func TestFixPermissions(t *testing.T) {
	store := newTestStore(t)
	if n, err := store.FixPermissions(); err != nil || n != 0 {
		t.Fatalf("skills 不存在时应忽略: n=%d err=%v", n, err)
	}

	mustPutSkill(t, store, "pdf-tools", "PDF 技能。")
	skills := filepath.Join(store.root, skillsDirName)
	skillDir := filepath.Join(skills, "pdf-tools")
	res := filepath.Join(skillDir, "res")
	if err := os.Mkdir(res, 0o755); err != nil {
		t.Fatal(err)
	}
	resFile := filepath.Join(res, "data.txt")
	if err := os.WriteFile(resFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// 模拟旧部署的过严权限:全部去掉其他/组位
	for _, p := range []string{skills, skillDir, res} {
		if err := os.Chmod(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	skillMDPath := filepath.Join(skillDir, skillFileName)
	if err := os.Chmod(skillMDPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// 链接指向外部文件:不应被跟随,目标权限保持
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillDir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	changed, err := store.FixPermissions()
	if err != nil {
		t.Fatal(err)
	}
	if changed != 5 { // skills、技能目录、res、SKILL.md、data.txt
		t.Fatalf("修改个数 = %d, want 5", changed)
	}
	for _, p := range []struct {
		path string
		want os.FileMode
	}{
		{skills, skillDirPerm},
		{skillDir, skillDirPerm},
		{res, skillDirPerm},
		{skillMDPath, skillFilePerm},
		{resFile, skillFilePerm},
	} {
		info, err := os.Lstat(p.path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != p.want {
			t.Fatalf("%s 权限 = %o, want %o", p.path, perm, p.want)
		}
	}
	if info, err := os.Lstat(outside); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("符号链接目标不应被改动: %v", err)
	}

	// 幂等:再次执行零修改
	if n, err := store.FixPermissions(); err != nil || n != 0 {
		t.Fatalf("幂等执行: n=%d err=%v", n, err)
	}
}
