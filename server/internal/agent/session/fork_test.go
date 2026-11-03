// fork 相关测试:整会话分叉、按消息分叉、文件头改写、meta 迁移、出错回滚。
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// forkedEntry 把新会话文件解析成(文件头, 条目列表)。
func forkedEntry(t *testing.T, store *Store, meta Meta) (map[string]any, []piEntry, [][]byte) {
	t.Helper()
	file, err := PiSessionFile(store.PiDir(meta.ID), meta.PiSessionID)
	if err != nil {
		t.Fatalf("按新 Pi 会话 id 定位文件失败: %v", err)
	}
	header, rawLines, entries, err := readPiFileRaw(file)
	if err != nil {
		t.Fatalf("读取分叉文件: %v", err)
	}
	var headerFields map[string]any
	if len(header) > 0 {
		if err := json.Unmarshal(header, &headerFields); err != nil {
			t.Fatalf("解析文件头: %v", err)
		}
	}
	return headerFields, entries, rawLines
}

func entryIDs(entries []piEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// 分叉 branch.jsonl(当前分支 u201→a201→u203→a203,旧分支 u202/a202):
// 整会话分叉只复制当前分支,文件头改写,meta 迁移正确,审批卡片与用户
// 原文在 BuildTranscript(新会话) 里与源一致。
func TestForkWholeSession(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	granted := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Title = "源会话标题"
		m.PiSessionID = "branchess001"
		m.Model = "xlyra/mock-9"
		m.PermissionMode = "full"
		m.Slot = 7
		m.SessionAllow = []string{"write"}
		m.Messages = []UserMessage{
			{MessageID: "m-1", EntryID: "u201", Text: "主干第一问(原文)", CreatedAt: fixedTime},
			{MessageID: "m-2", EntryID: "u203", Text: "改走另一分支的第二问", CreatedAt: fixedTime},
		}
		m.Escalations = []Escalation{
			{EscalationID: "esc-keep", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a201", Scope: "session"},
			{EscalationID: "esc-old", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a202"}, // 旧分支:不复制
			{EscalationID: "esc-tail", ToolName: "bash", Granted: nil, At: fixedTime},                            // 空锚点:不复制
		}
		m.RunErrors = []RunError{{RunID: "r1", Message: "失败", AfterEntryID: "a203", At: fixedTime}}
	})

	forked, err := store.Fork(meta.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.ID == meta.ID || !validID(forked.ID) {
		t.Fatalf("新会话 id = %q", forked.ID)
	}
	if forked.Title != "分叉 · 源会话标题" {
		t.Fatalf("Title = %q", forked.Title)
	}
	if forked.Model != "xlyra/mock-9" || forked.PermissionMode != "full" {
		t.Fatalf("Model/PermissionMode = %s/%s", forked.Model, forked.PermissionMode)
	}
	if forked.Slot != 0 || len(forked.SessionAllow) != 0 || len(forked.RunErrors) != 0 {
		t.Fatalf("Slot/SessionAllow/RunErrors 不应复制: %+v", forked)
	}
	if len(forked.Messages) != 2 || forked.Messages[0].MessageID != "m-1" || forked.Messages[1].EntryID != "u203" {
		t.Fatalf("Messages 迁移不符: %+v", forked.Messages)
	}
	if forked.Messages[0].Text != "主干第一问(原文)" {
		t.Fatalf("消息原文不符: %+v", forked.Messages[0])
	}
	if len(forked.Escalations) != 1 || forked.Escalations[0].EscalationID != "esc-keep" {
		t.Fatalf("Escalations 迁移不符: %+v", forked.Escalations)
	}
	if forked.Escalations[0].Granted == nil || !*forked.Escalations[0].Granted || forked.Escalations[0].Scope != "session" {
		t.Fatalf("审批记录字段未保持: %+v", forked.Escalations[0])
	}

	// 文件头改写:id/timestamp/cwd 换新,version 保留;条目按分支顺序原样复制
	header, entries, _ := forkedEntry(t, store, forked)
	if header["id"] != forked.PiSessionID {
		t.Fatalf("文件头 id = %v, want 新 Pi 会话 id %s", header["id"], forked.PiSessionID)
	}
	if header["cwd"] != store.WorkDir(forked.ID) {
		t.Fatalf("文件头 cwd = %v, want %s", header["cwd"], store.WorkDir(forked.ID))
	}
	if _, ok := header["timestamp"]; !ok {
		t.Fatal("文件头缺 timestamp")
	}
	if version, _ := header["version"].(float64); version != 3 {
		t.Fatalf("文件头 version = %v, want 3", header["version"])
	}
	if got, want := entryIDs(entries), []string{"b001", "u201", "a201", "u203", "a203"}; !equalStrings(got, want) {
		t.Fatalf("分叉条目 = %v, want %v(旧分支 u202/a202 不应复制)", got, want)
	}
	// 原始 JSON 行保持:与源文件里的对应行完全一致
	sourceLines := readSourceLines(t, store, meta)
	for _, id := range []string{"u201", "a203"} {
		if line, ok := rawLineByIDSource(sourceLines, id); ok {
			_, forkedEntries, forkedRaw := forkedEntry(t, store, forked)
			found := false
			for i, entry := range forkedEntries {
				if entry.ID == id && string(forkedRaw[i]) == line {
					found = true
				}
			}
			if !found {
				t.Fatalf("条目 %s 的原始行未被原样复制", id)
			}
		}
	}
	// parentId 链保持:u203 的父仍是 a201
	for _, entry := range entries {
		if entry.ID == "u203" {
			if entry.ParentID == nil || *entry.ParentID != "a201" {
				t.Fatalf("u203 的 parentId 被改动: %+v", entry.ParentID)
			}
		}
	}

	// BuildTranscript(新) 与源会话对应部分一致:用户原文 + 审批卡片就位
	entriesOut, err := store.BuildTranscript(forked.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	want := []struct {
		kind string
		text string
	}{
		{"message", "主干第一问(原文)"},
		{"message", "主干回答"},
		{"escalation", "esc-keep"},
		{"message", "改走另一分支的第二问"},
		{"message", "新分支回答"},
	}
	if len(entriesOut) != len(want) {
		t.Fatalf("transcript 条目数 = %d, want %d: %+v", len(entriesOut), len(want), entriesOut)
	}
	for i, expect := range want {
		if entryType(entriesOut[i]) != expect.kind {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entriesOut[i]), expect.kind)
		}
		if expect.kind == "escalation" {
			if entriesOut[i]["escalation_id"] != expect.text {
				t.Fatalf("entries[%d] = %+v", i, entriesOut[i])
			}
			if entriesOut[i]["scope"] != "session" {
				t.Fatalf("审批卡片的 scope 未保持: %+v", entriesOut[i])
			}
		} else if got := asMap(entriesOut[i]["message"])["content"]; got != expect.text {
			t.Fatalf("entries[%d].content = %v, want %q", i, got, expect.text)
		}
	}
	// 源会话不受影响
	after, err := store.Get(meta.ID)
	if err != nil || after.PiSessionID != "branchess001" || len(after.Messages) != 2 || after.Slot != 7 || len(after.RunErrors) != 1 {
		t.Fatalf("源会话被改动: %+v %v", after, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readSourceLines(t *testing.T, store *Store, meta Meta) [][]byte {
	t.Helper()
	file, err := PiSessionFile(store.PiDir(meta.ID), meta.PiSessionID)
	if err != nil {
		t.Fatalf("定位源文件: %v", err)
	}
	_, rawLines, _, err := readPiFileRaw(file)
	if err != nil {
		t.Fatalf("读源文件: %v", err)
	}
	return rawLines
}

func rawLineByIDSource(lines [][]byte, id string) (string, bool) {
	for _, line := range lines {
		var entry piEntry
		if json.Unmarshal(line, &entry) == nil && entry.ID == id {
			return string(line), true
		}
	}
	return "", false
}

// 多轮 + 工具调用 + compaction:按消息分叉只复制到该轮结束,含其后的
// assistant/toolResult 条目(与轮内的 compaction),不含下一条用户消息。
func TestForkUpToMessage(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writeLines(t, store, meta.ID, "multifork01",
		`{"type":"session","version":3,"id":"multifork01","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		`{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-10-29T08:00:00.001Z","provider":"xlyra","modelId":"mock-1"}`,
		`{"type":"message","id":"s1","parentId":"m1","timestamp":"2026-10-29T08:00:00.002Z","message":{"role":"system","content":"","sections":{"preamble":"preamble"},"timestamp":1793246400002}}`,
		piLine("user", "u1", "s1", "第一轮问题"),
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-29T08:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"我来写文件"},{"type":"toolCall","id":"call_1","name":"write","arguments":{"path":"hello.txt","content":"hi"}}]}}`,
		`{"type":"message","id":"r1","parentId":"a1","timestamp":"2026-10-29T08:00:03.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"write","content":[{"type":"text","text":"写入成功"}],"isError":false}}`,
		piLine("user", "u2", "r1", "第二轮问题"),
		piLine("assistant", "a2", "u2", "第二轮回答"),
		`{"type":"compaction","id":"cp1","parentId":"a2","timestamp":"2026-10-29T08:00:06.000Z","summary":"两轮之后的摘要","firstKeptEntryId":"u2","tokensBefore":100}`,
		piLine("user", "u3", "cp1", "第三轮问题"),
		piLine("assistant", "a3", "u3", "第三轮回答"),
	)
	granted := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "multifork01"
		m.Messages = []UserMessage{
			{MessageID: "m-1", EntryID: "u1", Text: "第一轮问题"},
			{MessageID: "m-2", EntryID: "u2", Text: "第二轮问题"},
			{MessageID: "m-3", EntryID: "u3", Text: "第三轮问题"},
		}
		m.Escalations = []Escalation{
			{EscalationID: "esc-r1", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "r1"},
			{EscalationID: "esc-a2", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a2"},
		}
	})

	cases := []struct {
		name          string
		messageID     string
		wantIDs       []string
		wantMessages  []string
		wantEscalated []string
	}{
		{
			name: "到第一轮", messageID: "m-1",
			// 该轮含 toolCall 与 toolResult,不含第二轮用户消息
			wantIDs:       []string{"m1", "s1", "u1", "a1", "r1"},
			wantMessages:  []string{"m-1"},
			wantEscalated: []string{"esc-r1"},
		},
		{
			name: "到第二轮", messageID: "m-2",
			// 第二轮之后的 compaction 属于该轮尾部:一并复制
			wantIDs:       []string{"m1", "s1", "u1", "a1", "r1", "u2", "a2", "cp1"},
			wantMessages:  []string{"m-1", "m-2"},
			wantEscalated: []string{"esc-r1", "esc-a2"},
		},
		{
			name: "到第三轮(即全部)", messageID: "m-3",
			wantIDs:       []string{"m1", "s1", "u1", "a1", "r1", "u2", "a2", "cp1", "u3", "a3"},
			wantMessages:  []string{"m-1", "m-2", "m-3"},
			wantEscalated: []string{"esc-r1", "esc-a2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forked, err := store.Fork(meta.ID, tc.messageID)
			if err != nil {
				t.Fatalf("Fork: %v", err)
			}
			_, entries, _ := forkedEntry(t, store, forked)
			if got := entryIDs(entries); !equalStrings(got, tc.wantIDs) {
				t.Fatalf("条目 = %v, want %v", got, tc.wantIDs)
			}
			var messages []string
			for _, message := range forked.Messages {
				messages = append(messages, message.MessageID)
			}
			if !equalStrings(messages, tc.wantMessages) {
				t.Fatalf("消息 = %v, want %v", messages, tc.wantMessages)
			}
			var escalated []string
			for _, esc := range forked.Escalations {
				escalated = append(escalated, esc.EscalationID)
			}
			if !equalStrings(escalated, tc.wantEscalated) {
				t.Fatalf("审批 = %v, want %v", escalated, tc.wantEscalated)
			}
			// 新文件的当前分支就是被复制的内容(叶子为最后一条)
			if leaf := entries[len(entries)-1]; leaf.ID != tc.wantIDs[len(tc.wantIDs)-1] {
				t.Fatalf("分叉文件叶子 = %s", leaf.ID)
			}
		})
	}

	// transcript 与源会话对应部分一致:fork 到第一轮时工具行与审批都在
	forked, err := store.Fork(meta.ID, "m-1")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	entriesOut, err := store.BuildTranscript(forked.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	want := []struct {
		kind string
		text string
	}{
		{"message", "第一轮问题"},
		{"message", "我来写文件"},
		{"message", "写入成功"}, // toolResult:type 仍为 message,role 为 tool
		{"escalation", "esc-r1"},
	}
	if len(entriesOut) != len(want) {
		t.Fatalf("transcript 条目 = %d, want %d: %+v", len(entriesOut), len(want), entriesOut)
	}
	for i, expect := range want {
		if entryType(entriesOut[i]) != expect.kind {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entriesOut[i]), expect.kind)
		}
		if expect.kind == "escalation" && entriesOut[i]["escalation_id"] != expect.text {
			t.Fatalf("entries[%d] = %+v", i, entriesOut[i])
		}
		if expect.kind == "message" {
			if got := asMap(entriesOut[i]["message"])["content"]; got != expect.text {
				t.Fatalf("entries[%d].content = %v, want %q", i, got, expect.text)
			}
		}
	}
	toolMsg := asMap(entriesOut[2]["message"])
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" || toolMsg["name"] != "write" {
		t.Fatalf("toolResult 条目不符: %+v", toolMsg)
	}
}

