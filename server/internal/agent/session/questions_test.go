// question 工具的会话数据层测试:转录条目、引用渲染、fork 复制、
// AddQuestion 去重与裁剪、AnswerQuestion 的 ErrNotFound。
package session

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func questionFixture(id, toolCallID, afterEntryID string, answered bool) Question {
	q := Question{
		QuestionID:   id,
		ToolCallID:   toolCallID,
		AfterEntryID: afterEntryID,
		Questions: []QuestionItem{{
			Header: "方案", Question: "选哪个方案?", MultiSelect: true,
			Options: []QuestionOption{{Label: "A 方案", Description: "首选"}, {Label: "B 方案"}},
		}},
		AskedAt: fixedTime,
	}
	if answered {
		at := fixedTime.Add(time.Minute)
		q.Answers = []QuestionAnswer{{Selected: []string{"A 方案", OtherOptionMarker}, Other: "补充说明"}}
		q.AnsweredAt = &at
	}
	return q
}

// 转录:已回答 / 跳过 / 取消 / 未回答各形态,以及锚点规则与审批一致。
func TestQuestionTranscript(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")

	skipped := questionFixture("q-skip", "call_s", "a001", true)
	skipped.Skipped = true
	skipped.Answers = nil
	cancelled := questionFixture("q-cancel", "call_c", "a002", true)
	cancelled.Cancelled = true
	cancelled.Answers = nil
	auto := questionFixture("q-auto", "call_a", "a002", true)
	auto.Auto = true
	answered := questionFixture("q-ans", "call_1", "r002", true)
	unanswered := questionFixture("q-open", "call_2", "a003", false)
	orphan := questionFixture("q-orphan", "call_3", "no-such", true)
	noAnchor := questionFixture("q-tail", "call_4", "", true)

	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "toolssess01"
		m.Questions = []Question{skipped, cancelled, auto, answered, unanswered, orphan, noAnchor}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	var got []map[string]any
	for _, entry := range entries {
		if entryType(entry) == "question" {
			got = append(got, entry)
		}
	}
	// orphan 的锚点不在当前分支:丢弃;其余 6 条按锚点顺序出现
	if len(got) != 6 {
		t.Fatalf("question 条目数 = %d, want 6(旧分支锚点丢弃): %+v", len(got), got)
	}
	byID := map[string]map[string]any{}
	for _, entry := range got {
		byID[entry["question_id"].(string)] = entry
	}
	// 未回答:没有 answers/skipped/cancelled/answered_at
	open := byID["q-open"]
	if _, has := open["answers"]; has {
		t.Fatalf("未回答不应有 answers: %+v", open)
	}
	if _, has := open["skipped"]; has {
		t.Fatalf("未回答不应有 skipped: %+v", open)
	}
	// 已回答:answers 形态 + auto 标记
	ans := byID["q-ans"]
	answer := asMaps(ans["answers"])[0]
	if answer["other"] != "补充说明" {
		t.Fatalf("answers = %+v", ans["answers"])
	}
	if _, has := ans["auto"]; has {
		t.Fatalf("非 auto 不应输出 auto: %+v", ans)
	}
	if byID["q-auto"]["auto"] != true {
		t.Fatalf("auto 应输出: %+v", byID["q-auto"])
	}
	// 跳过/取消:布尔字段输出
	if byID["q-skip"]["skipped"] != true {
		t.Fatalf("skipped 应输出: %+v", byID["q-skip"])
	}
	if byID["q-cancel"]["cancelled"] != true {
		t.Fatalf("cancelled 应输出: %+v", byID["q-cancel"])
	}
	// 时间戳与 questions 形态
	if ans["timestamp"] != "2026-10-29T08:00:00.000Z" {
		t.Fatalf("timestamp = %v", ans["timestamp"])
	}
	item := asMaps(ans["questions"])[0]
	if item["header"] != "方案" || item["multiSelect"] != true {
		t.Fatalf("questions = %+v", ans["questions"])
	}
	option := asMaps(item["options"])[0]
	if option["label"] != "A 方案" || option["description"] != "首选" {
		t.Fatalf("options = %+v", item["options"])
	}
	// 位置:q-ans 锚点是 r002(call_2 的 toolResult),应紧跟其后
	var ansIndex int
	for i, entry := range entries {
		if entry["question_id"] == "q-ans" {
			ansIndex = i
		}
	}
	prev := asMap(entries[ansIndex-1]["message"])
	if prev["tool_call_id"] != "call_2" {
		t.Fatalf("q-ans 前一条应为 r002: %+v", prev)
	}
	// 空锚点(q-tail)应追加到末尾
	if entries[len(entries)-1]["question_id"] != "q-tail" {
		t.Fatalf("空锚点应在末尾: %+v", entries[len(entries)-1])
	}
}

