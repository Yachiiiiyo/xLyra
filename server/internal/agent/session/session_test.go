package session

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "重新生成 testdata/*.golden.json")

// ---- 测试助手 ----

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func mustCreate(t *testing.T, store *Store) Meta {
	t.Helper()
	meta, err := store.Create("xlyra/mock-1", "ask")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return meta
}

// writePiFile 把夹具复制进会话的 pi/ 目录,文件名带固定时间戳前缀。
func writePiFile(t *testing.T, store *Store, id, fixture, piSessionID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("读取夹具 %s: %v", fixture, err)
	}
	name := "2026-10-29T00-00-00-000Z_" + piSessionID + ".jsonl"
	path := filepath.Join(store.PiDir(id), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入 Pi 会话文件: %v", err)
	}
	return name
}

// updateMeta 用公开 API 改 meta,顺便回归 Update 本身。
func updateMeta(t *testing.T, store *Store, id string, fn func(*Meta)) Meta {
	t.Helper()
	meta, err := store.Update(id, func(m *Meta) error {
		fn(m)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return meta
}

func entryType(entry map[string]any) string {
	value, _ := entry["type"].(string)
	return value
}

func asMap(value any) map[string]any {
	m, _ := value.(map[string]any)
	return m
}

func asMaps(value any) []map[string]any {
	s, _ := value.([]map[string]any)
	return s
}

func asSlice(value any) []any {
	s, _ := value.([]any)
	return s
}

var fixedTime = time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

// ---- Store ----

func TestStoreLifecycle(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)

	// 目录结构、权限与 meta 文件
	for _, dir := range []string{store.Dir(meta.ID), store.PiDir(meta.ID), store.WorkDir(meta.ID)} {
		if dir == "" {
			t.Fatalf("路径助手对合法 id 返回空串: %s", meta.ID)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录", dir)
		}
		if perm := info.Mode().Perm(); perm != 0o750 {
			t.Fatalf("%s 权限 = %o, want 750", dir, perm)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Dir(meta.ID), "meta.json")); err != nil {
		t.Fatalf("meta.json 未创建: %v", err)
	}

	got, err := store.Get(meta.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != meta.ID || got.Model != "xlyra/mock-1" || got.PermissionMode != "ask" {
		t.Fatalf("Get 返回 %+v", got)
	}
	if len(got.Messages) != 0 || len(got.Escalations) != 0 {
		t.Fatalf("新会话应有空集合: %+v", got)
	}

	// Update 修改 + 自动刷新 UpdatedAt
	before, _ := store.Get(meta.ID)
	updated := updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Title = "标题"
		m.PiSessionID = "pisess"
		m.Messages = append(m.Messages, UserMessage{MessageID: "m1", EntryID: "e1", Text: "hi"})
	})
	if updated.Title != "标题" || updated.PiSessionID != "pisess" || len(updated.Messages) != 1 {
		t.Fatalf("Update 结果 %+v", updated)
	}
	if !updated.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("Update 未刷新 UpdatedAt: %v -> %v", before.UpdatedAt, updated.UpdatedAt)
	}
	again, _ := store.Get(meta.ID)
	if again.Title != "标题" || len(again.Messages) != 1 {
		t.Fatalf("Update 未落盘: %+v", again)
	}

	// fn 返回错误:放弃修改
	if _, err := store.Update(meta.ID, func(m *Meta) error { return errors.New("boom") }); err == nil {
		t.Fatal("fn 出错时 Update 应返回错误")
	}
	kept, _ := store.Get(meta.ID)
	if kept.Title != "标题" {
		t.Fatalf("fn 出错后 meta 被改动: %+v", kept)
	}

	// Update 不存在的会话
	if _, err := store.Update("00000000-0000-4000-8000-00000000dead", func(*Meta) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update 缺失会话: err = %v", err)
	}

	// Delete 幂等
	for i := 0; i < 2; i++ {
		if err := store.Delete(meta.ID); err != nil {
			t.Fatalf("第 %d 次 Delete: %v", i+1, err)
		}
	}
	if _, err := store.Get(meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后 Get: err = %v", err)
	}
	if dir := store.Dir(meta.ID); dir == "" {
		t.Fatal("Dir 对合法 id 应返回路径")
	}
}

func TestInvalidIDs(t *testing.T) {
	store := newTestStore(t)
	bad := []string{
		"",
		"../x",
		"../../etc",
		"not-a-uuid",
		"550e8400-e29b-41d4-a716-446655440000X",
		"550E8400-E29B-41D4-A716-446655440000", // 大写:uuid.Parse 接受,但非规范形式
		"{550e8400-e29b-41d4-a716-446655440000}",
		"urn:uuid:550e8400-e29b-41d4-a716-446655440000",
		"550e8400-e29b-41d4-a716-44665544000",
	}
	for _, id := range bad {
		if _, err := store.Get(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Get(%q): err = %v, want ErrInvalidID", id, err)
		}
		if _, err := store.Update(id, func(*Meta) error { return nil }); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Update(%q): err = %v, want ErrInvalidID", id, err)
		}
		if err := store.Delete(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Delete(%q): err = %v, want ErrInvalidID", id, err)
		}
		if err := store.EnsureDirs(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("EnsureDirs(%q): err = %v, want ErrInvalidID", id, err)
		}
		if _, err := store.BuildTranscript(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("BuildTranscript(%q): err = %v, want ErrInvalidID", id, err)
		}
		if store.Dir(id) != "" || store.PiDir(id) != "" || store.WorkDir(id) != "" {
			t.Errorf("路径助手对非法 id %q 应返回空串", id)
		}
	}
	// 确认没有任何目录被创建到 root 之外
	rootEntries, err := os.ReadDir(store.root)
	if err != nil || len(rootEntries) != 0 {
		t.Fatalf("非法 id 不应创建目录: %v %v", rootEntries, err)
	}
}

