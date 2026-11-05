package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "重写 testdata/translated 下的 golden 文件")

const (
	piEventsDir = "../testdata/pi-events" // 样本来源与场景表见该目录 README.md
	goldenDir   = "testdata/translated"
)

// TestFeedRules 表驱动覆盖 07 §十.2 的每条翻译规则(内联小 JSON)。
func TestFeedRules(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []Event
	}{
		{
			name:  "agent_start",
			lines: []string{`{"type":"agent_start"}`},
			want:  []Event{{Type: "agent_start", Data: map[string]any{}}},
		},
		{
			name:  "text_delta",
			lines: []string{`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"你好"}}`},
			want:  []Event{{Type: "text_delta", Data: map[string]any{"delta": "你好"}}},
		},
		{
			name:  "thinking_delta",
			lines: []string{`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"想"}}`},
			want:  []Event{{Type: "thinking", Data: map[string]any{"delta": "想"}}},
		},
		{
			name: "toolcall_start 记录 contentIndex 映射供 delta 使用",
			lines: []string{
				`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":0,"id":"c1","toolName":"echo"}}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":0,"delta":"{\"a\":"}}`,
			},
			want: []Event{
				{Type: "tool_call_start", Data: map[string]any{"tool_call": map[string]any{"id": "c1", "name": "echo"}}},
				{Type: "tool_call_delta", Data: map[string]any{"tool_call_id": "c1", "delta": `{"a":`}},
			},
		},
		{
			name:  "toolcall_end 输出完整 raw_arguments 字符串",
			lines: []string{`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"id":"c1","name":"echo","arguments":{"text":"ping"}}}}`},
			want: []Event{{Type: "tool_call", Data: map[string]any{"tool_call": map[string]any{
				"id": "c1", "name": "echo", "raw_arguments": `{"text":"ping"}`,
			}}}},
		},
		{
			name: "tool_execution_start 同样输出完整 tool_call 并记住调用",
			lines: []string{
				`{"type":"tool_execution_start","toolCallId":"c1","toolName":"write","args":{"path":"out/a.txt","command":["touch","x"]}}`,
			},
			want: []Event{{Type: "tool_call", Data: map[string]any{"tool_call": map[string]any{
				"id": "c1", "name": "write", "raw_arguments": `{"command":["touch","x"],"path":"out/a.txt"}`,
			}}}},
		},
		{
			name: "tool_execution_end 文本块换行连接、图片块占位、is_error 取 isError",
			lines: []string{
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"read","isError":true,` +
					`"result":{"content":[{"type":"text","text":"a"},{"type":"image","source":"..."},{"type":"text","text":"b"}]}}`,
			},
			want: []Event{{Type: "tool_result", Data: map[string]any{"tool_result": map[string]any{
				"tool_call_id": "c1", "name": "read", "output": "a\n[image]\nb", "is_error": true,
			}}}},
		},
		{
			name: "tool_execution_end 的 result 为纯字符串时直接作为 output",
			lines: []string{
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":false,"result":"plain"}`,
			},
			want: []Event{{Type: "tool_result", Data: map[string]any{"tool_result": map[string]any{
				"tool_call_id": "c1", "name": "bash", "output": "plain", "is_error": false,
			}}}},
		},
		{
			name: "tool_execution_end 的 result.details.images 只保留白名单元数据",
			lines: []string{
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"image_generation","isError":false,` +
					`"result":{"content":[{"type":"text","text":"已生成"}],"details":{"images":[` +
					`{"path":"images/01.png","mime":"image/png","bytes":123,"model":"gpt-image-2","site":"demo","alt":"图"},` +
					`{"path":"images/../x.png","mime":"image/png"},` +
					`{"path":"images/02.png","mime":"text/plain","bytes":-1,"model":1,"alt":null}]}}}`,
			},
			want: []Event{{Type: "tool_result", Data: map[string]any{"tool_result": map[string]any{
				"tool_call_id": "c1", "name": "image_generation", "output": "已生成", "is_error": false,
				"images": []any{
					map[string]any{"path": "images/01.png", "mime": "image/png", "bytes": float64(123), "model": "gpt-image-2", "site": "demo", "alt": "图"},
					map[string]any{"path": "images/02.png", "mime": "", "bytes": float64(0), "model": "", "site": "", "alt": ""},
				},
			}}}},
		},
		{
			name: "result.details.images 不是数组时忽略图片字段",
			lines: []string{
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"x","result":{"content":[],"details":{"images":"bad"}}}`,
			},
			want: []Event{{Type: "tool_result", Data: map[string]any{"tool_result": map[string]any{
				"tool_call_id": "c1", "name": "x", "output": "", "is_error": false,
			}}}},
		},
		{
			name: "extension_ui_request 的 message 为 JSON 约定时按约定关联",
			lines: []string{
				`{"type":"tool_execution_start","toolCallId":"c1","toolName":"write","args":{"path":"out/a.txt","command":["touch","x"]}}`,
				`{"type":"extension_ui_request","id":"esc1","method":"confirm","title":"Allow write?",` +
					`"message":"{\"toolCallId\":\"c1\",\"toolName\":\"write\",\"path\":\"out/a.txt\",\"command\":[\"touch\",\"x\"],\"note\":\"n\"}"}`,
			},
			want: []Event{
				{Type: "tool_call", Data: map[string]any{"tool_call": map[string]any{
					"id": "c1", "name": "write", "raw_arguments": `{"command":["touch","x"],"path":"out/a.txt"}`,
				}}},
				{Type: "escalation_request", Data: map[string]any{"escalation": map[string]any{
					"escalation_id":     "esc1",
					"tool_name":         "write",
					"requested_path":    "out/a.txt",
					"resolved_path":     "out/a.txt",
					"requested_command": []string{"touch", "x"},
					"note":              "n",
				}}},
			},
		},
		{
			name: "message 非 JSON 时兜底取最近一个未审批工具",
			lines: []string{
				`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls"}}`,
				`{"type":"extension_ui_request","id":"esc1","method":"confirm","title":"Allow bash?","message":"toolCallId=?? note=opaque"}`,
			},
			want: []Event{
				{Type: "tool_call", Data: map[string]any{"tool_call": map[string]any{
					"id": "c1", "name": "bash", "raw_arguments": `{"command":"ls"}`,
				}}},
				{Type: "escalation_request", Data: map[string]any{"escalation": map[string]any{
					"escalation_id":     "esc1",
					"tool_name":         "bash",
					"requested_command": []string{"ls"},
				}}},
			},
		},
		{
			name: "message 非 JSON 且无可兜底工具时 tool_name 为空",
			lines: []string{
				`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{}}`,
				`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":false,"result":{"content":[]}}`,
				`{"type":"extension_ui_request","id":"esc1","method":"select","title":"pick","message":"first"}`,
			},
			want: []Event{
				{Type: "tool_call", Data: map[string]any{"tool_call": map[string]any{
					"id": "c1", "name": "bash", "raw_arguments": "{}",
				}}},
				{Type: "tool_result", Data: map[string]any{"tool_result": map[string]any{
					"tool_call_id": "c1", "name": "bash", "output": "", "is_error": false,
				}}},
				{Type: "escalation_request", Data: map[string]any{"escalation": map[string]any{
					"escalation_id": "esc1",
					"tool_name":     "",
				}}},
			},
		},
		{
			name:  "method 不是 confirm/select/editor 的 extension_ui_request 被忽略",
			lines: []string{`{"type":"extension_ui_request","id":"esc1","method":"input","title":"t","message":"m"}`},
			want:  nil,
		},
		{
			name:  "compaction_start manual",
			lines: []string{`{"type":"compaction_start","reason":"manual"}`},
			want:  []Event{{Type: "context_compacting", Data: map[string]any{"manual": true}}},
		},
		{
			name:  "compaction_start 非 manual",
			lines: []string{`{"type":"compaction_start","reason":"overflow"}`},
			want:  []Event{{Type: "context_compacting", Data: map[string]any{"manual": false}}},
		},
		{
			name: "compaction_end 带 result 时补 tokens_before/tokens_after",
			lines: []string{
				`{"type":"compaction_end","reason":"manual","result":{"summary":"s","tokensBefore":16,` +
					`"estimatedTokensAfter":587,"firstKeptEntryId":"ab"}}`,
			},
			want: []Event{{Type: "context_compacted", Data: map[string]any{"compaction": map[string]any{
				"summary": "s", "firstKeptEntryId": "ab",
				"tokensBefore": float64(16), "estimatedTokensAfter": float64(587),
				"tokens_before": float64(16), "tokens_after": float64(587),
			}}}},
		},
		{
			name:  "compaction_end 无 result 时载荷为空(前端标为 skipped)",
			lines: []string{`{"type":"compaction_end","reason":"manual"}`},
			want:  []Event{{Type: "context_compacted", Data: map[string]any{}}},
		},
		{
			name: "assistant message_end stopReason=error 在 agent_end(willRetry=false) 时输出 agent_error.message",
			lines: []string{
				`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"boom"}}`,
				`{"type":"agent_end","messages":[],"willRetry":false}`,
			},
			want: []Event{{Type: "agent_error", Data: map[string]any{"message": "boom"}}},
		},
		{
			name: "error 后 willRetry=true 不是终态:丢弃错误,auto_retry_start 给进度提示,重试成功后 agent_done",
			lines: []string{
				`{"type":"agent_start"}`,
				`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"503"}}`,
				`{"type":"agent_end","messages":[],"willRetry":true}`,
				`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"503"}`,
				`{"type":"agent_start"}`,
				`{"type":"agent_end","messages":[],"willRetry":false}`,
				`{"type":"agent_settled","aborted":false}`,
			},
			want: []Event{
				{Type: "agent_start", Data: map[string]any{}},
				{Type: "agent_notice", Data: map[string]any{"notice": "上游暂时出错,正在自动重试(第 1/3 次)"}},
				{Type: "agent_start", Data: map[string]any{}},
				{Type: "agent_done", Data: map[string]any{}},
			},
		},
		{
			name: "没见到 agent_end 就 agent_settled:暂存的错误作为终态发出,且不再发 agent_done",
			lines: []string{
				`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"boom"}}`,
				`{"type":"agent_settled","aborted":false}`,
			},
			want: []Event{{Type: "agent_error", Data: map[string]any{"message": "boom"}}},
		},
		{
			name: "非 assistant 或非 error 的 message_end 不输出",
			lines: []string{
				`{"type":"message_end","message":{"role":"user","stopReason":"stop"}}`,
				`{"type":"message_end","message":{"role":"toolResult","isError":true}}`,
				`{"type":"message_end","message":{"role":"assistant","stopReason":"aborted","errorMessage":"Request was aborted"}}`,
			},
			want: nil,
		},
		{
			name:  "agent_settled aborted=true 输出 agent_cancelled",
			lines: []string{`{"type":"agent_settled","aborted":true}`},
			want:  []Event{{Type: "agent_cancelled", Data: map[string]any{"aborted": true}}},
		},
		{
			name:  "agent_settled aborted=false 输出 agent_done",
			lines: []string{`{"type":"agent_settled","aborted":false}`},
			want:  []Event{{Type: "agent_done", Data: map[string]any{}}},
		},
		{
			name: "agent_error 之后的 agent_settled 不再发 agent_done,下一轮 agent_start 恢复",
			lines: []string{
				`{"type":"agent_start"}`,
				`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"boom"}}`,
				`{"type":"agent_end","messages":[],"willRetry":false}`,
				`{"type":"agent_settled","aborted":false}`,
				`{"type":"agent_start"}`,
				`{"type":"agent_settled","aborted":false}`,
			},
			want: []Event{
				{Type: "agent_start", Data: map[string]any{}},
				{Type: "agent_error", Data: map[string]any{"message": "boom"}},
				{Type: "agent_start", Data: map[string]any{}},
				{Type: "agent_done", Data: map[string]any{}},
			},
		},
		{
			name: "忽略清单",
			lines: []string{
				`{"type":"response","id":"req_1","command":"get_state"}`,
				`{"type":"auto_retry_end"}`,
				`{"type":"entry_appended"}`,
				`{"type":"queue_update"}`,
				`{"type":"turn_start"}`,
				`{"type":"turn_end","message":{}}`,
				`{"type":"agent_end","messages":[]}`,
				`{"type":"message_start","message":{}}`,
				`{"type":"tool_execution_update","toolCallId":"c1"}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
				`{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"x"}}`,
			},
			want: nil,
		},
		{
			name:  "未知类型",
			lines: []string{`{"type":"mystery_thing","payload":1}`},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := New()
			var got []Event
			for _, line := range tt.lines {
				events, err := tr.Feed([]byte(line))
				if err != nil {
					t.Fatalf("Feed(%s) 意外出错: %v", line, err)
				}
				got = append(got, events...)
			}
			assertEvents(t, got, tt.want)
		})
	}
}

// TestFeedRobustness:非法 JSON、空行、无映射的 toolcall_delta 都不 panic,按约定返回。
func TestToolExecutionEditPatch(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		isError  bool
		patch    string
		want     string
	}{
		{name: "edit 成功", toolName: "edit", patch: "--- a\n+++ a\n@@ -1 +1 @@\n-a\n+b\n", want: "--- a\n+++ a\n@@ -1 +1 @@\n-a\n+b\n"},
		{name: "edit 失败", toolName: "edit", isError: true, patch: "--- a\n+++ a\n"},
		{name: "patch 超限", toolName: "edit", patch: strings.Repeat("+", 200<<10+1)},
		{name: "非 edit", toolName: "write", patch: "--- a\n+++ a\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, err := json.Marshal(map[string]any{
				"type": "tool_execution_end", "toolCallId": "c1", "toolName": tt.toolName,
				"isError": tt.isError, "result": map[string]any{"details": map[string]any{"patch": tt.patch}},
			})
			if err != nil {
				t.Fatal(err)
			}
			events, err := New().Feed(line)
			if err != nil {
				t.Fatal(err)
			}
			result, _ := events[0].Data["tool_result"].(map[string]any)
			got, has := result["details_patch"]
			if tt.want == "" {
				if has {
					t.Fatalf("details_patch = %v, want 省略", got)
				}
			} else if got != tt.want {
				t.Fatalf("details_patch = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestFeedRobustness(t *testing.T) {
	tr := New()

	if _, err := tr.Feed([]byte(`{"type":"agent_start"`)); err == nil {
		t.Error("非法 JSON 应返回 error")
	}
	for _, line := range []string{"", "   ", "\t", "null", `{"no_type":true}`} {
		events, err := tr.Feed([]byte(line))
		if err != nil || events != nil {
			t.Errorf("Feed(%q) = (%v, %v), want (nil, nil)", line, events, err)
		}
	}
	events, err := tr.Feed([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":7,"delta":"x"}}`))
	if err != nil || events != nil {
		t.Errorf("无映射的 toolcall_delta = (%v, %v), want (nil, nil)", events, err)
	}
}

