// question 工具的翻译测试:question_request 事件形态、非法 prefill 不发事件
// 且给出取消信号、其他 editor 请求的取消信号、QuestionAnswered 各形态。
package translate

import (
	"encoding/json"
	"reflect"
	"testing"

	"xlyra/server/internal/agent/session"
)

const validPrefill = `{"toolCallId":"call_1","questions":[` +
	`{"header":"方案","question":"选哪个方案?","multiSelect":false,` +
	`"options":[{"label":"A 方案","description":"首选"},{"label":"B 方案"}]}]}`

// editorLine 拼一条 editor 请求事件(prefill 是 JSON 字符串,需整体转义)。
func editorLine(id, title, prefill string) string {
	encoded, err := json.Marshal(prefill)
	if err != nil {
		panic(err)
	}
	return `{"type":"extension_ui_request","id":"` + id + `","method":"editor","title":"` +
		title + `","prefill":` + string(encoded) + `}`
}

// assertEventJSON 用序列化后的 JSON 比对事件(绕开 map/slice 具体类型差异)。
func assertEventJSON(t *testing.T, got, want Event) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化 got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("序列化 want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("事件不符:\n got  %s\n want %s", gotJSON, wantJSON)
	}
}

func TestQuestionRequestValid(t *testing.T) {
	tr := New()
	events, err := tr.Feed([]byte(editorLine("q1", "xlyra:question:call_1", validPrefill)))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	want := Event{Type: "question_request", Data: map[string]any{"question": map[string]any{
		"question_id":  "q1",
		"tool_call_id": "call_1",
		"questions": []map[string]any{{
			"header": "方案", "question": "选哪个方案?", "multiSelect": false,
			"options": []map[string]any{
				{"label": "A 方案", "description": "首选"},
				{"label": "B 方案"},
			},
		}},
	}}}
	if len(events) != 1 {
		t.Fatalf("事件数 = %d, want 1", len(events))
	}
	assertEventJSON(t, events[0], want)
	if ids := tr.CancelUIRequestIDs(); len(ids) != 0 {
		t.Fatalf("合法请求不应有取消信号: %v", ids)
	}
}

// 多题、无 description 的选项、toolCallId 缺失时取 title 后缀。
func TestQuestionRequestMultiAndFallback(t *testing.T) {
	tr := New()
	prefill := `{"questions":[` +
		`{"header":"语言","question":"用哪种语言?","multiSelect":true,` +
		`"options":[{"label":"中文"},{"label":"英文"}]},` +
		`{"header":"范围","question":"改哪些文件?","multiSelect":false,` +
		`"options":[{"label":"仅后端"},{"label":"前后端"}]}]}`
	events, err := tr.Feed([]byte(editorLine("q2", "xlyra:question:call_9", prefill)))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(events) != 1 || events[0].Type != "question_request" {
		t.Fatalf("事件 = %+v", events)
	}
	question := events[0].Data["question"].(map[string]any)
	if question["tool_call_id"] != "call_9" {
		t.Fatalf("tool_call_id 应取 title 后缀: %v", question["tool_call_id"])
	}
	if question["question_id"] != "q2" {
		t.Fatalf("question_id = %v", question["question_id"])
	}
	items := question["questions"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("题数 = %d, want 2", len(items))
	}
	if items[0]["multiSelect"] != true || items[1]["multiSelect"] != false {
		t.Fatalf("multiSelect: %+v", items)
	}
	// 无 description 的选项不带该字段
	if _, has := items[0]["options"].([]map[string]any)[0]["description"]; has {
		t.Fatalf("缺省 description 应省略: %+v", items)
	}
}