// 引用渲染:每题一行,摘要含顿号/其他/跳过/取消;未回答的提问不渲染。
func TestQuestionRenderReference(t *testing.T) {
	store := newTestStore(t)
	meta := refSession(t, store, "refq000001", []string{
		`{"type":"session","version":3,"id":"refq000001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-29T08:00:00.010Z","message":{"role":"user","content":"做吧"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T08:00:00.020Z","message":{"role":"assistant","content":"好"}}`,
	}, func(m *Meta) {
		m.PiSessionID = "refq000001"
		answered := questionFixture("q-1", "call_1", "u1", true)
		answered.Questions = append(answered.Questions, QuestionItem{
			Header: "范围", Question: "改哪些?", MultiSelect: false,
			Options: []QuestionOption{{Label: "仅后端"}, {Label: "前后端"}},
		})
		answered.Answers = []QuestionAnswer{
			{Selected: []string{"A 方案", OtherOptionMarker}, Other: "补充说明"},
			{Selected: []string{"仅后端"}},
		}
		skipped := questionFixture("q-2", "call_2", "u1", true)
		skipped.Skipped = true
		cancelled := questionFixture("q-3", "call_3", "", true)
		cancelled.Cancelled = true
		unanswered := questionFixture("q-4", "call_4", "u1", false)
		m.Questions = []Question{answered, skipped, cancelled, unanswered}
	})
	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	for _, want := range []string{
		"[提问] 方案:选哪个方案? → A 方案、其他:补充说明",
		"[提问] 范围:改哪些? → 仅后端",
		"[提问] 方案:选哪个方案? → 已跳过",
		"[提问] 方案:选哪个方案? → 已取消",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "[提问]") != 4 {
		t.Fatalf("未回答的提问不应渲染:\n%s", out)
	}
}

// 长回答受单行上限约束,整体渲染仍受会话级预算约束(既有截断逻辑不破坏)。
func TestQuestionRenderReferenceTruncation(t *testing.T) {
	store := newTestStore(t)
	long := strings.Repeat("长", 500)
	meta := refSession(t, store, "refq000002", []string{
		`{"type":"session","version":3,"id":"refq000002","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-29T08:00:00.010Z","message":{"role":"user","content":"做"}}`,
	}, func(m *Meta) {
		m.PiSessionID = "refq000002"
		q := questionFixture("q-1", "call_1", "u1", true)
		q.Answers = []QuestionAnswer{{Selected: []string{long}, Other: long}}
		m.Questions = []Question{q}
	})
	out, err := store.RenderReference(meta.ID, RenderReferenceOptions{})
	if err != nil {
		t.Fatalf("RenderReference: %v", err)
	}
	if !strings.Contains(out, truncationMarker) {
		t.Fatalf("长回答应被截断:\n%.300s", out)
	}
}