func TestDecision(t *testing.T) {
	tr := New()
	got := tr.Decision("esc-1", true)
	want := Event{Type: "permission_result", Data: map[string]any{"request_id": "esc-1", "allowed": true}}
	assertEvents(t, []Event{got}, []Event{want})
}

// AutoDecision 与 Decision 同形,额外带 auto 标记(会话内允许自动放行)。
func TestAutoDecision(t *testing.T) {
	tr := New()
	got := tr.AutoDecision("esc-2", true)
	want := Event{Type: "permission_result", Data: map[string]any{"request_id": "esc-2", "allowed": true, "auto": true}}
	assertEvents(t, []Event{got}, []Event{want})
	// 自动拒绝(理论路径)同样带标记;原 Decision 事件形状不变。
	deny := tr.Decision("esc-3", false)
	assertEvents(t, []Event{deny}, []Event{{Type: "permission_result", Data: map[string]any{"request_id": "esc-3", "allowed": false}}})
}

func TestParseApprovalMessage(t *testing.T) {
	approval, ok := ParseApprovalMessage(`{"toolCallId":"c1","toolName":"write","path":"out/a.txt","command":["touch","x"],"note":"n"}`)
	if !ok {
		t.Fatal("JSON message 应解析成功")
	}
	want := Approval{ToolCallID: "c1", ToolName: "write", Path: "out/a.txt", Command: []string{"touch", "x"}, Note: "n"}
	if !reflect.DeepEqual(approval, want) {
		t.Errorf("approval = %+v, want %+v", approval, want)
	}

	for _, message := range []string{"", "first", "toolCallId=c1 note=opaque", "{not json"} {
		if _, ok := ParseApprovalMessage(message); ok {
			t.Errorf("ParseApprovalMessage(%q) 应返回 ok=false", message)
		}
	}
}

