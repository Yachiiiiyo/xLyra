package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 回放夹具完整性:testdata/pi-events 下的每个样本都必须是逐行 JSON 对象的事件流,
// 且至少包含一行 agent_start。夹具来源与场景见 testdata/pi-events/README.md。
func TestPiEventFixturesAreValidEventStreams(t *testing.T) {
	const dir = "testdata/pi-events"

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}

	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		checked++
		verifyEventStream(t, filepath.Join(dir, entry.Name()))
	}

	// 样本只会增不会减;低于该数说明夹具被误删。
	if want := 14; checked < want {
		t.Errorf("fixture count = %d, want >= %d", checked, want)
	}
}

func verifyEventStream(t *testing.T, path string) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Errorf("%s: open: %v", path, err)
		return
	}
	defer file.Close()

	// 单行可能很长(整段 system prompt 塞在一行里),加大缓冲区避免 Scanner 截断。
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	hasAgentStart := false
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Errorf("%s:%d: invalid JSON line: %v", path, lineNo, err)
			continue
		}
		if event.Type == "" {
			t.Errorf("%s:%d: missing non-empty string field \"type\"", path, lineNo)
			continue
		}
		if event.Type == "agent_start" {
			hasAgentStart = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Errorf("%s: scan: %v", path, err)
		return
	}

	// 每个样本都是完整会话,必然以 agent_start 开场(已逐一 grep 确认,无例外)。
	if !hasAgentStart {
		t.Errorf("%s: no \"agent_start\" event in stream", path)
	}
}