func TestListOrderingAndSkip(t *testing.T) {
	store := newTestStore(t)
	// 直接写 meta.json 以精确控制 UpdatedAt
	sess := func(id string, updated time.Time) {
		t.Helper()
		meta := Meta{ID: id, UpdatedAt: updated, CreatedAt: updated.Add(-time.Hour)}
		data, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(store.Dir(id), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.Dir(id), "meta.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	sess("00000000-0000-4000-8000-000000000001", base)
	sess("00000000-0000-4000-8000-000000000002", base.Add(time.Minute))
	sess("00000000-0000-4000-8000-000000000003", base.Add(2*time.Minute))
	// 并列时间:按 id 稳定排序
	sess("00000000-0000-4000-8000-000000000004", base)
	// 坏目录:非 UUID 目录名、缺 meta.json 的 UUID 目录、坏 JSON
	if err := os.MkdirAll(filepath.Join(store.root, "not-a-session"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.root, "00000000-0000-4000-8000-00000000bad"), 0o750); err != nil {
		t.Fatal(err)
	}
	bad2 := filepath.Join(store.root, "00000000-0000-4000-8000-00000000bad2")
	if err := os.MkdirAll(bad2, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad2, "meta.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000004",
	}
	if len(metas) != len(want) {
		t.Fatalf("List 返回 %d 个: %+v", len(metas), metas)
	}
	for i, id := range want {
		if metas[i].ID != id {
			t.Fatalf("metas[%d].ID = %s, want %s(顺序应按 UpdatedAt 倒序、并列按 id)", i, metas[i].ID, id)
		}
	}

	// 根目录不存在:空列表不报错
	empty, err := NewStore(filepath.Join(t.TempDir(), "nope")).List()
	if err != nil || len(empty) != 0 {
		t.Fatalf("缺失根目录 List: %v %v", empty, err)
	}
}

// ---- 槽位分配 ----

// pickFrom 返回固定序列里第一个未被占用的槽位(测试用的确定性选择器)。
func pickFrom(candidates ...int) func(map[int]bool) (int, error) {
	return func(used map[int]bool) (int, error) {
		for _, slot := range candidates {
			if !used[slot] {
				return slot, nil
			}
		}
		return 0, errors.New("pick: 无可用槽位")
	}
}

func TestAssignSlotLifecycle(t *testing.T) {
	store := newTestStore(t)
	a := mustCreate(t, store)
	b := mustCreate(t, store)

	// 首次分配:占用表含对方为 0 不计入,a 取首选 3
	slot, err := store.AssignSlot(a.ID, pickFrom(3))
	if err != nil || slot != 3 {
		t.Fatalf("AssignSlot(a) = %d, %v; want 3", slot, err)
	}
	meta, _ := store.Get(a.ID)
	if meta.Slot != 3 {
		t.Fatalf("meta.Slot = %d, want 3", meta.Slot)
	}

	// 幂等:已有槽位直接返回,pick 不再被调用
	slot, err = store.AssignSlot(a.ID, func(map[int]bool) (int, error) {
		t.Fatal("已有槽位时不应调用 pick")
		return 0, nil
	})
	if err != nil || slot != 3 {
		t.Fatalf("重复 AssignSlot(a) = %d, %v; want 3", slot, err)
	}

	// 扫描能见到已占用的 3:b 的候选避开它
	if slot, err = store.AssignSlot(b.ID, pickFrom(3, 7)); err != nil || slot != 7 {
		t.Fatalf("AssignSlot(b) = %d, %v; want 7", slot, err)
	}

	// pick 报错:错误上抛且 meta 不变(新会话 c 未落盘槽位)
	c := mustCreate(t, store)
	if _, err = store.AssignSlot(c.ID, pickFrom(3, 7)); err == nil {
		t.Fatal("无可用候选应报错")
	}
	if meta, _ = store.Get(c.ID); meta.Slot != 0 {
		t.Fatalf("pick 报错后 meta 被改动: %+v", meta)
	}

	// 删除会话后槽位被回收:c 可以拿到 b 释放的 7
	if err := store.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if slot, err = store.AssignSlot(c.ID, pickFrom(3, 7)); err != nil || slot != 7 {
		t.Fatalf("删除后 AssignSlot(c) = %d, %v; want 7", slot, err)
	}

	// 不存在的会话与非法 id
	if _, err = store.AssignSlot("00000000-0000-4000-8000-00000000dead", pickFrom(1)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失会话错误 = %v, want ErrNotFound", err)
	}
	if _, err = store.AssignSlot("../x", pickFrom(1)); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("非法 id 错误 = %v, want ErrInvalidID", err)
	}
	if _, err = store.AssignSlot(a.ID, nil); err == nil {
		t.Fatal("pick 为 nil 应报错")
	}
}

// TestAssignSlotConcurrent 几十个会话并发分配,槽位两两不同(-race 下验证)。
func TestAssignSlotConcurrent(t *testing.T) {
	store := newTestStore(t)
	const count = 32
	ids := make([]string, count)
	for i := range ids {
		meta := mustCreate(t, store)
		ids[i] = meta.ID
	}
	// 干扰项:坏 meta 目录与非法目录名不阻断分配
	bad := filepath.Join(store.root, "00000000-0000-4000-8000-00000000bad")
	if err := os.MkdirAll(bad, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "meta.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.root, "not-a-session"), 0o750); err != nil {
		t.Fatal(err)
	}

	slots := make([]int, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slot, err := store.AssignSlot(ids[i], func(used map[int]bool) (int, error) {
				// 与生产选择器同策略:取最小空闲位
				for s := 1; ; s++ {
					if !used[s] {
						return s, nil
					}
				}
			})
			if err != nil {
				t.Errorf("AssignSlot(%d): %v", i, err)
				return
			}
			slots[i] = slot
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, slot := range slots {
		if slot <= 0 {
			t.Fatalf("会话 %d 槽位非法: %d", i, slot)
		}
		if seen[slot] {
			t.Fatalf("槽位 %d 被会话 %d 重复分配", slot, i)
		}
		seen[slot] = true
	}
	if len(seen) != count {
		t.Fatalf("应得到 %d 个不同槽位,得到 %d", count, len(seen))
	}
}

// TestAssignSlotNotDisturbUpdate:槽位写入与同会话的 Update 并发,互不丢失
// (-race 下验证)。
func TestAssignSlotNotDisturbUpdate(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := store.Update(meta.ID, func(m *Meta) error {
				m.Messages = append(m.Messages, UserMessage{MessageID: fmt.Sprintf("m-%d", i), Text: "x"})
				return nil
			}); err != nil {
				t.Errorf("Update: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := store.AssignSlot(meta.ID, pickFrom(9)); err != nil {
				t.Errorf("AssignSlot: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	final, err := store.Get(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Slot != 9 {
		t.Fatalf("Slot = %d, want 9", final.Slot)
	}
	if len(final.Messages) != 20 {
		t.Fatalf("消息数 = %d, want 20(Update 不得被槽位写入覆盖)", len(final.Messages))
	}
}

func TestConcurrentUpdate(t *testing.T) {
	store := newTestStore(t)
	const sessions = 2
	const goroutines = 4
	const iterations = 25
	ids := make([]string, sessions)
	for i := range ids {
		meta := mustCreate(t, store)
		ids[i] = meta.ID
	}
	var wg sync.WaitGroup
	for s, id := range ids {
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(id string, s, g int) {
				defer wg.Done()
				for i := 0; i < iterations; i++ {
					_, err := store.Update(id, func(m *Meta) error {
						m.Messages = append(m.Messages, UserMessage{
							MessageID: fmt.Sprintf("m-%d-%d-%d", s, g, i),
							EntryID:   fmt.Sprintf("e-%d-%d-%d", s, g, i),
							Text:      "并发",
						})
						return nil
					})
					if err != nil {
						t.Errorf("Update: %v", err)
						return
					}
				}
			}(id, s, g)
		}
	}
	wg.Wait()
	for _, id := range ids {
		meta, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(meta.Messages) != goroutines*iterations {
			t.Fatalf("会话 %s 丢失更新: %d 条, want %d", id, len(meta.Messages), goroutines*iterations)
		}
	}
}

// ---- Title / PiSessionFile / Preview ----

func TestTitleFrom(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", "新会话"},
		{"纯空白", "   \n\t \n", "新会话"},
		{"普通", "hello world", "hello world"},
		{"换行折叠", "第一行\n第二行\t第三行", "第一行 第二行 第三行"},
		{"首尾空白", "  你好  ", "你好"},
		{"中文四十个正好", strings.Repeat("中", 40), strings.Repeat("中", 40)},
		{"中文四十一个截断", strings.Repeat("中", 41), strings.Repeat("中", 40) + "…"},
		{"混合多字节", strings.Repeat("a中", 21), strings.Repeat("a中", 20) + "…"},
		{"英文长文本", strings.Repeat("x", 100), strings.Repeat("x", 40) + "…"},
		{"换行折叠后超限", strings.Repeat("好", 20) + "\n" + strings.Repeat("好", 20), strings.Repeat("好", 20) + " " + strings.Repeat("好", 19) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleFrom(tc.in); got != tc.want {
				t.Fatalf("TitleFrom(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPiSessionFile(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"2026-01-01T00-00-00-000Z_alpha.jsonl",
		"2026-02-01T00-00-00-000Z_beta.jsonl",
		"2026-03-01T00-00-00-000Z_beta.jsonl", // 同 id 双文件:取最新
		"not-jsonl.txt",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub_dir.jsonl"), 0o750); err != nil {
		t.Fatal(err)
	}

	if got, err := PiSessionFile(dir, "beta"); err != nil || filepath.Base(got) != "2026-03-01T00-00-00-000Z_beta.jsonl" {
		t.Fatalf("按 id 匹配: %v %v", got, err)
	}
	if got, err := PiSessionFile(dir, "alpha"); err != nil || filepath.Base(got) != "2026-01-01T00-00-00-000Z_alpha.jsonl" {
		t.Fatalf("精确 id: %v %v", got, err)
	}
	// id 必须整段匹配:gamma 与 beta/alpha 都不应误中
	if _, err := PiSessionFile(dir, "gamma"); err == nil {
		t.Fatal("不存在的 id 应报错")
	}
	if got, err := PiSessionFile(dir, ""); err != nil || filepath.Base(got) != "2026-03-01T00-00-00-000Z_beta.jsonl" {
		t.Fatalf("空 id 取最新: %v %v", got, err)
	}
	if _, err := PiSessionFile(t.TempDir(), ""); err == nil {
		t.Fatal("空目录应报错")
	}
	if _, err := PiSessionFile(filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("不存在的目录应报错")
	}
}

func piLine(role, id, parent, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%q,"timestamp":"2026-10-29T08:00:00.000Z","message":{"role":%q,"content":[{"type":"text","text":%q}],"timestamp":1793246400000}}`,
		id, parent, role, text)
}

func TestPreview(t *testing.T) {
	write := func(t *testing.T, lines ...string) (*Store, Meta) {
		t.Helper()
		store := newTestStore(t)
		meta := mustCreate(t, store)
		var b strings.Builder
		b.WriteString(`{"type":"session","version":3,"id":"pv","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}` + "\n")
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
		if err := os.WriteFile(filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_pv.jsonl"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return store, meta
	}

	t.Run("最后是assistant", func(t *testing.T) {
		store, meta := write(t,
			piLine("user", "u1", "h", "用户问"),
			piLine("assistant", "a1", "u1", "助手答"),
		)
		if got := store.Preview(meta); got != "助手答" {
			t.Fatalf("Preview = %q, want 助手答", got)
		}
	})
	t.Run("最后是user", func(t *testing.T) {
		store, meta := write(t,
			piLine("user", "u1", "h", "用户问"),
			piLine("assistant", "a1", "u1", "助手答"),
			piLine("user", "u2", "a1", "追问"),
		)
		if got := store.Preview(meta); got != "追问" {
			t.Fatalf("Preview = %q, want 追问", got)
		}
	})
	t.Run("跳过system与toolResult", func(t *testing.T) {
		store, meta := write(t,
			piLine("system", "s1", "h", ""),
			piLine("toolResult", "r1", "s1", "工具输出"),
			piLine("assistant", "a1", "r1", "助手答"),
		)
		if got := store.Preview(meta); got != "助手答" {
			t.Fatalf("Preview = %q, want 助手答", got)
		}
	})
	t.Run("超长截断80rune", func(t *testing.T) {
		long := strings.Repeat("长", 120)
		store, meta := write(t, piLine("assistant", "a1", "h", long))
		if got, want := store.Preview(meta), strings.Repeat("长", 80); got != want {
			t.Fatalf("Preview 长度 = %d rune, want 80", len([]rune(got)))
		}
	})
	t.Run("截断的尾行", func(t *testing.T) {
		store := newTestStore(t)
		meta := mustCreate(t, store)
		content := `{"type":"session","version":3,"id":"pv","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}` + "\n" +
			piLine("assistant", "a1", "h", "完整的一行") + "\n" +
			`{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-10-29T08:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"被截`
		if err := os.WriteFile(filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_pv.jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := store.Preview(meta); got != "完整的一行" {
			t.Fatalf("Preview = %q, want 完整的一行", got)
		}
	})
	t.Run("超过64KB窗口的单行", func(t *testing.T) {
		store := newTestStore(t)
		meta := mustCreate(t, store)
		huge := piLine("assistant", "a1", "h", strings.Repeat("大", 40000)) // ~120KB 单行
		if err := os.WriteFile(filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_pv.jsonl"), []byte(huge), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := store.Preview(meta); got != "" {
			t.Fatalf("窗口内读不到完整行,Preview 应为空,得到 %d 字节", len(got))
		}
	})
	t.Run("无Pi文件", func(t *testing.T) {
		store := newTestStore(t)
		meta := mustCreate(t, store)
		if got := store.Preview(meta); got != "" {
			t.Fatalf("Preview = %q, want 空串", got)
		}
	})
	t.Run("指定PiSessionID", func(t *testing.T) {
		store := newTestStore(t)
		meta := mustCreate(t, store)
		// 旧文件在时间上更新也会被 id 精确匹配排除
		if err := os.WriteFile(filepath.Join(store.PiDir(meta.ID), "2026-01-01T00-00-00-000Z_old.jsonl"), []byte(piLine("assistant", "a1", "h", "旧会话")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		meta = updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "old" })
		if got := store.Preview(meta); got != "旧会话" {
			t.Fatalf("Preview = %q, want 旧会话", got)
		}
		meta = updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "missing-id" })
		if got := store.Preview(meta); got != "" {
			t.Fatalf("指定不存在 id 的 Preview = %q, want 空串", got)
		}
	})
}

// ---- Transcript ----

func TestBuildTranscriptCompactionFixture(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "compaction.jsonl", "h2resume01")
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "h2resume01"
		m.Messages = []UserMessage{
			{MessageID: "m-001", EntryID: "54e69579", Text: "session-marker-h2 第一段"},
			{MessageID: "m-002", EntryID: "76133244", Text: "recent-marker 第二段"},
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 期望:user,assistant,user,assistant,compaction(system/model_change 等跳过)
	wantTypes := []string{"message", "message", "message", "message", "compaction"}
	if len(entries) != len(wantTypes) {
		t.Fatalf("条目数 = %d, want %d: %+v", len(entries), len(wantTypes), entries)
	}
	for i, want := range wantTypes {
		if entryType(entries[i]) != want {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entries[i]), want)
		}
	}
	first := asMap(entries[0]["message"])
	if first["role"] != "user" || first["content"] != "session-marker-h2 第一段" {
		t.Fatalf("entries[0].message = %+v", first)
	}
	if entries[0]["message_id"] != "m-001" {
		t.Fatalf("message_id 反查失败: %v", entries[0]["message_id"])
	}
	if entries[2]["message_id"] != "m-002" {
		t.Fatalf("entries[2].message_id = %v, want m-002", entries[2]["message_id"])
	}
	compaction := entries[4]
	if compaction["summary"] == "" {
		t.Fatal("compaction.summary 为空")
	}
	if compaction["tokens_before"] != int64(16) {
		t.Fatalf("tokens_before = %v, want 16", compaction["tokens_before"])
	}
	if _, ok := compaction["tokens_after"]; ok {
		t.Fatalf("文件里没有 tokens_after 时应省略: %+v", compaction)
	}
	if compaction["timestamp"] != "2026-10-29T14:19:40.414Z" {
		t.Fatalf("compaction.timestamp = %v", compaction["timestamp"])
	}
}

func TestBuildTranscriptAbortFixture(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "text-abort.jsonl", "c3stream01")
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(entries))
	}
	userMsg := asMap(entries[0]["message"])
	if userMsg["role"] != "user" || userMsg["content"] != "流式中止" {
		t.Fatalf("user = %+v", userMsg)
	}
	asst := asMap(entries[1]["message"])
	if asst["role"] != "assistant" || asst["content"] != "PARTIAL-STREAM" {
		t.Fatalf("assistant = %+v", asst)
	}
	if entries[1]["message_id"] != "dfc7786e" {
		t.Fatalf("未绑定时 message_id 应回退 Pi 条目 id: %v", entries[1]["message_id"])
	}
}