// expectedSequences 是 14 个样本回放后期望的事件类型序列(含按文件名约定注入的
// permission_result),依据草稿翻译输出(translated-*.jsonl)对照后人工确认。
// 终态与压缩事件名与前端 agent-workspace 的精确匹配清单对齐(agent_done/agent_error/
// agent_cancelled、context_compacting/context_compacted),见 TestWorkspaceTerminalNamesLocked。
var expectedSequences = map[string][]string{
	"basic": expand("agent_start", "text_delta*8", "agent_done"),
	// Pi 自动重试 3 次:前三次失败只给进度提示,只有最后一次才是 agent_error 终态
	"err-500": expand(
		"agent_start", "agent_notice",
		"agent_start", "agent_notice",
		"agent_start", "agent_notice",
		"agent_start", "agent_error"),
	"tool-echo": expand("agent_start", "tool_call_start", "tool_call_delta*2", "tool_call*2", "tool_result", "text_delta*4", "agent_done"),
	"tool-parallel": expand("agent_start",
		"tool_call_start", "tool_call_delta*4",
		"tool_call_start", "tool_call_delta*4",
		"tool_call*4", "tool_result*2", "text_delta*3", "agent_done"),
	"tool-unknown-bash": expand("agent_start", "tool_call_start", "tool_call_delta*20", "tool_call*2", "tool_result", "text_delta", "agent_done"),
	"approval-allow":    expand("agent_start", "tool_call_start", "tool_call_delta*2", "tool_call*2", "escalation_request", "permission_result", "tool_result", "text_delta*2", "agent_done"),
	"approval-deny":     expand("agent_start", "tool_call_start", "tool_call_delta*2", "tool_call*2", "escalation_request", "permission_result", "tool_result", "text_delta*2", "agent_done"),
	"approval-timeout":  expand("agent_start", "tool_call_start", "tool_call_delta*4", "tool_call*2", "escalation_request", "permission_result", "tool_result", "text_delta*2", "agent_done"),
	"approval-dual": expand("agent_start", "tool_call_start", "tool_call_delta*4", "tool_call*2",
		"escalation_request", "permission_result",
		"escalation_request", "permission_result",
		"tool_result", "text_delta*3", "agent_done"),
	"approval-abort": expand("agent_start", "tool_call_start", "tool_call_delta*3",
		"tool_call*2", "escalation_request", "tool_result", "agent_error", "agent_cancelled",
		"agent_start", "text_delta*2", "agent_done"),
	"compact-manual": expand(
		"agent_start", "text_delta*14", "agent_done",
		"agent_start", "text_delta*12", "agent_done",
		"context_compacting", "context_compacted",
		"agent_start", "text_delta*7", "agent_done"),
	"compact-overflow": expand(
		"agent_start", "text_delta*7", "agent_done",
		"agent_start", "agent_error", "context_compacting", "context_compacted",
		"agent_start", "text_delta*7", "agent_done"),
	"abort-in-tool":   expand("agent_start", "tool_call_start", "tool_call_delta*2", "tool_call*2", "tool_result", "agent_error", "agent_cancelled"),
	"abort-in-stream": expand("agent_start", "text_delta*7", "agent_cancelled"),
}

