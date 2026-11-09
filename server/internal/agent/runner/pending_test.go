package runner

import (
	"testing"

	"xlyra/server/internal/agent/session"
	"xlyra/server/internal/agent/translate"
)

// TestPendingCounts:未决审批与提问按 session_id 计数,auto 分类中的审批
// 不等用户不计入,无活跃 run 的会话为 0。
func TestPendingCounts(t *testing.T) {
	ar := &activeRun{
		sessionID:        "s-1",
		pending:          map[string]session.Escalation{"e-1": {}, "e-2": {}, "e-auto": {}},
		autoReviewing:    map[string]translate.Event{"e-auto": {}},
		pendingQuestions: map[string]session.Question{"q-1": {}},
	}
	r := &Runner{active: map[string]*activeRun{"s-1": ar}}

	approvals, questions := r.PendingCounts("s-1")
	if approvals != 2 || questions != 1 {
		t.Fatalf("PendingCounts = (%d, %d), want (2, 1)", approvals, questions)
	}

	approvals, questions = r.PendingCounts("missing")
	if approvals != 0 || questions != 0 {
		t.Fatalf("无活跃 run 时应为 (0, 0), got (%d, %d)", approvals, questions)
	}
}
