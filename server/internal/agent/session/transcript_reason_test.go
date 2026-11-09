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

func TestEscalationEntryNoteAndRisk(t *testing.T) {
	entry := escalationEntry(Escalation{
		EscalationID: "esc-1",
		Note:         "工作目录: .",
		Risk:         "high",
		RiskRules:    []string{"sudo 提权", "rm 递归删除高危目标"},
	})
	if entry["note"] != "工作目录: ." || entry["risk"] != "high" {
		t.Fatalf("note/risk = %v / %v", entry["note"], entry["risk"])
	}
	rules, _ := entry["risk_rules"].([]string)
	if len(rules) != 2 || rules[0] != "sudo 提权" {
		t.Fatalf("risk_rules = %v", entry["risk_rules"])
	}

	// 旧数据没有这些字段:输出里不能出现空串或空数组。
	old := escalationEntry(Escalation{EscalationID: "esc-old"})
	for _, key := range []string{"note", "risk", "risk_rules"} {
		if _, ok := old[key]; ok {
			t.Fatalf("旧记录不应输出 %s: %v", key, old)
		}
	}
}