// decisionsFor 按文件名约定返回每个 escalation_request 依次对应的审批决定
// (见 testdata/pi-events/README.md):allow → true;deny/timeout → false;
// dual → 第一个 false、第二个 true;abort → 不注入决定。
func decisionsFor(sample string) ([]bool, bool) {
	switch sample {
	case "approval-allow":
		return []bool{true}, true
	case "approval-deny", "approval-timeout":
		return []bool{false}, true
	case "approval-dual":
		return []bool{false, true}, true
	default:
		return nil, false
	}
}

// TestFixtureReplay 回放 14 个 Pi 原始事件样本:断言类型序列与终态,
// 并与 testdata/translated 下的 golden 逐行对比(-update 重新生成)。
func TestFixtureReplay(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(piEventsDir, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) != len(expectedSequences) {
		t.Fatalf("样本数 = %d, 期望序列条目数 = %d", len(paths), len(expectedSequences))
	}

	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		expected, ok := expectedSequences[name]
		if !ok {
			t.Fatalf("样本 %s 缺少期望序列", name)
		}
		t.Run(name, func(t *testing.T) {
			events := replaySample(t, path, name)

			got := make([]string, len(events))
			for i, e := range events {
				got[i] = e.Type
			}
			if !reflect.DeepEqual(got, expected) {
				t.Errorf("事件类型序列不符:\n got  %v\n want %v", got, expected)
			}
			assertSampleInvariants(t, name, events)
			assertTerminalInvariants(t, name, events)
			checkGolden(t, name, events)
		})
	}
}