func TestBuildTranscriptTools(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// user, asst(call), tool, asst(call), tool(err), asst
	if len(entries) != 6 {
		t.Fatalf("条目数 = %d, want 6", len(entries))
	}
	asst1 := asMap(entries[1]["message"])
	if asst1["content"] != "我来执行 ls" {
		t.Fatalf("assistant 文本 = %v", asst1["content"])
	}
	if asst1["thinking"] != "先想想" {
		t.Fatalf("thinking = %v", asst1["thinking"])
	}
	calls := asMaps(asst1["tool_calls"])
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %+v", calls)
	}
	call := asMap(calls[0])
	if call["id"] != "call_1" || call["name"] != "bash" {
		t.Fatalf("call = %+v", call)
	}
	raw, isString := call["raw_arguments"].(string)
	if !isString {
		t.Fatalf("raw_arguments 应是 JSON 字符串: %T", call["raw_arguments"])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("raw_arguments 不是合法 JSON: %v", err)
	}
	if args["command"] != "ls" || args["timeout"] != float64(10) {
		t.Fatalf("raw_arguments = %q", raw)
	}
	tool1 := asMap(entries[2]["message"])
	if tool1["role"] != "tool" || tool1["tool_call_id"] != "call_1" || tool1["name"] != "bash" {
		t.Fatalf("tool = %+v", tool1)
	}
	if tool1["is_error"] != false {
		t.Fatalf("is_error = %v", tool1["is_error"])
	}
	tool2 := asMap(entries[4]["message"])
	if tool2["tool_call_id"] != "call_2" || tool2["is_error"] != true {
		t.Fatalf("tool2 = %+v", tool2)
	}
	asst3 := asMap(entries[5]["message"])
	if _, has := asst3["tool_calls"]; has {
		t.Fatalf("无工具调用的 assistant 不应有 tool_calls: %+v", asst3)
	}
	if _, has := asst3["thinking"]; has {
		t.Fatalf("无思考块的 assistant 不应有 thinking: %+v", asst3)
	}
	images := asMaps(tool1["images"])
	if len(images) != 1 {
		t.Fatalf("tool1.images = %+v, want 仅合法的 1 项", images)
	}
	image := asMap(images[0])
	if image["path"] != "images/01-20261103-153012.png" || image["mime"] != "image/png" ||
		image["bytes"] != int64(123) || image["model"] != "gpt-image-2" ||
		image["site"] != "demo" || image["alt"] != "示意图" {
		t.Fatalf("tool1.images[0] = %+v", image)
	}
	if _, has := tool2["images"]; has {
		t.Fatalf("无 details.images 的旧工具结果不应有 images: %+v", tool2)
	}
}

