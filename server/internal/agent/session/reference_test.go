// 引用渲染测试:行格式、工具输出截断、最近优先、meta 原文优先、多引用分配。
package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// refSession 建立一个带 Pi 会话文件与消息绑定的被引用会话,返回会话 meta。
func refSession(t *testing.T, store *Store, piID string, lines []string, meta func(*Meta)) Meta {
	t.Helper()
	meta0 := mustCreate(t, store)
	if lines != nil {
		writeLines(t, store, meta0.ID, piID, lines...)
	}
	if meta != nil {
		meta0 = updateMeta(t, store, meta0.ID, meta)
	}
	return meta0
}

func TestRenderReferenceBasic(t *testing.T) {
	store := newTestStore(t)
	meta := refSession(t, store, "refsess0001", []string{
		`{"type":"session","version":3,"id":"refsess0001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		`{"type":"model_change","id":"g1","parentId":null,"timestamp":"2026-10-29T08:00:00.001Z","provider":"xlyra","modelId":"mock-1"}`,
		`{"type":"message","id":"s1","parentId":"g1","timestamp":"2026-10-29T08:00:00.002Z","message":{"role":"system","content":"","sections":{"preamble":"系统前缀"}}`,
		`{"type":"usage","id":"us1","parentId":"s1","timestamp":"2026-10-29T08:00:00.003Z","kind":"turn","usage":{"input":1}}`,
		`{"type":"message","id":"u1","parentId":"s1","timestamp":"2026-10-29T08:00:00.010Z","message":{"role":"user","content":[{"type":"text","text":"Pi 文件里的原文(可能拼了引用块)"},{"type":"image","mimeType":"image/png","data":"aGk="}],"timestamp":1}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T08:00:00.020Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"思考不该出现"},{"type":"text","text":"我来执行"},{"type":"toolCall","id":"call_1","name":"write","arguments":{"path":"hello.txt","content":"hi"}}]}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-10-29T08:00:00.030Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"write","content":[{"type":"text","text":"写入成功"}],"isError":false}}`,
		`{"type":"message","id":"a2","parentId":"r1","timestamp":"2026-10-29T08:00:00.040Z","message":{"role":"assistant","content":[{"type":"text","text":"完成"}]}}`,
	}, func(m *Meta) {
		m.Title = "被引用的会话"
		m.PiSessionID = "refsess0001"
		// 用户展示文本取 meta 原文,而非 Pi 文件里的原文
		m.Messages = []UserMessage{{MessageID: "m-1", EntryID: "u1", Text: "帮我写 hello.txt"}}
	})

	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	if !strings.HasPrefix(out, `<referenced_session id="`+meta.ID+`" title="被引用的会话">`) {
		t.Fatalf("开头不符: %q", out)
	}
	if !strings.HasSuffix(out, "</referenced_session>") {
		t.Fatalf("结尾不符: %q", out)
	}
	for _, want := range []string{
		"[用户] 帮我写 hello.txt\n[图片]",
		"[助手] 我来执行",
		"[工具 write hello.txt] 写入成功",
		"[助手] 完成",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"系统前缀", "思考不该出现", "Pi 文件里的原文", "model_change"} {
		if strings.Contains(out, banned) {
			t.Fatalf("不应包含 %q:\n%s", banned, out)
		}
	}
}

// 工具输出与 compaction 摘要超过 400 字时截断并加标记。
func TestRenderReferenceToolTruncation(t *testing.T) {
	store := newTestStore(t)
	long := strings.Repeat("甲", 500)
	longSummary := strings.Repeat("乙", 500)
	meta := refSession(t, store, "reflong0001", []string{
		`{"type":"session","version":3,"id":"reflong0001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		piLine("user", "u1", "", "看输出"),
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T08:00:00.020Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"cat big.txt"}}]}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-10-29T08:00:00.030Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"bash","content":[{"type":"text","text":"` + long + `"}]}}`,
		`{"type":"compaction","id":"cp1","parentId":"r1","timestamp":"2026-10-29T08:00:00.040Z","summary":"` + longSummary + `","tokensBefore":10}`,
	}, func(m *Meta) { m.PiSessionID = "reflong0001" })

	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	toolLine := "[工具 bash cat big.txt] " + strings.Repeat("甲", 400) + "…[已截断]"
	if !strings.Contains(out, toolLine) {
		t.Fatalf("工具行截断不符:\n%s", out)
	}
	if !strings.Contains(out, "[摘要] "+strings.Repeat("乙", 400)+"…[已截断]") {
		t.Fatalf("摘要行截断不符:\n%s", out)
	}
}

// 超出上限时优先保留最新消息:最早的行被丢弃,顶部加省略提示,总长不超限。
func TestRenderReferenceKeepsRecent(t *testing.T) {
	store := newTestStore(t)
	var lines []string
	lines = append(lines, `{"type":"session","version":3,"id":"refmany0001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`)
	const turns = 30
	parent := ""
	for i := 0; i < turns; i++ {
		user := piLine("user", fmt.Sprintf("u%02d", i), parent, fmt.Sprintf("第%02d轮问题-%s", i, strings.Repeat("问", 10)))
		parent = fmt.Sprintf("u%02d", i)
		lines = append(lines, user,
			piLine("assistant", fmt.Sprintf("v%02d", i), parent, fmt.Sprintf("第%02d轮回答-%s", i, strings.Repeat("答", 10))))
		parent = fmt.Sprintf("v%02d", i)
	}
	meta := refSession(t, store, "refmany0001", lines, func(m *Meta) { m.PiSessionID = "refmany0001" })

	const limit = 200
	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{MaxChars: limit})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	if got := utf8.RuneCountInString(out); got > limit {
		t.Fatalf("渲染结果 %d 字符,超过上限 %d", got, limit)
	}
	if !strings.Contains(out, "[已省略更早的 ") || !strings.Contains(out, " 条消息]") {
		t.Fatalf("缺少省略提示:\n%s", out)
	}
	if !strings.Contains(out, "第29轮回答") || !strings.Contains(out, "第29轮问题") {
		t.Fatalf("最新的消息应保留:\n%s", out)
	}
	if strings.Contains(out, "第01轮") || strings.Contains(out, "第00轮") {
		t.Fatalf("最早的消息应被丢弃:\n%s", out)
	}
	// 省略提示在最顶部(紧跟引用标签之后)
	firstLine := strings.Split(out, "\n")[1]
	if !strings.HasPrefix(firstLine, "[已省略更早的 ") {
		t.Fatalf("省略提示应在正文首行: %q", firstLine)
	}
}