func replaySample(t *testing.T, path, name string) []Event {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	// 单行可能很长(整段 system prompt 塞在一行里),加大缓冲区避免截断
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	decisions, inject := decisionsFor(name)
	next := 0
	lineNo := 0
	tr := New()
	var events []Event
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		batch, err := tr.Feed(line)
		if err != nil {
			t.Fatalf("Feed 第 %d 行: %v", lineNo, err)
		}
		events = append(events, batch...)
		if !inject {
			continue
		}
		for _, e := range batch {
			if e.Type != "escalation_request" {
				continue
			}
			if next >= len(decisions) {
				t.Fatalf("escalation_request 多于预期(第 %d 个)", next+1)
			}
			escalation, _ := e.Data["escalation"].(map[string]any)
			id, _ := escalation["escalation_id"].(string)
			events = append(events, tr.Decision(id, decisions[next]))
			next++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return events
}

// assertSampleInvariants 落实任务要求的每样本终态断言(独立于序列表,双保险)。
func assertSampleInvariants(t *testing.T, name string, events []Event) {
	t.Helper()

	if len(events) == 0 {
		t.Fatal("没有产生任何事件")
	}
	for _, e := range events {
		if e.Data == nil {
			t.Fatalf("事件 %s 的 Data 为 nil,应恒为非 nil map", e.Type)
		}
	}

	if strings.HasPrefix(name, "approval-") {
		count := 0
		for _, e := range events {
			if e.Type != "escalation_request" {
				continue
			}
			count++
			escalation, _ := e.Data["escalation"].(map[string]any)
			if toolName, _ := escalation["tool_name"].(string); toolName == "" {
				t.Error("escalation_request 的 tool_name 为空")
			}
		}
		if count == 0 {
			t.Error("缺少 escalation_request 事件")
		}
	}

	switch name {
	case "basic":
		if last := events[len(events)-1]; last.Type != "agent_done" {
			t.Errorf("最后事件 = %s, want agent_done", last.Type)
		}
	case "err-500":
		hasError := false
		for _, e := range events {
			switch e.Type {
			case "agent_error":
				hasError = true
				if msg, _ := e.Data["message"].(string); msg == "" {
					t.Error("agent_error 事件的 data.message 为空")
				}
			case "agent_done":
				t.Error("err-500 不应输出 agent_done(agent_error 已收口本轮)")
			}
		}
		if !hasError {
			t.Error("缺少 agent_error 事件")
		}
	case "abort-in-tool", "abort-in-stream", "approval-abort":
		if !hasType(events, "agent_cancelled") {
			t.Error("缺少 agent_cancelled 事件")
		}
	case "tool-parallel":
		ids := map[string]bool{}
		for _, e := range events {
			if e.Type != "tool_call_start" {
				continue
			}
			call, _ := e.Data["tool_call"].(map[string]any)
			id, _ := call["id"].(string)
			if id == "" {
				t.Fatal("tool_call_start 缺 id")
			}
			ids[id] = true
		}
		if len(ids) != 2 {
			t.Errorf("不同 id 的 tool_call_start 数 = %d, want 2", len(ids))
		}
		for _, e := range events {
			if e.Type != "tool_result" {
				continue
			}
			result, _ := e.Data["tool_result"].(map[string]any)
			id, _ := result["tool_call_id"].(string)
			if !ids[id] {
				t.Errorf("tool_result 的 tool_call_id %q 对不上任何 tool_call_start", id)
			}
		}
	}
}

func hasType(events []Event, typ string) bool {
	for _, e := range events {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// 终态事件名:前端 agent-workspace 对这三个用精确匹配
// (['agent_done','agent_error','agent_cancelled'].includes(event.type))
// 命中才 setRunning(false) 并拉 transcript 对账。
var terminalEventTypes = map[string]bool{
	"agent_done":      true,
	"agent_error":     true,
	"agent_cancelled": true,
}

// 旧裸名(reducer 子串匹配也能认,但 workspace 精确匹配不到)不允许再出现。
var legacyEventTypes = map[string]bool{
	"done": true, "error": true, "cancelled": true,
	"compacting": true, "compacted": true,
}

// assertTerminalInvariants 落实「终态事件 ∈ 三者之一且最多一个」:
// 07 §十.2 规定 error 之后 agent_settled{aborted:true} 仍会补 cancelled
// (approval-abort/abort-in-tool 的 run1 因此合法地 agent_error+agent_cancelled 并存),
// 所以「最多一个」按对齐前端行为的口径落实为:
//  1. 整条回放以终态事件收尾(14 个样本都满足,workspace 依赖它收 running);
//  2. 每个 run 段(agent_start 分界)内 agent_done 至多一个,且不与
//     agent_error/agent_cancelled 同段共存(失败后绝不补 done);
//  3. 不得出现旧裸名。
func assertTerminalInvariants(t *testing.T, name string, events []Event) {
	t.Helper()

	for _, e := range events {
		if legacyEventTypes[e.Type] {
			t.Errorf("出现了旧事件名 %s(前端 workspace 精确匹配不到)", e.Type)
		}
	}

	if last := events[len(events)-1]; !terminalEventTypes[last.Type] {
		t.Errorf("最后事件 %s 不是终态事件(agent_done/agent_error/agent_cancelled)", last.Type)
	}

	// 按 agent_start 分段检查终态搭配
	var done, failed bool
	reset := func() { done, failed = false, false }
	reset()
	for _, e := range events {
		if e.Type == "agent_start" {
			reset()
			continue
		}
		switch {
		case e.Type == "agent_done":
			done = true
		case e.Type == "agent_error" || e.Type == "agent_cancelled":
			failed = true
		}
		if done && failed {
			t.Errorf("同一 run 段内 agent_done 与 agent_error/agent_cancelled 同时出现")
			return
		}
	}
}

// TestWorkspaceTerminalNamesLocked 防前后端漂移:前端 agent-workspace 对终态事件
// 用精确匹配 ['agent_done','agent_error','agent_cancelled'],后端改名必须同步。
// 读不到前端源码(如 CI 裁剪 web/)时跳过。
func TestWorkspaceTerminalNamesLocked(t *testing.T) {
	const workspace = "../../../../web/src/features/agent/components/agent-workspace.tsx"
	source, err := os.ReadFile(workspace)
	if err != nil {
		t.Skipf("读不到前端工作区源码(%v),跳过防漂移检查", err)
	}
	const terminalList = "'agent_done', 'agent_error', 'agent_cancelled'"
	if !strings.Contains(string(source), terminalList) {
		t.Errorf("agent-workspace.tsx 不再包含精确匹配串 %s,后端终态事件名需要同步复核", terminalList)
	}
}

func checkGolden(t *testing.T, name string, events []Event) {
	t.Helper()

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	for _, e := range events {
		if err := encoder.Encode(e); err != nil {
			t.Fatalf("编码事件 %s: %v", e.Type, err)
		}
	}

	goldenPath := filepath.Join(goldenDir, name+".golden.jsonl")
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", goldenDir, err)
		}
		if err := os.WriteFile(goldenPath, buffer.Bytes(), 0o644); err != nil {
			t.Fatalf("写 golden %s: %v", goldenPath, err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读 golden 失败(先用 go test -update 生成): %v", err)
	}
	if bytes.Equal(bytes.TrimRight(want, "\n"), bytes.TrimRight(buffer.Bytes(), "\n")) {
		return
	}
	wantLines := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var wantLine, gotLine string
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine != gotLine {
			t.Errorf("golden 第 %d 行不符:\n golden %s\n got    %s", i+1, wantLine, gotLine)
			return
		}
	}
}

func assertEvents(t *testing.T, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("事件数 = %d, want %d\n got %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Type != want[i].Type {
			t.Errorf("第 %d 个事件类型 = %s, want %s", i, got[i].Type, want[i].Type)
			continue
		}
		if got[i].Data == nil {
			t.Errorf("第 %d 个事件(%s)的 Data 为 nil", i, got[i].Type)
			continue
		}
		if !reflect.DeepEqual(got[i].Data, want[i].Data) {
			t.Errorf("第 %d 个事件(%s)载荷不符:\n got  %s\n want %s",
				i, got[i].Type, mustJSON(got[i].Data), mustJSON(want[i].Data))
		}
	}
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

// expand 把 "text_delta*8" 形式的简写展开成完整类型序列。
func expand(items ...string) []string {
	var out []string
	for _, item := range items {
		typ, count, found := strings.Cut(item, "*")
		if !found {
			out = append(out, typ)
			continue
		}
		n := 0
		if _, err := fmt.Sscanf(count, "%d", &n); err != nil || n < 0 {
			panic("translate 测试:非法的期望序列简写 " + item)
		}
		for i := 0; i < n; i++ {
			out = append(out, typ)
		}
	}
	return out
}
