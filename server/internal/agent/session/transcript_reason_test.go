package session

import "testing"

func TestEscalationEntryClassifierReason(t *testing.T) {
	entry := escalationEntry(Escalation{EscalationID: "esc-1", Auto: true, ClassifierReason: "仅修改工作区文件"})
	if got := entry["reason"]; got != "仅修改工作区文件" {
		t.Fatalf("reason = %v", got)
	}
	blocked := escalationEntry(Escalation{EscalationID: "esc-3", ClassifierReason: "需要人工确认"})
	if _, ok := blocked["reason"]; ok {
		t.Fatalf("拦截理由不应输出 reason: %v", blocked)
	}
	without := escalationEntry(Escalation{EscalationID: "esc-2", Auto: true})
	if _, ok := without["reason"]; ok {
		t.Fatalf("空分类器理由不应输出 reason: %v", without)
	}
}