// 分叉错误:源不存在、message_id 不在 meta、绑定条目不在当前分支、非法 id。
func TestForkErrors(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Fork("00000000-0000-4000-8000-00000000dead", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("源不存在: err = %v", err)
	}
	if _, err := store.Fork("../x", ""); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("非法 id: err = %v", err)
	}

	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.Messages = []UserMessage{
			{MessageID: "m-1", EntryID: "u201", Text: "主干第一问"},
			{MessageID: "m-old", EntryID: "a202", Text: "旧分支消息"},
		}
	})
	if _, err := store.Fork(meta.ID, "no-such-message"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("message_id 不存在: err = %v", err)
	}
	// 绑定到旧分支条目上的消息:当前分支找不到,同样 404
	if _, err := store.Fork(meta.ID, "m-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("旧分支消息: err = %v", err)
	}
	// 失败不应留下任何新目录
	if entries, err := os.ReadDir(store.root); err != nil || len(entries) != 1 {
		t.Fatalf("失败的 Fork 留下了残留: %v %v", entries, err)
	}
}

// 源会话还没跑过 Pi(没有会话文件):分叉得到一个空会话,Pi 会话 id 为空,
// 首个 run 从空白开始;消息与审批因条目为空而不复制。
func TestForkSessionWithoutPiFile(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Title = "只有 meta"
		m.Messages = []UserMessage{{MessageID: "m-1", EntryID: "", Text: "还没跑过"}}
	})
	forked, err := store.Fork(meta.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked.PiSessionID != "" || len(forked.Messages) != 0 {
		t.Fatalf("空分支分叉结果不符: %+v", forked)
	}
	if forked.Title != "分叉 · 只有 meta" {
		t.Fatalf("Title = %q", forked.Title)
	}
	if _, err := os.Stat(store.Dir(forked.ID)); err != nil {
		t.Fatalf("新会话目录应存在: %v", err)
	}
	// 没有会话文件:PiSessionFile 报错(首个 run 不带 --session-id)
	if _, err := PiSessionFile(store.PiDir(forked.ID), forked.PiSessionID); err == nil {
		t.Fatal("不应有 Pi 会话文件")
	}
	// message_id 给了但文件为空:条目找不到,404
	if _, err := store.Fork(meta.ID, "m-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("无条目可定位: err = %v", err)
	}
}