// details.images 的形状与字段都来自 Pi 扩展,非法项丢弃、非法字段归零,
// 且不能影响同一 toolResult 的文字内容。
func TestBuildTranscriptToolImageSanitization(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	alt := strings.Repeat("说", 201)
	writeLines(t, store, meta.ID, "toolimgs01",
		`{"type":"session","version":3,"id":"toolimgs01","timestamp":"2026-10-29T08:00:00.000Z","cwd":"w"}`,
		`{"type":"message","id":"r1","parentId":null,"timestamp":"2026-10-29T08:00:00.010Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"image_generation","content":"已生成","details":{"images":[{"path":"images/ok.webp","mime":"text/plain","bytes":-1,"model":2,"site":null,"alt":"`+alt+`"},{"path":"images//x.png","mime":"image/png"},{"path":"images/a.png","mime":5,"bytes":1.5}]}}}`,
	)
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	message := asMap(entries[0]["message"])
	images := asMaps(message["images"])
	if len(images) != 3 {
		t.Fatalf("images = %+v, want 3 项", images)
	}
	first := asMap(images[0])
	if first["mime"] != "" || first["bytes"] != int64(0) || first["model"] != "" || first["site"] != "" ||
		utf8.RuneCountInString(first["alt"].(string)) != 200 {
		t.Fatalf("非法字段未归零/截断: %+v", first)
	}
	second := asMap(images[1])
	if second["path"] != "images//x.png" || second["mime"] != "image/png" {
		t.Fatalf("合法项被误删或改写: %+v", second)
	}
	third := asMap(images[2])
	if third["mime"] != "" || third["bytes"] != int64(0) {
		t.Fatalf("非法字段未归零: %+v", third)
	}
	if message["content"] != "已生成" || asMap(entries[0])["type"] != "message" {
		t.Fatalf("工具文字内容被改动: %+v", entries[0])
	}
}

func TestBuildTranscriptImageAndStringContent(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "image.jsonl", "imgsess001")
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "imgsess001"
		m.Messages = []UserMessage{{
			MessageID: "m-010", EntryID: "u010", Text: "看这两张图",
			Attachments: []Attachment{{Name: "截图.png", MimeType: "image/png"}},
		}}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("条目数 = %d, want 4", len(entries))
	}
	user := asMap(entries[0]["message"])
	if user["content"] != "看这两张图" {
		t.Fatalf("content = %v", user["content"])
	}
	attachments := asMaps(user["attachments"])
	if len(attachments) != 2 {
		t.Fatalf("attachments = %+v", attachments)
	}
	first := asMap(attachments[0])
	if first["name"] != "截图.png" || first["mime_type"] != "image/png" {
		t.Fatalf("第一张(有 meta 名字): %+v", first)
	}
	if dataURL, _ := first["data_url"].(string); !strings.HasPrefix(dataURL, "data:image/png;base64,iVBOR") {
		t.Fatalf("data_url = %q", dataURL)
	}
	second := asMap(attachments[1])
	if second["name"] != "image-1" || second["mime_type"] != "image/jpeg" {
		t.Fatalf("第二张(meta 缺名,回退 image-1): %+v", second)
	}
	// content 为字符串的用户消息
	plain := asMap(entries[2]["message"])
	if plain["content"] != "纯文本字符串内容" {
		t.Fatalf("字符串 content = %v", plain["content"])
	}
	if _, has := plain["attachments"]; has {
		t.Fatalf("无图片不应有 attachments: %+v", plain)
	}
}

func TestBuildTranscriptFork(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	const forkID = "01a186c7-97db-767e-a5ba-e0566fe41356"
	writePiFile(t, store, meta.ID, "fork.jsonl", forkID)
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = forkID // fork 后必须用新文件/新 id
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 新文件是线性复制:RETRY-A 一问一答 + fork 后一问一答
	if len(entries) != 4 {
		t.Fatalf("条目数 = %d, want 4", len(entries))
	}
	texts := []string{"RETRY-A 第一问", "RETRY-A 的回答", "RETRY-B 改写后的重发", "fork 之后的回答"}
	for i, want := range texts {
		msg := asMap(entries[i]["message"])
		if msg["content"] != want {
			t.Fatalf("entries[%d].content = %v, want %q", i, msg["content"], want)
		}
	}
}