// 单条消息超过整个上限:保留被截断的最新一条,不报错。
func TestRenderReferenceSingleHugeLine(t *testing.T) {
	store := newTestStore(t)
	meta := refSession(t, store, "refhuge0001", []string{
		`{"type":"session","version":3,"id":"refhuge0001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		piLine("user", "u1", "", strings.Repeat("长", 300)),
	}, func(m *Meta) { m.PiSessionID = "refhuge0001" })
	const limit = 100
	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{MaxChars: limit})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	if got := utf8.RuneCountInString(out); got > limit {
		t.Fatalf("渲染结果 %d 字符,超过 %d", got, limit)
	}
}

// 标题回退:meta.Title 为空时取第一条用户消息截取;被引用会话不存在或非法
// id 报错;没有 Pi 文件时输出空块。
func TestRenderReferenceEdges(t *testing.T) {
	store := newTestStore(t)
	meta := refSession(t, store, "reftitle001", []string{
		`{"type":"session","version":3,"id":"reftitle001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		piLine("user", "u1", "", "第一句话作为标题"),
		piLine("assistant", "a1", "u1", "回答"),
	}, func(m *Meta) {
		m.PiSessionID = "reftitle001"
		m.Messages = []UserMessage{{MessageID: "m-1", EntryID: "u1", Text: "第一句话作为标题"}}
	})
	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `title="第一句话作为标题"`) {
		t.Fatalf("标题回退不符: %q", out)
	}

	if _, err := store.RenderReference("00000000-0000-4000-8000-00000000dead", RenderReferenceOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在会话: err = %v", err)
	}
	if _, err := store.RenderReference("../x", RenderReferenceOptions{}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("非法 id: err = %v", err)
	}

	empty := refSession(t, store, "", nil, nil)
	out, err = store.RenderReference(empty.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatalf("没有 Pi 文件应渲染空块: %v", err)
	}
	if out != "<referenced_session id=\""+empty.ID+"\" title=\"新会话\">\n\n</referenced_session>" {
		t.Fatalf("空块不符: %q", out)
	}
}

