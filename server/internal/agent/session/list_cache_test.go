package session

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func listItemsUncached(s *Store, running func(id string) bool) ([]ListItem, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	items := make([]ListItem, 0, len(metas))
	for _, meta := range metas {
		item := ListItem{
			SessionID: meta.ID,
			UpdatedAt: meta.UpdatedAt.UnixMilli(),
		}
		if meta.Title != "" {
			item.Title = meta.Title
		} else {
			item.Title = TitleFrom(firstUserText(meta))
		}
		item.Preview = s.Preview(meta)
		item.SessionAllow = meta.SessionAllow
		if item.SessionAllow == nil {
			item.SessionAllow = []string{}
		}
		if running != nil {
			item.Running = running(meta.ID)
		}
		items = append(items, item)
	}
	return items, nil
}

func setListUpdatedAt(t *testing.T, store *Store, id string, updatedAt time.Time) {
	t.Helper()
	meta, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	meta.UpdatedAt = updatedAt
	if err := store.writeMeta(id, meta); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}
}

func TestListItemsCacheMatchesUncached(t *testing.T) {
	store := newTestStore(t)
	a := mustCreate(t, store)
	b := mustCreate(t, store)
	c := mustCreate(t, store)
	writeLines(t, store, a.ID, "cacheeq01", piLine("assistant", "a1", "", "缓存等价预览"))
	updateMeta(t, store, a.ID, func(m *Meta) {
		m.Messages = []UserMessage{{Text: "  "}, {Text: "第一条 用户消息\n第二行"}}
	})
	updateMeta(t, store, b.ID, func(m *Meta) {
		m.Title = "自定义标题"
		m.SessionAllow = []string{"write", "bash"}
	})
	setListUpdatedAt(t, store, a.ID, fixedTime)
	setListUpdatedAt(t, store, b.ID, fixedTime)
	setListUpdatedAt(t, store, c.ID, fixedTime.Add(time.Minute))

	runningA := func(id string) bool { return id == a.ID }
	want, err := listItemsUncached(store, runningA)
	if err != nil {
		t.Fatalf("listItemsUncached: %v", err)
	}
	got, err := store.ListItems(runningA)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("首次结果不等价:\ngot  %+v\nwant %+v", got, want)
	}
	got, err = store.ListItems(runningA)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("缓存结果不等价: %v\ngot  %+v\nwant %+v", err, got, want)
	}

	runningB := func(id string) bool { return id == b.ID }
	want, err = listItemsUncached(store, runningB)
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.ListItems(runningB)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Running 未重新计算: %v\ngot  %+v\nwant %+v", err, got, want)
	}
	for i := range got {
		if got[i].SessionID == b.ID {
			got[i].Title = "调用方改写"
			got[i].SessionAllow[0] = "调用方改写"
		}
	}
	got, err = store.ListItems(runningB)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("返回值与缓存共享引用: %v\ngot  %+v\nwant %+v", err, got, want)
	}
}