// 出错回滚:新会话目录一旦创建,后续任何失败都必须删掉它,不留半成品。
// 用一个后台 watcher 在新会话目录出现后立刻创建 meta.json.tmp 目录,
// 使随后 Update 写 meta 的原子替换失败(EISDIR);源文件足够大,保证
// watcher 有时间在写入完成前介入。
func TestForkRollbackOnError(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	// 造一个较大的源文件:读取+选择需要几毫秒,拓宽 watcher 的介入窗口
	var lines []string
	lines = append(lines, `{"type":"session","version":3,"id":"bigsess001","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`)
	for i := 0; i < 3000; i++ {
		lines = append(lines, piLine("assistant", fmt.Sprintf("a-%04d", i), fmt.Sprintf("a-%04d", i), strings.Repeat("内容", 40)))
	}
	for i := 0; i < 30; i++ {
		lines = append(lines, piLine("user", fmt.Sprintf("u-%02d", i), fmt.Sprintf("a-%04d", 2999), fmt.Sprintf("用户第 %d 轮", i)))
	}
	writeLines(t, store, meta.ID, "bigsess001", lines...)
	updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "bigsess001" })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	// 每个新目录只破坏一次:重复创建会与回滚的 RemoveAll 竞争,反而留下残留。
	var mu sync.Mutex
	sabotaged := map[string]bool{}
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			entries, err := os.ReadDir(store.root)
			if err == nil {
				for _, entry := range entries {
					if !entry.IsDir() || entry.Name() == meta.ID {
						continue
					}
					mu.Lock()
					done := sabotaged[entry.Name()]
					sabotaged[entry.Name()] = true
					mu.Unlock()
					if !done {
						// 阻断 meta 的原子替换:meta.json.tmp 已是目录
						_ = os.Mkdir(filepath.Join(store.root, entry.Name(), "meta.json.tmp"), 0o750)
					}
				}
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	var lastErr error
	for attempt := 0; attempt < 80; attempt++ {
		forked, err := store.Fork(meta.ID, "")
		if err != nil {
			lastErr = err
			// 回滚后 root 里只剩源会话目录
			entries, listErr := os.ReadDir(store.root)
			if listErr != nil {
				t.Fatalf("ReadDir: %v", listErr)
			}
			for _, entry := range entries {
				if entry.Name() != meta.ID {
					t.Fatalf("回滚不彻底:残留 %s(err = %v)", entry.Name(), err)
				}
			}
			break
		}
		// watcher 没赶上:清掉这次成功的分叉,再试
		if err := store.Delete(forked.ID); err != nil {
			t.Fatalf("清理成功分叉: %v", err)
		}
	}
	if lastErr == nil {
		t.Skip("watcher 未能介入,跳过回滚断言")
	}
	// 源会话仍然完好
	after, err := store.Get(meta.ID)
	if err != nil || after.PiSessionID != "bigsess001" {
		t.Fatalf("源会话被破坏: %+v %v", after, err)
	}
}

