import { QUESTION_OTHER, type AgentQuestionAnswer, type AgentQuestionItem } from '@/features/agent/api/agent'

export const QUESTION_OTHER_MAX = 2000

/** 卡片的选项选择状态：每题一个，selected 存选项 label（含 QUESTION_OTHER 标记），other 存「其他」文本 */
export type QuestionDraft = { selected: string[]; other: string }

export type QuestionDraftState = Record<number, QuestionDraft>

export function emptyDraft(items: AgentQuestionItem[]): QuestionDraftState {
  const state: QuestionDraftState = {}
  items.forEach((_, index) => { state[index] = { selected: [], other: '' } })
  return state
}

/** 状态 → 请求体 answers：未选「其他」不带 other；selected 去掉空值保序 */
export function buildAnswers(items: AgentQuestionItem[], state: QuestionDraftState): AgentQuestionAnswer[] {
  return items.map((_item, index) => {
    const draft = state[index] ?? { selected: [], other: '' }
    const selected = [...draft.selected]
    const answer: AgentQuestionAnswer = { selected }
    if (selected.includes(QUESTION_OTHER) && draft.other.trim()) answer.other = draft.other.trim()
    return answer
  })
}

/** 校验单题：至少选 1 项；选「其他」必须有非空文本且 ≤ 2000 字 */
export function validateQuestionDraft(_item: AgentQuestionItem, draft: QuestionDraft): string | null {
  if (draft.selected.length === 0) return 'question.required'
  if (draft.selected.includes(QUESTION_OTHER)) {
    const text = draft.other.trim()
    if (!text) return 'question.otherRequired'
    if (text.length > QUESTION_OTHER_MAX) return 'question.otherTooLong'
  }
  return null
}

/** 校验：每题至少选 1 项；选「其他」必须有非空文本且 ≤ 2000 字。返回第一个错误的 i18n 键，通过时为 null */
export function validateAnswers(items: AgentQuestionItem[], state: QuestionDraftState): string | null {
  for (let index = 0; index < items.length; index += 1) {
    const error = validateQuestionDraft(items[index]!, state[index] ?? { selected: [], other: '' })
    if (error) return error
  }
  return null
}

/** 数字键映射：按键 1-9 对应第 n 个选项（不含「其他」），超界返回 null */
export function optionIndexForDigit(digit: string, item: AgentQuestionItem): number | null {
  if (!/^[1-9]$/.test(digit)) return null
  const index = Number(digit) - 1
  return index < item.options.length ? index : null
}

/** 回车是否触发提交:文本框内 Enter/Shift+Enter 换行,只有 Cmd/Ctrl+Enter 提交;其余位置 Enter 提交。 */
export function shouldSubmitOnEnter(input: { key: string; inTextarea: boolean; metaKey: boolean; ctrlKey: boolean }): boolean {
  if (input.key !== 'Enter') return false
  return !input.inTextarea || input.metaKey || input.ctrlKey
}