func TestListItemsCacheInvalidation(t *testing.T) {
	store := newTestStore(t)
	a := mustCreate(t, store)
	b := mustCreate(t, store)
	writeLines(t, store, a.ID, "cacheinv01", piLine("assistant", "a1", "", "初始预览"))
	updateMeta(t, store, a.ID, func(m *Meta) {
		m.Messages = []UserMessage{{Text: "原始问题"}}
		m.SessionAllow = []string{"write"}
	})

	oldReadMeta := listReadMeta
	var reads atomic.Int64
	listReadMeta = func(path string) (Meta, error) {
		reads.Add(1)
		return readMeta(path)
	}
	t.Cleanup(func() { listReadMeta = oldReadMeta })

	if _, err := store.ListItems(nil); err != nil {
		t.Fatalf("首次 ListItems: %v", err)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("首次 readMeta 次数 = %d, want 2", got)
	}
	if _, err := store.ListItems(nil); err != nil {
		t.Fatalf("第二次 ListItems: %v", err)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("未变化时新增 readMeta 次数 = %d, want 0", got-2)
	}

	updateMeta(t, store, a.ID, func(m *Meta) { m.Title = "更新后的标题，长度也已变化" })
	items, err := store.ListItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("标题更新后 readMeta 次数 = %d, want 3", got)
	}
	if items[0].SessionID != a.ID || items[0].Title != "更新后的标题，长度也已变化" {
		t.Fatalf("标题更新未生效: %+v", items)
	}

	updateMeta(t, store, a.ID, func(m *Meta) {
		m.Title = ""
		m.Messages = append([]UserMessage{{Text: "新的首条消息"}}, m.Messages...)
		m.SessionAllow = append(m.SessionAllow, "bash")
	})
	items, err = store.ListItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got != 4 {
		t.Fatalf("消息更新后 readMeta 次数 = %d, want 4", got)
	}
	if items[0].Title != "新的首条消息" || !reflect.DeepEqual(items[0].SessionAllow, []string{"write", "bash"}) {
		t.Fatalf("消息或允许列表更新未生效: %+v", items[0])
	}

	piPath, err := PiSessionFile(store.PiDir(a.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(piPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(piLine("assistant", "a2", "a1", "追加后的预览") + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got != 5 {
		t.Fatalf("Pi 更新后 readMeta 次数 = %d, want 5", got)
	}
	if items[0].Preview != "追加后的预览" {
		t.Fatalf("Pi 追加后 Preview = %q", items[0].Preview)
	}

	if err := store.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SessionID != b.ID {
		t.Fatalf("删除后列表 = %+v", items)
	}
	if got := reads.Load(); got != 5 {
		t.Fatalf("删除后多读了 meta: %d", got)
	}
	value, ok := listCaches.Load(store.root)
	if !ok {
		t.Fatal("缺少根目录缓存")
	}
	cache := value.(*listCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 1 {
		t.Fatalf("缓存项数 = %d, want 1", len(cache.entries))
	}
	if _, ok := cache.entries[a.ID]; ok {
		t.Fatal("已删除会话仍在缓存中")
	}
	if _, ok := cache.entries[b.ID]; !ok {
		t.Fatal("现存会话缓存被误删")
	}
}

func TestListItemsCacheConcurrent(t *testing.T) {
	store := newTestStore(t)
	ids := make([]string, 3)
	for i := range ids {
		meta := mustCreate(t, store)
		ids[i] = meta.ID
		updateMeta(t, store, meta.ID, func(m *Meta) {
			m.SessionAllow = []string{"write"}
		})
	}

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				items, err := store.ListItems(func(id string) bool { return id == ids[0] })
				if err != nil {
					t.Errorf("ListItems: %v", err)
					return
				}
				for i := range items {
					if items[i].SessionAllow == nil {
						t.Errorf("SessionAllow 为 nil: %+v", items[i])
						return
					}
					if len(items[i].SessionAllow) > 0 {
						items[i].SessionAllow[0] = "调用方改写"
					}
				}
			}
		}()
	}
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := range 25 {
				if _, err := store.Update(id, func(m *Meta) error {
					m.Title = fmt.Sprintf("并发标题-%d", i)
					m.Messages = append(m.Messages, UserMessage{Text: fmt.Sprintf("消息-%d", i)})
					return nil
				}); err != nil {
					t.Errorf("Update: %v", err)
					return
				}
			}
		}(id)
	}
	wg.Wait()

	got, err := store.ListItems(nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := listItemsUncached(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("并发写入后的结果不等价:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestListItemsCacheMissingRoot(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	if _, err := store.ListItems(nil); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store.root); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("根目录删除后: %+v, %v", items, err)
	}
	value, _ := listCaches.Load(store.root)
	cache := value.(*listCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 0 {
		t.Fatalf("根目录删除后缓存未清空: %+v", cache.entries)
	}
	if _, err := store.Get(meta.ID); err == nil {
		t.Fatal("测试会话应已删除")
	}
}

func BenchmarkListItemsCache200(b *testing.B) {
	store := NewStore(b.TempDir())
	for i := range 200 {
		meta, err := store.Create("xlyra/mock-1", "ask")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := store.Update(meta.ID, func(m *Meta) error {
			m.Title = fmt.Sprintf("会话-%03d", i)
			m.Messages = []UserMessage{{Text: strings.Repeat("较长的历史消息", 1024)}}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.Cleanup(func() { listCaches.Delete(store.root) })

	var cold, warm time.Duration
	b.ResetTimer()
	for range b.N {
		listCaches.Delete(store.root)
		start := time.Now()
		if _, err := store.ListItems(nil); err != nil {
			b.Fatal(err)
		}
		cold += time.Since(start)
		start = time.Now()
		if _, err := store.ListItems(nil); err != nil {
			b.Fatal(err)
		}
		warm += time.Since(start)
	}
	b.ReportMetric(float64(cold.Microseconds())/float64(b.N), "cold_us")
	b.ReportMetric(float64(warm.Microseconds())/float64(b.N), "warm_us")
}

func TestListItemsCacheSkipsBrokenMeta(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	if _, err := store.ListItems(nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(meta.ID), metaFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("坏 meta 未跳过: %+v, %v", items, err)
	}
	value, _ := listCaches.Load(store.root)
	cache := value.(*listCache)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 0 {
		t.Fatalf("坏 meta 缓存未删除: %+v", cache.entries)
	}
}
