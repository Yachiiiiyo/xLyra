package notify

import "xlyra/server/internal/agent/session"

// SessionStore 是通知需要的会话读取能力。*session.Store 满足该接口。
type SessionStore interface {
	Get(id string) (session.Meta, error)
	List() ([]session.Meta, error)
}

// StoreSessions 用会话元数据实现 Sessions。
// 待处理数按会话列表同样的口径:未决审批(Granted == nil)加未回答提问。
type StoreSessions struct {
	Store SessionStore
}

func (s StoreSessions) Get(id string) (SessionMeta, error) {
	meta, err := s.Store.Get(id)
	if err != nil {
		return SessionMeta{}, err
	}
	return SessionMeta{
		Title:          meta.Title,
		PermissionMode: meta.PermissionMode,
		Escalations:    meta.Escalations,
		Questions:      meta.Questions,
	}, nil
}

func (s StoreSessions) PendingTotal() int {
	metas, err := s.Store.List()
	if err != nil {
		return 0
	}
	total := 0
	for _, meta := range metas {
		for _, escalation := range meta.Escalations {
			if escalation.Granted == nil && !escalation.Reviewing {
				total++
			}
		}
		for _, question := range meta.Questions {
			if question.AnsweredAt == nil {
				total++
			}
		}
	}
	return total
}