// 同一源会话并发分叉:各自得到独立的新会话,源不受影响(-race 下验证)。
func TestForkConcurrent(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.Messages = []UserMessage{{MessageID: "m-1", EntryID: "u201", Text: "主干第一问"}}
	})
	const count = 8
	metas := make([]Meta, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			forked, err := store.Fork(meta.ID, "")
			if err != nil {
				t.Errorf("Fork(%d): %v", i, err)
				return
			}
			metas[i] = forked
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, forked := range metas {
		if forked.ID == "" || seen[forked.ID] {
			t.Fatalf("分叉 id 冲突或为空: %d %s", i, forked.ID)
		}
		seen[forked.ID] = true
	}
}

// 分叉后的新 Pi 会话文件能被 PiSessionFile 按新 id 定位(首个 run 以
// --session-id <新 id> 恢复时的查找路径)。
func TestForkPiSessionFileLookup(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "toolssess01"
		m.Messages = []UserMessage{{MessageID: "m-001", EntryID: "u001", Text: "列出目录"}}
	})
	forked, err := store.Fork(meta.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	file, err := PiSessionFile(store.PiDir(forked.ID), forked.PiSessionID)
	if err != nil {
		t.Fatalf("按新 id 定位失败: %v", err)
	}
	// 文件名符合 <UTC 时间戳>_<piSessionID>.jsonl 的命名习惯
	base := filepath.Base(file)
	if !strings.HasSuffix(base, "_"+forked.PiSessionID+".jsonl") {
		t.Fatalf("文件名 = %s", base)
	}
	stamp := strings.TrimSuffix(strings.TrimSuffix(base, "_"+forked.PiSessionID+".jsonl"), ".jsonl")
	if len(stamp) != len("2026-10-29T08-00-00-000Z") || strings.ContainsAny(stamp, ":.") {
		t.Fatalf("时间戳前缀 = %q, 应为 Pi 习惯的连字符格式", stamp)
	}
}