func TestQuestionRequestInvalid(t *testing.T) {
	cases := []struct {
		name    string
		prefill string
	}{
		{"prefill 非 JSON", "not json"},
		{"零题", `{"toolCallId":"c","questions":[]}`},
		{"超过 4 题", `{"questions":[` +
			`{"header":"h","question":"q","options":[{"label":"a"},{"label":"b"}]},` +
			`{"header":"h","question":"q","options":[{"label":"a"},{"label":"b"}]},` +
			`{"header":"h","question":"q","options":[{"label":"a"},{"label":"b"}]},` +
			`{"header":"h","question":"q","options":[{"label":"a"},{"label":"b"}]},` +
			`{"header":"h","question":"q","options":[{"label":"a"},{"label":"b"}]}]}`},
		{"header 为空", `{"questions":[{"header":"","question":"q","options":[{"label":"a"},{"label":"b"}]}]}`},
		{"question 为空", `{"questions":[{"header":"h","question":"","options":[{"label":"a"},{"label":"b"}]}]}`},
		{"选项少于 2", `{"questions":[{"header":"h","question":"q","options":[{"label":"a"}]}]}`},
		{"选项超过 6", `{"questions":[{"header":"h","question":"q","options":[` +
			`{"label":"1"},{"label":"2"},{"label":"3"},{"label":"4"},{"label":"5"},{"label":"6"},{"label":"7"}]}]}`},
		{"label 为空", `{"questions":[{"header":"h","question":"q","options":[{"label":"a"},{"label":""}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := New()
			events, err := tr.Feed([]byte(editorLine("qbad", "xlyra:question:call_1", tc.prefill)))
			if err != nil {
				t.Fatalf("Feed: %v", err)
			}
			if events != nil {
				t.Fatalf("非法 prefill 不应发事件: %+v", events)
			}
			ids := tr.CancelUIRequestIDs()
			if !reflect.DeepEqual(ids, []string{"qbad"}) {
				t.Fatalf("取消信号 = %v, want [qbad]", ids)
			}
			// 队列被消费后应为空
			if ids := tr.CancelUIRequestIDs(); len(ids) != 0 {
				t.Fatalf("消费后应清空: %v", ids)
			}
		})
	}
	t.Run("prefill 缺失", func(t *testing.T) {
		tr := New()
		events, err := tr.Feed([]byte(`{"type":"extension_ui_request","id":"qbad","method":"editor","title":"xlyra:question:call_1"}`))
		if err != nil {
			t.Fatalf("Feed: %v", err)
		}
		if events != nil {
			t.Fatalf("无 prefill 不应发事件: %+v", events)
		}
		if ids := tr.CancelUIRequestIDs(); !reflect.DeepEqual(ids, []string{"qbad"}) {
			t.Fatalf("取消信号 = %v", ids)
		}
	})
}

// 其他 editor 请求(title 不带 question 前缀)丢弃并给出取消信号,
// 避免 Pi 永久阻塞。
func TestOtherEditorRequestCancelled(t *testing.T) {
	tr := New()
	events, err := tr.Feed([]byte(editorLine("ed1", "Edit some text", "Line 1")))
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if events != nil {
		t.Fatalf("其他 editor 请求不应发事件: %+v", events)
	}
	if ids := tr.CancelUIRequestIDs(); !reflect.DeepEqual(ids, []string{"ed1"}) {
		t.Fatalf("取消信号 = %v, want [ed1]", ids)
	}
}

func TestQuestionAnswered(t *testing.T) {
	tr := New()
	answers := []session.QuestionAnswer{
		{Selected: []string{"A 方案"}},
		{Selected: []string{"中文", session.OtherOptionMarker}, Other: "其他补充"},
	}
	// 普通回答:布尔字段仅在真时输出,other 存在时携带
	assertEventJSON(t, tr.QuestionAnswered("q1", answers, false, false, false)[0],
		Event{Type: "question_answered", Data: map[string]any{
			"question_id": "q1",
			"answers": []map[string]any{
				{"selected": []string{"A 方案"}},
				{"selected": []string{"中文", session.OtherOptionMarker}, "other": "其他补充"},
			},
		}})
	// 跳过 + auto
	assertEventJSON(t, tr.QuestionAnswered("q1", answers, true, false, true)[0],
		Event{Type: "question_answered", Data: map[string]any{
			"question_id": "q1",
			"answers": []map[string]any{
				{"selected": []string{"A 方案"}},
				{"selected": []string{"中文", session.OtherOptionMarker}, "other": "其他补充"},
			},
			"skipped": true, "auto": true,
		}})
	// 取消:answers 为空时输出空数组
	assertEventJSON(t, tr.QuestionAnswered("q2", nil, false, true, false)[0],
		Event{Type: "question_answered", Data: map[string]any{
			"question_id": "q2", "answers": []map[string]any{}, "cancelled": true,
		}})
}