// fork 只复制已回答(含跳过/取消)且锚点落在被复制条目内的记录。
func TestQuestionFork(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "toolssess01"
		m.Messages = []UserMessage{{MessageID: "m-1", EntryID: "u001", Text: "列出目录"}}
		m.Questions = []Question{
			questionFixture("q-keep", "call_1", "r001", true),   // 已回答且在分支上
			questionFixture("q-open", "call_2", "a003", false),  // 未回答:丢弃
			questionFixture("q-branch", "call_3", "a003", true), // 锚点在全量复制范围内:保留
		}
	})
	forked, err := store.Fork(meta.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(forked.Questions) != 2 {
		t.Fatalf("fork 后提问数 = %d, want 2: %+v", len(forked.Questions), forked.Questions)
	}
	for _, q := range forked.Questions {
		if q.QuestionID == "q-open" {
			t.Fatalf("未回答的提问不应复制: %+v", forked.Questions)
		}
	}

	// 定点 fork(到 m-1 那轮为止):u001 之后到 a003 都属同一轮,a003 也在
	// 复制范围内;旧分支丢弃另用 branch.jsonl 验证(见下)。
	partial, err := store.Fork(meta.ID, "m-1")
	if err != nil {
		t.Fatalf("Fork(部分): %v", err)
	}
	if len(partial.Questions) != 2 {
		t.Fatalf("部分 fork 提问 = %+v", partial.Questions)
	}

	// 旧分支上的提问:branch.jsonl 当前分支是 u201→a201→u203→a203,
	// 锚点 u202 在旧分支上,应丢弃。
	branchMeta := mustCreate(t, store)
	writePiFile(t, store, branchMeta.ID, "branch.jsonl", "branchess001")
	updateMeta(t, store, branchMeta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.Questions = []Question{
			questionFixture("q-on", "call_1", "u201", true),
			questionFixture("q-off", "call_2", "u202", true),
		}
	})
	branchFork, err := store.Fork(branchMeta.ID, "")
	if err != nil {
		t.Fatalf("Fork(branch): %v", err)
	}
	if len(branchFork.Questions) != 1 || branchFork.Questions[0].QuestionID != "q-on" {
		t.Fatalf("旧分支提问应丢弃: %+v", branchFork.Questions)
	}
}

func TestAddQuestionDedupAndCap(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)

	q := questionFixture("q-1", "call_1", "e1", true)
	if err := store.AddQuestion(meta.ID, q); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	// 同 QuestionID 重复:原位替换(重放去重)
	q.ToolCallID = "call_1b"
	if err := store.AddQuestion(meta.ID, q); err != nil {
		t.Fatalf("AddQuestion(重复): %v", err)
	}
	got, err := store.Get(meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Questions) != 1 || got.Questions[0].ToolCallID != "call_1b" {
		t.Fatalf("去重失败: %+v", got.Questions)
	}

	// 上限:超过 MaxQuestions 丢最旧
	for i := 0; i < MaxQuestions+5; i++ {
		if err := store.AddQuestion(meta.ID, questionFixture("q-many-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "c", "e", false)); err != nil {
			t.Fatalf("AddQuestion(%d): %v", i, err)
		}
	}
	got, err = store.Get(meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Questions) != MaxQuestions {
		t.Fatalf("裁剪后 = %d, want %d", len(got.Questions), MaxQuestions)
	}
	for _, q := range got.Questions {
		if q.QuestionID == "q-1" {
			t.Fatalf("最旧的 q-1 应被丢弃")
		}
	}
}

func TestAnswerQuestionNotFound(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	if err := store.AddQuestion(meta.ID, questionFixture("q-1", "call_1", "e1", false)); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	err := store.AnswerQuestion(meta.ID, "q-no-such", nil, false, false, false, fixedTime)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AnswerQuestion(不存在) = %v, want ErrNotFound", err)
	}
	// 正常回答:补齐答案与时间
	if err := store.AnswerQuestion(meta.ID, "q-1",
		[]QuestionAnswer{{Selected: []string{"A 方案"}}}, false, false, false, fixedTime); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	got, err := store.Get(meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	q := got.Questions[0]
	if q.AnsweredAt == nil || len(q.Answers) != 1 || q.Answers[0].Selected[0] != "A 方案" {
		t.Fatalf("回答后记录 = %+v", q)
	}
	// 会话不存在:ErrNotFound
	if err := store.AnswerQuestion("00000000-0000-0000-0000-000000000000", "q-1", nil, false, false, false, fixedTime); !errors.Is(err, ErrNotFound) {
		t.Fatalf("会话不存在 = %v", err)
	}
}