func TestBuildTranscriptBranch(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 叶子是最后一条(a203),路径 u201→a201→u203→a203;被放弃的 u202/a202 不出现
	var texts []string
	for _, entry := range entries {
		msg := asMap(entry["message"])
		texts = append(texts, fmt.Sprint(msg["content"]))
	}
	joined := strings.Join(texts, "|")
	if joined != "主干第一问|主干回答|改走另一分支的第二问|新分支回答" {
		t.Fatalf("分支路径内容 = %q", joined)
	}
}

func TestBuildTranscriptBrokenFile(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "broken.jsonl", "brokensess1")
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 坏行、空行、截断行被跳过;最后一行无换行符也能读;u302 的 parentId=u301
	if len(entries) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(entries))
	}
	if got := asMap(entries[0]["message"])["content"]; got != "第一条" {
		t.Fatalf("entries[0].content = %v", got)
	}
	if got := asMap(entries[1]["message"])["content"]; got != "最后一条(无换行结尾)" {
		t.Fatalf("entries[1].content = %v", got)
	}
}

func TestBuildTranscriptEmptyAndMissing(t *testing.T) {
	store := newTestStore(t)

	t.Run("没有Pi目录", func(t *testing.T) {
		meta := mustCreate(t, store)
		entries, err := store.BuildTranscript(meta.ID)
		if err != nil {
			t.Fatalf("BuildTranscript: %v", err)
		}
		if entries == nil || len(entries) != 0 {
			t.Fatalf("应返回空非 nil 切片: %v", entries)
		}
		// JSON 序列化应为 []
		data, _ := json.Marshal(entries)
		if string(data) != "[]" {
			t.Fatalf("JSON = %s, want []", data)
		}
		// 启动阶段失败可能尚未来得及创建会话文件,错误仍要回放。
		updateMeta(t, store, meta.ID, func(m *Meta) {
			m.AppendRunError(RunError{RunID: "boot-failed", Message: "启动失败", At: fixedTime})
		})
		entries, err = store.BuildTranscript(meta.ID)
		if err != nil || len(entries) != 1 || entries[0]["type"] != "run_error" || entries[0]["message"] != "启动失败" {
			t.Fatalf("无会话文件的 run 失败: %v %+v", err, entries)
		}
	})
	t.Run("空文件", func(t *testing.T) {
		meta := mustCreate(t, store)
		path := filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_empty.jsonl")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err := store.BuildTranscript(meta.ID)
		if err != nil || len(entries) != 0 {
			t.Fatalf("空文件: %v %v", entries, err)
		}
	})
	t.Run("全是坏行", func(t *testing.T) {
		meta := mustCreate(t, store)
		path := filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_bad.jsonl")
		content := "not json\n{\n\"type\":\"message\"\n\n[]\n123\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		entries, err := store.BuildTranscript(meta.ID)
		if err != nil || len(entries) != 0 {
			t.Fatalf("坏行: %v %v", entries, err)
		}
	})
	t.Run("meta不存在", func(t *testing.T) {
		if _, err := store.BuildTranscript("00000000-0000-4000-8000-00000000000f"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestBuildTranscriptHugeLine(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	big := strings.Repeat("长文本", 700000) // >4MB 的单行
	line := piLine("user", "big1", "", big)
	path := filepath.Join(store.PiDir(meta.ID), "2026-10-29T00-00-00-000Z_big.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d, want 1", len(entries))
	}
	if got := asMap(entries[0]["message"])["content"]; got != big {
		t.Fatalf("超长行内容不符: %d 字节", len(got.(string)))
	}
}

func TestEscalationInsertion(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	granted := false
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Escalations = []Escalation{
			{EscalationID: "esc-1", ToolName: "write", RequestedPath: "/tmp/a.txt", ResolvedPath: "/tmp/a.txt",
				RequestedCommand: []string{"write", "/tmp/a.txt"}, Granted: &granted, At: fixedTime, AfterEntryID: "r002"},
			{EscalationID: "esc-2", ToolName: "bash", RequestedPath: "", ResolvedPath: "",
				Granted: nil, At: fixedTime, AfterEntryID: "zzz-unknown"}, // 非空但不在当前文件里:旧文件的记录,丢弃
			{EscalationID: "esc-3", ToolName: "bash", Granted: nil, At: fixedTime}, // 空锚点:末尾
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 基础 6 条 + 2 条审批(esc-2 的锚点不在当前文件里,被丢弃);esc-1 应紧跟 r002(下标 4 的 tool)之后
	if len(entries) != 8 {
		t.Fatalf("条目数 = %d, want 8", len(entries))
	}
	if entryType(entries[5]) != "escalation" || entries[5]["escalation_id"] != "esc-1" {
		t.Fatalf("entries[5] 应是 esc-1: %+v", entries[5])
	}
	tool := asMap(entries[4]["message"])
	if tool["tool_call_id"] != "call_2" { // r002 是 call_2 的结果
		t.Fatalf("锚点前应为 r002: %+v", tool)
	}
	esc1 := entries[5]
	if esc1["granted"] != false || esc1["requested_command"].([]string) == nil {
		t.Fatalf("esc-1 = %+v", esc1)
	}
	if esc1["timestamp"] != "2026-10-29T08:00:00.000Z" {
		t.Fatalf("esc-1.timestamp = %v", esc1["timestamp"])
	}
	// 末尾只剩空锚点的 esc-3(esc-2 的锚点不在当前文件里,已丢弃)
	if entryType(entries[7]) != "escalation" || entries[7]["escalation_id"] != "esc-3" {
		t.Fatalf("entries[7] 应是 esc-3: %+v", entries[7])
	}
	if _, has := entries[7]["requested_command"]; has {
		t.Fatalf("空命令应省略 requested_command: %+v", entries[7])
	}
	if entries[7]["granted"] != nil {
		t.Fatalf("未决审批 granted 应为 null: %+v", entries[7])
	}
}

// 同一锚点多条审批保持 meta 顺序。
func TestEscalationOrderSameAnchor(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	yes := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Escalations = []Escalation{
			{EscalationID: "esc-b", ToolName: "write", Granted: &yes, At: fixedTime, AfterEntryID: "a002"},
			{EscalationID: "esc-a", ToolName: "bash", Granted: nil, At: fixedTime, AfterEntryID: "a002"},
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// a002 是第 3 条(下标 3),其后紧跟 esc-b、esc-a
	if entries[4]["escalation_id"] != "esc-b" || entries[5]["escalation_id"] != "esc-a" {
		t.Fatalf("同锚点顺序: %+v %+v", entries[4], entries[5])
	}
}

// ---- ListItems ----

func TestListItems(t *testing.T) {
	store := newTestStore(t)

	// 会话 A:无 title(回退 TitleFrom)、有 Pi 文件(preview)、running
	a := mustCreate(t, store)
	writePiFile(t, store, a.ID, "text-abort.jsonl", "c3stream01")
	updateMeta(t, store, a.ID, func(m *Meta) {
		m.Messages = []UserMessage{{MessageID: "m1", EntryID: "1ae6f659", Text: "第一句话 很长的开头\n第二行"}}
	})
	// 会话 B:有 title、无 Pi 文件
	b := mustCreate(t, store)
	updateMeta(t, store, b.ID, func(m *Meta) { m.Title = "自定义标题" })
	// 让 B 更新:排最前
	time.Sleep(2 * time.Millisecond)
	updateMeta(t, store, b.ID, func(m *Meta) { m.Model = "xlyra/mock-2" })

	items, err := store.ListItems(func(id string) bool { return id == a.ID })
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	first, second := items[0], items[1]
	if first.SessionID != b.ID || first.Title != "自定义标题" || first.Preview != "" || first.Running {
		t.Fatalf("first = %+v", first)
	}
	if second.SessionID != a.ID {
		t.Fatalf("second = %+v", second)
	}
	if second.Title != "第一句话 很长的开头 第二行" {
		t.Fatalf("Title 回退 = %q", second.Title)
	}
	if second.Preview != "PARTIAL-STREAM" {
		t.Fatalf("Preview = %q, want PARTIAL-STREAM", second.Preview)
	}
	if !second.Running {
		t.Fatal("running 回调未生效")
	}
	if second.UpdatedAt <= 0 {
		t.Fatalf("UpdatedAt = %d", second.UpdatedAt)
	}
}

// ---- Golden ----

func TestGoldenTranscript(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name    string
		fixture string
		piID    string
		meta    func(*Meta)
	}{
		{
			name: "compaction", fixture: "compaction.jsonl", piID: "h2resume01",
			meta: func(m *Meta) {
				m.PiSessionID = "h2resume01"
				m.Messages = []UserMessage{
					{MessageID: "m-001", EntryID: "54e69579", Text: "session-marker-h2 第一段"},
					{MessageID: "m-002", EntryID: "76133244", Text: "recent-marker 第二段"},
				}
				m.Escalations = []Escalation{{
					EscalationID: "esc-1", ToolName: "write",
					RequestedPath: "/tmp/h2.txt", ResolvedPath: "/tmp/h2-resolved.txt",
					RequestedCommand: []string{"write", "/tmp/h2.txt"},
					Granted:          &yes, At: fixedTime, AfterEntryID: "76133244",
				}}
			},
		},
		{
			name: "tools", fixture: "tools.jsonl", piID: "toolssess01",
			meta: func(m *Meta) {
				m.PiSessionID = "toolssess01"
				m.Messages = []UserMessage{{MessageID: "m-001", EntryID: "u001", Text: "列出目录"}}
				m.Escalations = []Escalation{
					{EscalationID: "esc-deny", ToolName: "write", RequestedPath: "a.txt", ResolvedPath: "/w/a.txt",
						RequestedCommand: []string{"write", "a.txt"}, Granted: &no, At: fixedTime, AfterEntryID: "r002"},
					{EscalationID: "esc-orphan", ToolName: "bash", Granted: nil, At: fixedTime, AfterEntryID: "no-such"},
					{EscalationID: "esc-open", ToolName: "bash", Granted: nil, At: fixedTime},
				}
			},
		},
		{
			name: "image", fixture: "image.jsonl", piID: "imgsess001",
			meta: func(m *Meta) {
				m.PiSessionID = "imgsess001"
				m.Messages = []UserMessage{{
					MessageID: "m-010", EntryID: "u010", Text: "看这两张图",
					Attachments: []Attachment{{Name: "截图.png", MimeType: "image/png"}},
				}}
			},
		},
		{
			name: "fork", fixture: "fork.jsonl", piID: "01a186c7-97db-767e-a5ba-e0566fe41356",
			meta: func(m *Meta) {
				m.PiSessionID = "01a186c7-97db-767e-a5ba-e0566fe41356"
				m.Messages = []UserMessage{
					{MessageID: "m-101", EntryID: "u101", Text: "RETRY-A 第一问"},
					{MessageID: "m-102", EntryID: "u102", Text: "RETRY-B 改写后的重发"},
				}
			},
		},
		{name: "branch", fixture: "branch.jsonl", piID: "branchess001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			meta := mustCreate(t, store)
			writePiFile(t, store, meta.ID, tc.fixture, tc.piID)
			if tc.meta != nil {
				updateMeta(t, store, meta.ID, tc.meta)
			}
			entries, err := store.BuildTranscript(meta.ID)
			if err != nil {
				t.Fatalf("BuildTranscript: %v", err)
			}
			got, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				t.Fatalf("MarshalIndent: %v", err)
			}
			goldenPath := filepath.Join("testdata", tc.name+".golden.json")
			if *update {
				if err := os.WriteFile(goldenPath, append(got, '\n'), 0o600); err != nil {
					t.Fatalf("写 golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("读 golden(先跑 go test -update 生成): %v", err)
			}
			if string(got) != strings.TrimRight(string(want), "\n") {
				t.Fatalf("golden 不符:\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// ---- P6e:旧分支审批过滤 / run 失败记录 / 会话内允许 ----

// fork 后被抛弃的旧分支上的审批不再补到末尾;当前分支上的照旧插入,
// 空锚点的补在末尾;非空但当前文件里找不到的锚点属于被抛弃的旧文件(retry 会 fork 出新文件),丢弃。branch.jsonl 的当前分支是
// u201→a201→u203→a203,旧分支为 u202/a202。
func TestEscalationDroppedOnForkedBranch(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	granted := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Escalations = []Escalation{
			{EscalationID: "esc-old", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a202"}, // 旧分支:丢弃
			{EscalationID: "esc-new", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "u203"}, // 当前分支:插入
			{EscalationID: "esc-miss", ToolName: "bash", Granted: nil, At: fixedTime, AfterEntryID: "no-such"},   // 非空但不在当前文件里:旧文件的记录,丢弃
			{EscalationID: "esc-empty", ToolName: "bash", Granted: nil, At: fixedTime},                           // 空锚点:末尾
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	var ids []string
	for _, entry := range entries {
		if entryType(entry) == "escalation" {
			ids = append(ids, entry["escalation_id"].(string))
		}
	}
	// 顺序:esc-new 紧跟 u203(第 3 条消息,下标 2)之后;末尾两条按 meta 顺序
	if len(ids) != 2 || ids[0] != "esc-new" || ids[1] != "esc-empty" {
		t.Fatalf("审批序列 = %v, want [esc-new esc-empty](esc-old、esc-miss 应被丢弃)", ids)
	}
	if entries[3]["escalation_id"] != "esc-new" {
		t.Fatalf("esc-new 应紧跟 u203 之后(下标 3): %+v", entries[3])
	}
	if msg := asMap(entries[2]["message"]); msg["content"] != "改走另一分支的第二问" {
		t.Fatalf("esc-new 的锚点前一条应为 u203: %+v", msg)
	}
}

// run 失败记录按与审批相同的锚点规则插入;同一锚点下审批在前、错误在后;
// 旧分支上的错误同样丢弃。
func TestRunErrorInsertion(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	granted := false
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Escalations = []Escalation{
			{EscalationID: "esc-a201", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a201"},
		}
		m.RunErrors = []RunError{
			{RunID: "run-1", Message: "旧分支失败", AfterEntryID: "a202", At: fixedTime},      // 旧分支:丢弃
			{RunID: "run-2", Message: "a201 后失败", AfterEntryID: "a201", At: fixedTime},   // 同锚点:审批之后
			{RunID: "run-3", Message: "新分支失败", AfterEntryID: "a203", At: fixedTime},      // 当前分支末条之后
			{RunID: "run-4", Message: "无锚点失败", AfterEntryID: "", At: fixedTime},          // 空锚点:末尾
			{RunID: "run-5", Message: "锚点未落盘失败", AfterEntryID: "no-such", At: fixedTime}, // 非空但不在当前文件里:旧文件的记录,丢弃
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	// 期望序列(消息路径 u201,a201,u203,a203):u201, a201, esc-a201, run-2,
	// u203, a203, run-3, run-4, run-5
	want := []struct {
		kind    string
		payload string
	}{
		{"message", "主干第一问"},
		{"message", "主干回答"},
		{"escalation", "esc-a201"},
		{"run_error", "a201 后失败"},
		{"message", "改走另一分支的第二问"},
		{"message", "新分支回答"},
		{"run_error", "新分支失败"},
		{"run_error", "无锚点失败"},
	}
	if len(entries) != len(want) {
		t.Fatalf("条目数 = %d, want %d: %+v", len(entries), len(want), entries)
	}
	for i, expect := range want {
		if entryType(entries[i]) != expect.kind {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entries[i]), expect.kind)
		}
		switch expect.kind {
		case "escalation":
			if entries[i]["escalation_id"] != expect.payload {
				t.Fatalf("entries[%d] = %+v, want %s", i, entries[i], expect.payload)
			}
		case "run_error":
			if entries[i]["message"] != expect.payload {
				t.Fatalf("entries[%d] = %+v, want %s", i, entries[i], expect.payload)
			}
			if entries[i]["timestamp"] != "2026-10-29T08:00:00.000Z" {
				t.Fatalf("run_error.timestamp = %v", entries[i]["timestamp"])
			}
		default:
			if content := asMap(entries[i]["message"])["content"]; content != expect.payload {
				t.Fatalf("entries[%d].content = %v, want %q", i, content, expect.payload)
			}
		}
	}
	// run_error 条目只有 type/message/timestamp 三个键
	for _, entry := range entries {
		if entryType(entry) == "run_error" && len(entry) != 3 {
			t.Fatalf("run_error 条目应只有 3 个键: %+v", entry)
		}
	}
}

// escalation 条目仅在会话内允许/自动放行时带 scope 与 auto。
func TestEscalationScopeAutoFields(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")
	yes := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.Escalations = []Escalation{
			{EscalationID: "esc-plain", ToolName: "write", Granted: &yes, At: fixedTime, AfterEntryID: "a001"},
			{EscalationID: "esc-session", ToolName: "write", Granted: &yes, At: fixedTime, AfterEntryID: "r001", Scope: "session"},
			{EscalationID: "esc-auto", ToolName: "write", Granted: &yes, At: fixedTime, AfterEntryID: "a002", Scope: "session", Auto: true},
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, entry := range entries {
		if entryType(entry) == "escalation" {
			byID[entry["escalation_id"].(string)] = entry
		}
	}
	if len(byID) != 3 {
		t.Fatalf("审批条目 = %d, want 3", len(byID))
	}
	if _, has := byID["esc-plain"]["scope"]; has {
		t.Fatalf("普通审批不应带 scope: %+v", byID["esc-plain"])
	}
	if _, has := byID["esc-plain"]["auto"]; has {
		t.Fatalf("普通审批不应带 auto: %+v", byID["esc-plain"])
	}
	if byID["esc-session"]["scope"] != "session" {
		t.Fatalf("esc-session.scope = %v", byID["esc-session"]["scope"])
	}
	if _, has := byID["esc-session"]["auto"]; has {
		t.Fatalf("用户点击的会话内允许不应带 auto: %+v", byID["esc-session"])
	}
	if byID["esc-auto"]["scope"] != "session" || byID["esc-auto"]["auto"] != true {
		t.Fatalf("esc-auto = %+v", byID["esc-auto"])
	}
}

// AppendRunError 只保留最近 MaxRunErrors 条。
func TestAppendRunErrorCap(t *testing.T) {
	meta := Meta{}
	for i := 0; i < MaxRunErrors+8; i++ {
		meta.AppendRunError(RunError{RunID: fmt.Sprintf("run-%02d", i), Message: "失败"})
	}
	if len(meta.RunErrors) != MaxRunErrors {
		t.Fatalf("条数 = %d, want %d", len(meta.RunErrors), MaxRunErrors)
	}
	if meta.RunErrors[0].RunID != "run-08" || meta.RunErrors[len(meta.RunErrors)-1].RunID != fmt.Sprintf("run-%02d", MaxRunErrors+7) {
		t.Fatalf("应保留最近的 %d 条,首尾 = %s/%s", MaxRunErrors, meta.RunErrors[0].RunID, meta.RunErrors[len(meta.RunErrors)-1].RunID)
	}
}

// 旧 meta.json(缺 session_allow/run_errors/escalation 的 scope 等新字段)
// 反序列化得到空切片/零值,读写不受影响。
func TestMetaLegacyCompat(t *testing.T) {
	store := newTestStore(t)
	id := "00000000-0000-4000-8000-0000000000aa"
	dir := store.Dir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"` + id + `","title":"旧会话","model":"m","permission_mode":"ask",` +
		`"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z",` +
		`"pi_session_id":"","messages":[],"escalations":[]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if meta.SessionAllow == nil || meta.RunErrors == nil || len(meta.SessionAllow) != 0 || len(meta.RunErrors) != 0 {
		t.Fatalf("旧 meta 的新字段应为空非 nil 切片: %+v", meta)
	}
	// Update 追加新字段后正常落盘
	if _, err := store.Update(id, func(m *Meta) error {
		m.SessionAllow = append(m.SessionAllow, "write")
		m.AppendRunError(RunError{RunID: "r1", Message: "boom", At: fixedTime})
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	meta, err = store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SessionAllow) != 1 || meta.SessionAllow[0] != "write" || len(meta.RunErrors) != 1 {
		t.Fatalf("追加后 = %+v", meta)
	}
}

// ListItems 的 SessionAllow 恒为非 nil(前端 JSON 需要数组)。
func TestListItemsSessionAllow(t *testing.T) {
	store := newTestStore(t)
	mustCreate(t, store) // 无允许列表的会话
	b := mustCreate(t, store)
	updateMeta(t, store, b.ID, func(m *Meta) { m.SessionAllow = []string{"write", "bash"} })
	items, err := store.ListItems(nil)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	for _, item := range items {
		if item.SessionAllow == nil {
			t.Fatalf("会话 %s 的 SessionAllow 不应为 nil", item.SessionID)
		}
	}
}

// ---- P6g:锚点兜底 LastEntryID / 多轮审批就位 / fork 后丢弃 ----

// writeLines 在会话 pi/ 目录下写一个 Pi 会话文件(内容为拼接的行)。
func writeLines(t *testing.T, store *Store, id, piSessionID string, lines ...string) {
	t.Helper()
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
	path := filepath.Join(store.PiDir(id), "2026-10-29T00-00-00-000Z_"+piSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("写入 Pi 会话文件: %v", err)
	}
}

func TestLastEntryID(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "tools.jsonl", "toolssess01")

	// meta.PiSessionID 为空时取目录里最新的文件;末条带 id 的条目是 a003
	if got, err := store.LastEntryID(meta.ID); err != nil || got != "a003" {
		t.Fatalf("LastEntryID = %q, %v; want a003", got, err)
	}
	// 指定 PiSessionID 走精确匹配
	meta = updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "toolssess01" })
	if got, err := store.LastEntryID(meta.ID); err != nil || got != "a003" {
		t.Fatalf("指定 id 的 LastEntryID = %q, %v; want a003", got, err)
	}
	// 指向不存在的 Pi 会话:空串且不报错
	updateMeta(t, store, meta.ID, func(m *Meta) { m.PiSessionID = "no-such" })
	if got, err := store.LastEntryID(meta.ID); err != nil || got != "" {
		t.Fatalf("不存在文件的 LastEntryID = %q, %v; want 空", got, err)
	}

	t.Run("文件不存在", func(t *testing.T) {
		fresh := mustCreate(t, store)
		if got, err := store.LastEntryID(fresh.ID); err != nil || got != "" {
			t.Fatalf("无 Pi 文件的 LastEntryID = %q, %v; want 空", got, err)
		}
	})
	t.Run("空文件与坏行", func(t *testing.T) {
		empty := mustCreate(t, store)
		writeLines(t, store, empty.ID, "empty01")
		if got, err := store.LastEntryID(empty.ID); err != nil || got != "" {
			t.Fatalf("空文件 LastEntryID = %q, %v; want 空", got, err)
		}
		bad := mustCreate(t, store)
		writeLines(t, store, bad.ID, "bad01", "not json", "{", `{"type":"message"}`, `{"id":"no-type"}`)
		if got, err := store.LastEntryID(bad.ID); err != nil || got != "" {
			t.Fatalf("坏行 LastEntryID = %q, %v; want 空", got, err)
		}
	})
	t.Run("末行无换行符也能取到", func(t *testing.T) {
		tail := mustCreate(t, store)
		content := piLine("user", "u-1", "", "问") + "\n" + piLine("assistant", "a-1", "u-1", "答")
		path := filepath.Join(store.PiDir(tail.ID), "2026-10-29T00-00-00-000Z_tail01.jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := store.LastEntryID(tail.ID); err != nil || got != "a-1" {
			t.Fatalf("LastEntryID = %q, %v; want a-1", got, err)
		}
	})
	t.Run("尾部窗口内没有条目", func(t *testing.T) {
		huge := mustCreate(t, store)
		// 末条有效条目之后跟一行超过 256 KiB 的垃圾(无换行结尾):
		// 窗口内解析不出任何条目,返回空串而不是更早的条目。
		content := piLine("assistant", "a-1", "", "早于窗口") + "\n" + strings.Repeat("x", 300*1024)
		path := filepath.Join(store.PiDir(huge.ID), "2026-10-29T00-00-00-000Z_huge01.jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := store.LastEntryID(huge.ID); err != nil || got != "" {
			t.Fatalf("窗口外 LastEntryID = %q, %v; want 空", got, err)
		}
	})
	t.Run("非法id", func(t *testing.T) {
		if _, err := store.LastEntryID("../x"); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("err = %v, want ErrInvalidID", err)
		}
	})
}

// 多轮会话里锚点正确的审批各自出现在自己那一轮的 assistant 条目之后,
// 而不是全部补到 transcript 末尾(P6g 修复前的表现)。
func TestEscalationAnchoredPerTurn(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writeLines(t, store, meta.ID, "multiturn01",
		`{"type":"session","version":3,"id":"multiturn01","timestamp":"2026-10-29T08:00:00.000Z","cwd":"/w"}`,
		piLine("user", "u-1", "f003", "第一轮问题"),
		piLine("assistant", "a-1", "u-1", "第一轮回答"),
		piLine("user", "u-2", "a-1", "第二轮问题"),
		piLine("assistant", "a-2", "u-2", "第二轮回答"),
	)
	granted := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "multiturn01"
		m.Escalations = []Escalation{
			{EscalationID: "esc-1", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a-1"},
			{EscalationID: "esc-2", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a-2"},
		}
	})
	if got, err := store.LastEntryID(meta.ID); err != nil || got != "a-2" {
		t.Fatalf("LastEntryID = %q, %v; want a-2", got, err)
	}
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	want := []struct {
		kind string
		text string
	}{
		{"message", "第一轮问题"}, {"message", "第一轮回答"}, {"escalation", "esc-1"},
		{"message", "第二轮问题"}, {"message", "第二轮回答"}, {"escalation", "esc-2"},
	}
	if len(entries) != len(want) {
		t.Fatalf("条目数 = %d, want %d: %+v", len(entries), len(want), entries)
	}
	for i, expect := range want {
		if entryType(entries[i]) != expect.kind {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entries[i]), expect.kind)
		}
		if expect.kind == "escalation" {
			if entries[i]["escalation_id"] != expect.text {
				t.Fatalf("entries[%d] = %+v, want %s", i, entries[i], expect.text)
			}
			continue
		}
		if got := asMap(entries[i]["message"])["content"]; got != expect.text {
			t.Fatalf("entries[%d].content = %v, want %q", i, got, expect.text)
		}
	}
}

// fork 之后旧分支条目仍在文件里但不在当前分支:旧分支的审批与失败记录被
// 丢弃,新分支的按锚点就位(branch.jsonl 的当前分支是 u201→a201→u203→a203)。
func TestForkedBranchAnchorsDroppedEndToEnd(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	granted := true
	meta = updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.Escalations = []Escalation{
			{EscalationID: "esc-old", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a202"}, // 旧分支:丢弃
			{EscalationID: "esc-new", ToolName: "write", Granted: &granted, At: fixedTime, AfterEntryID: "a203"}, // 新分支末条之后
		}
		m.RunErrors = []RunError{
			{RunID: "run-old", Message: "旧分支失败", AfterEntryID: "u202", At: fixedTime}, // 旧分支:丢弃
			{RunID: "run-new", Message: "新分支失败", AfterEntryID: "u203", At: fixedTime}, // 新分支问句之后
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatalf("BuildTranscript: %v", err)
	}
	want := []struct {
		kind string
		text string
	}{
		{"message", "主干第一问"},
		{"message", "主干回答"},
		{"message", "改走另一分支的第二问"},
		{"run_error", "新分支失败"},
		{"message", "新分支回答"},
		{"escalation", "esc-new"},
	}
	if len(entries) != len(want) {
		t.Fatalf("条目数 = %d, want %d: %+v", len(entries), len(want), entries)
	}
	for i, expect := range want {
		if entryType(entries[i]) != expect.kind {
			t.Fatalf("entries[%d].type = %s, want %s", i, entryType(entries[i]), expect.kind)
		}
		switch expect.kind {
		case "escalation":
			if entries[i]["escalation_id"] != expect.text {
				t.Fatalf("entries[%d] = %+v, want %s", i, entries[i], expect.text)
			}
		case "run_error":
			if entries[i]["message"] != expect.text {
				t.Fatalf("entries[%d] = %+v, want %s", i, entries[i], expect.text)
			}
		default:
			if got := asMap(entries[i]["message"])["content"]; got != expect.text {
				t.Fatalf("entries[%d].content = %v, want %q", i, got, expect.text)
			}
		}
	}
	// 旧分支的审批与错误不再出现
	for _, entry := range entries {
		if id, _ := entry["escalation_id"].(string); id == "esc-old" {
			t.Fatal("旧分支的审批应被丢弃")
		}
		if msg, _ := entry["message"].(string); entryType(entry) == "run_error" && msg == "旧分支失败" {
			t.Fatal("旧分支的失败记录应被丢弃")
		}
	}
}
