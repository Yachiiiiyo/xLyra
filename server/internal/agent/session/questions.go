// questions.go:question 工具的会话数据层——meta.Questions 的类型与增改方法。
// 转录/引用渲染/fork 的消费逻辑分别在 transcript.go、reference.go、fork.go。
package session

import (
	"fmt"
	"time"
)

// MaxQuestions 是 meta 里保留的提问记录条数上限,超出丢弃最旧的(与 MaxRunErrors 同风格)。
const MaxQuestions = 50

// OtherOptionMarker 是回答里表示选中「其他」的约定标记:前端固定追加
// 「其他」选项,不占模型的标签空间,用哨兵值避免与真实标签冲突。
// translate 发事件、session 存 meta 都用它辨认「其他」回答。
const OtherOptionMarker = "__other__"

// Question 是一次 question 工具调用的完整记录:发起时写入 Questions/AfterEntryID,
// 用户回答(或跳过/取消)后由 AnswerQuestion 补上答案与时间。
// QuestionItem/QuestionAnswer 的字段名与扩展 prefill、translate 事件保持一致,
// 供 translate 包直接复用(它解析 prefill 时用的就是同一结构)。
type Question struct {
	QuestionID   string           `json:"question_id"`    // extension_ui_request 的 id
	ToolCallID   string           `json:"tool_call_id"`   // question 工具调用 id
	AfterEntryID string           `json:"after_entry_id"` // 锚点,语义与审批相同
	Questions    []QuestionItem   `json:"questions"`
	Answers      []QuestionAnswer `json:"answers,omitempty"` // 未回答时省略
	Skipped      bool             `json:"skipped,omitempty"`
	Cancelled    bool             `json:"cancelled,omitempty"`
	Auto         bool             `json:"auto,omitempty"`
	AskedAt      time.Time        `json:"asked_at"`
	AnsweredAt   *time.Time       `json:"answered_at,omitempty"` // nil 表示未回答
}

// QuestionItem 是单个问题;multiSelect/options 的字段名与扩展 prefill 及
// question_request 事件一致(前端两处读同一形态)。
type QuestionItem struct {
	Header      string           `json:"header"`
	Question    string           `json:"question"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []QuestionOption `json:"options"`
}

// QuestionOption 是单个选项;description 缺省省略。
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionAnswer 是单个问题的回答:选中标签列表(选「其他」时含约定标记
// "__other__")与其他文本。translate 的事件与 meta 共用本结构。
type QuestionAnswer struct {
	Selected []string `json:"selected"`
	Other    string   `json:"other,omitempty"`
}

// AddQuestion 记录一次提问;同一 QuestionID 已存在时原位替换(重放去重),
// 超出 MaxQuestions 丢弃最旧。
func (s *Store) AddQuestion(id string, q Question) error {
	_, err := s.Update(id, func(m *Meta) error {
		for i := range m.Questions {
			if m.Questions[i].QuestionID == q.QuestionID {
				m.Questions[i] = q
				return nil
			}
		}
		m.Questions = append(m.Questions, q)
		if len(m.Questions) > MaxQuestions {
			m.Questions = m.Questions[len(m.Questions)-MaxQuestions:]
		}
		return nil
	})
	return err
}

// AnswerQuestion 补记一次提问的回答(用户提交/跳过、停止取消或超时兜底)。
// 问题不存在时返回 ErrNotFound;at 为回答时间。
func (s *Store) AnswerQuestion(id, questionID string, answers []QuestionAnswer, skipped, cancelled, auto bool, at time.Time) error {
	_, err := s.Update(id, func(m *Meta) error {
		for i := range m.Questions {
			if m.Questions[i].QuestionID != questionID {
				continue
			}
			answered := at.UTC()
			m.Questions[i].Answers = answers
			m.Questions[i].Skipped = skipped
			m.Questions[i].Cancelled = cancelled
			m.Questions[i].Auto = auto
			m.Questions[i].AnsweredAt = &answered
			return nil
		}
		return fmt.Errorf("%w: 问题 %s 不存在", ErrNotFound, questionID)
	})
	return err
}
