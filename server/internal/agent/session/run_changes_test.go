package session

import (
	"fmt"
	"testing"
)

func TestUpsertRunChange(t *testing.T) {
	var meta Meta
	meta.UpsertRunChange(RunChange{TurnID: "turn", RunIDs: []string{"r1"}, Total: 1})
	meta.UpsertRunChange(RunChange{TurnID: "turn", RunIDs: []string{"r1", "r2"}, Total: 2})
	if len(meta.RunChanges) != 1 || meta.RunChanges[0].Total != 2 || fmt.Sprint(meta.RunChanges[0].RunIDs) != "[r1 r2]" {
		t.Fatalf("替换失败: %+v", meta.RunChanges)
	}
	for i := 0; i < MaxRunChanges+1; i++ {
		meta.UpsertRunChange(RunChange{TurnID: fmt.Sprintf("t-%d", i)})
	}
	if len(meta.RunChanges) != MaxRunChanges || meta.RunChanges[0].TurnID != "t-1" {
		t.Fatalf("上限失败: %d %s", len(meta.RunChanges), meta.RunChanges[0].TurnID)
	}
	meta.RemoveRunChange("t-1")
	if len(meta.RunChanges) != MaxRunChanges-1 {
		t.Fatal("删除失败")
	}
}

func TestRunChangeTranscript(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.Escalations = []Escalation{{EscalationID: "e", AfterEntryID: "a201", At: fixedTime}}
		m.RunErrors = []RunError{{RunID: "r", AfterEntryID: "a201", At: fixedTime}}
		m.RunChanges = []RunChange{
			{TurnID: "keep", RunIDs: []string{"r1"}, AfterEntryID: "a201", At: fixedTime},
			{TurnID: "old", AfterEntryID: "a202", At: fixedTime},
			{TurnID: "tail", AfterEntryID: "", At: fixedTime, Incomplete: true},
		}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, entry := range entries {
		types = append(types, entry["type"].(string))
		if entry["turn_id"] == "old" {
			t.Fatal("旧分支变更未丢弃")
		}
	}
	for i, entry := range entries {
		if entry["turn_id"] == "keep" {
			if i < 2 || entries[i-1]["type"] != "run_error" || entries[i-2]["type"] != "escalation" {
				t.Fatalf("同锚点顺序错误: %v", types)
			}
		}
	}
	if entries[len(entries)-1]["turn_id"] != "tail" {
		t.Fatal("空锚点变更未追加末尾")
	}
}

func TestRunChangeNotReplayedWithoutPi(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.RunChanges = []RunChange{{TurnID: "t"}}
	})
	entries, err := store.BuildTranscript(meta.ID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("无 Pi 文件不应回放: %#v %v", entries, err)
	}
}

func TestForkRunChanges(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	writePiFile(t, store, meta.ID, "branch.jsonl", "branchess001")
	updateMeta(t, store, meta.ID, func(m *Meta) {
		m.PiSessionID = "branchess001"
		m.RunChanges = []RunChange{
			{TurnID: "keep", AfterEntryID: "a201"},
			{TurnID: "old", AfterEntryID: "a202"},
			{TurnID: "tail"},
		}
	})
	forked, err := store.Fork(meta.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(forked.RunChanges) != 1 || forked.RunChanges[0].TurnID != "keep" {
		t.Fatalf("fork 变更复制错误: %+v", forked.RunChanges)
	}
}

func TestOldMetaWithoutRunChanges(t *testing.T) {
	store := newTestStore(t)
	meta := mustCreate(t, store)
	path := store.Dir(meta.ID) + "/meta.json"
	data := []byte(`{"id":"` + meta.ID + `","title":"old"}`)
	if err := writeFileSync(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(meta.ID)
	if err != nil || got.RunChanges == nil {
		t.Fatalf("旧 meta 兼容失败: %#v %v", got, err)
	}
}
