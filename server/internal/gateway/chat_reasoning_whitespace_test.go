package gateway

import (
	"encoding/json"
	"testing"
)

// 回归:上游把词和空格拆成独立增量发来(或词带前导空格)时,推理文本必须原样拼回,
// 不能被 TrimSpace 或丢掉纯空白段,否则会得到「AppStoreConnect」。
func TestOpenAIChatDecoderKeepsReasoningWhitespace(t *testing.T) {
	for name, field := range map[string]string{"reasoning_content": "reasoning_content", "thinking": "thinking"} {
		t.Run(name, func(t *testing.T) {
			d := &openAIChatStreamDecoder{}
			var got string
			for _, token := range []string{"I", " ", "should", " respond", " ", "in", "\n\n", "Chinese"} {
				quoted, _ := json.Marshal(token)
				events, err := d.DecodeLine([]byte(`data: {"id":"x","choices":[{"index":0,"delta":{"` + field + `":` + string(quoted) + `}}]}`))
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					if event.Type == canonicalStreamEventReasoningDelta {
						got += event.Delta
					}
				}
			}
			if want := "I should respond in\n\nChinese"; got != want {
				t.Fatalf("reasoning = %q, want %q", got, want)
			}
		})
	}
}