// RenderReferences:多个引用按顺序平均分配总预算,每个块不超过份额。
func TestRenderReferencesBudget(t *testing.T) {
	store := newTestStore(t)
	build := func(piID, tag string, turns int) Meta {
		var lines []string
		lines = append(lines, `{"type":"session","version":3,"id":"`+piID+`","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`)
		for i := 0; i < turns; i++ {
			lines = append(lines,
				piLine("user", piID+"u"+tag, "", tag+" 问题"+strings.Repeat("问", 30)),
				piLine("assistant", piID+"a"+tag, piID+"u"+tag, tag+" 回答"+strings.Repeat("答", 30)),
			)
		}
		return refSession(t, store, piID, lines, func(m *Meta) { m.PiSessionID = piID })
	}
	a := build("refaaa0001", "A", 40)
	b := build("refbbb0001", "B", 40)

	out, err := store.RenderReferences([]string{a.ID, b.ID})
	if err != nil {
		t.Fatalf("RenderReferences: %v", err)
	}
	blocks := strings.Split(out, "\n")
	if len(blocks) < 2 {
		t.Fatalf("应有两个引用块: %q", out)
	}
	// 每块不超过平均份额 30000,且都含最新内容
	if !strings.Contains(out, "A 回答") || !strings.Contains(out, "B 回答") {
		t.Fatalf("两个引用的最新内容都应保留:\n%s", out)
	}
	share := referenceTotalMaxRunes / 2
	for i, id := range []string{a.ID, b.ID} {
		block := extractBlock(out, id)
		if got := utf8.RuneCountInString(block); got > share {
			t.Fatalf("引用 %d 超过份额 %d: %d", i, share, got)
		}
	}
	if _, err := store.RenderReferences(nil); err != nil {
		t.Fatalf("空列表: %v", err)
	}

	// 引用不存在:报错
	if _, err := store.RenderReferences([]string{a.ID, "00000000-0000-4000-8000-00000000dead"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失引用: err = %v", err)
	}
}

func extractBlock(text, id string) string {
	open := `<referenced_session id="` + id + `"`
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	end := strings.Index(text[start:], "</referenced_session>")
	if end < 0 {
		return ""
	}
	return text[start : start+end+len("</referenced_session>")]
}

// transcript 的用户消息展示文本优先取 meta 原文(绑定存在时):Pi 文件里的
// 用户条目可能拼有引用块或附件正文;无绑定时回退 Pi 原文。
func TestTranscriptUserTextPrefersMeta(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writeLines(t, store, meta.ID, "metatext01",
		`{"type":"session","version":3,"id":"metatext01","timestamp":"2026-10-29T08:00:00.000Z","cwd":"w"}`,
		piLine("user", "ub1", "", "拼进提示的原文+引用块"),
		piLine("assistant", "ua1", "ub1", "回答"),
		piLine("user", "ub2", "ua1", "未绑定的用户消息"),
	)
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "metatext01"
		m.Messages = []UserMessage{{
			MessageID: "m-1", EntryID: "ub1", Text: "用户原话",
			References: []Reference{{SessionID: "00000000-0000-4000-8000-0000000000aa", Title: "引用的会话"}},
		}}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d, want 3", len(entries))
	}
	first := asMap(entries[0]["message"])
	if first["content"] != "用户原话" {
		t.Fatalf("绑定消息应显示 meta 原文: %v", first["content"])
	}
	refs, ok := first["references"].([]map[string]any)
	if !ok || len(refs) != 1 || refs[0]["session_id"] != "00000000-0000-4000-8000-0000000000aa" || refs[0]["title"] != "引用的会话" {
		t.Fatalf("references 字段不符: %+v", first["references"])
	}
	last := asMap(entries[2]["message"])
	if last["content"] != "未绑定的用户消息" {
		t.Fatalf("未绑定消息应回退 Pi 原文: %v", last["content"])
	}
	if _, has := last["references"]; has {
		t.Fatalf("未绑定消息不应有 references: %+v", last)
	}
}

// DisplayTitle:无标题时回退到第一条用户消息截取,与列表页规则一致。
func TestDisplayTitle(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	if got := meta.DisplayTitle(); got != "新会话" {
		t.Fatalf("空会话标题 = %q", got)
	}
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Title = "已设标题"
		m.Messages = []UserMessage{{Text: "第一句话"}}
	})
	if got := (Meta{Title: "已设标题", Messages: []UserMessage{{Text: "第一句话"}}}).DisplayTitle(); got != "已设标题" {
		t.Fatalf("已设标题 = %q", got)
	}
	if got := (Meta{Messages: []UserMessage{{Text: "第一句话"}}}).DisplayTitle(); got != "第一句话" {
		t.Fatalf("回退标题 = %q", got)
	}
}
