package runner

import (
	"encoding/json"
	"strings"
	"time"

	"xlyra/server/internal/agent/session"
)

// genSampleTracker 按 assistant 消息实测生成耗时:message_start(assistant)
// 重置,该消息第一个 *_delta 增量(含 thinking/toolcall)到达时记首字时刻,
// message_end 时算出实测生成耗时。text_start 等块开始事件可能先于 token
// 到达,不算首字;网关排队/重试与首字等待都发生在第一个增量之前,自然被
// 排除,不用 Pi 的 durationMs。
type genSampleTracker struct {
	now        func() time.Time
	active     bool      // 最近一条 message_start 是 assistant
	firstDelta time.Time // 本条 assistant 消息第一个增量的到达时刻
}

// observe 观察一条 Pi 原始事件;assistant 消息正常结束时返回可写回的样本。
// 事件里 message.timestamp 与会话文件里同一条消息的 message.timestamp 同源
// (毫秒),统计按它对上会话文件里的消息。
func (t *genSampleTracker) observe(raw []byte) (session.GenSample, bool) {
	kind := eventType(raw)
	if kind != "message_start" && kind != "message_update" && kind != "message_end" {
		return session.GenSample{}, false
	}
	var event struct {
		AssistantMessageEvent struct {
			Type string `json:"type"`
		} `json:"assistantMessageEvent"`
		Message struct {
			Role       string `json:"role"`
			Timestamp  int64  `json:"timestamp"`
			StopReason string `json:"stopReason"`
			Usage      struct {
				Output int64 `json:"output"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return session.GenSample{}, false
	}
	msg := event.Message
	switch kind {
	case "message_start":
		t.active = msg.Role == "assistant"
		t.firstDelta = time.Time{}
		return session.GenSample{}, false
	case "message_update":
		if t.active && t.firstDelta.IsZero() && strings.HasSuffix(event.AssistantMessageEvent.Type, "_delta") {
			t.firstDelta = t.now()
		}
		return session.GenSample{}, false
	default: // message_end
		first := t.firstDelta
		t.active = false
		t.firstDelta = time.Time{}
		if msg.Role != "assistant" || msg.StopReason == "error" || msg.StopReason == "aborted" ||
			msg.Usage.Output <= 0 || msg.Timestamp == 0 || first.IsZero() {
			return session.GenSample{}, false
		}
		return session.GenSample{
			Timestamp:    msg.Timestamp,
			OutputTokens: msg.Usage.Output,
			GenMs:        t.now().Sub(first).Milliseconds(),
		}, true
	}
}

// recordGenSamples 把实测的生成速度样本写回 meta(超出上限丢最旧)。
// drive 在每条 assistant 消息结束时调用一次,统计才能在一轮对话进行中逐步更新;
// 只是一次很小的 meta 更新,与审批记录的写法一致。
func (r *Runner) recordGenSamples(ar *activeRun, samples []session.GenSample) {
	if len(samples) == 0 {
		return
	}
	if _, err := r.cfg.Store.Update(ar.sessionID, func(meta *session.Meta) error {
		for _, sample := range samples {
			meta.AppendGenSample(sample)
		}
		return nil
	}); err != nil {
		r.logger.Warn("记录生成速度样本失败", "session_id", ar.sessionID, "run_id", ar.runID, "error", err)
	}
}
