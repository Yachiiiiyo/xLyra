import { describe, expect, it } from 'vitest'
import { QUESTION_OTHER, type AgentQuestionItem } from '@/features/agent/api/agent'
import { buildAnswers, emptyDraft, optionIndexForDigit, shouldSubmitOnEnter, validateAnswers, validateQuestionDraft, type QuestionDraftState } from '@/features/agent/lib/question-answers'

const items: AgentQuestionItem[] = [
  { header: '风格', question: '选哪种风格？', multiSelect: false, options: [{ label: '简约' }, { label: '复古', description: '怀旧感' }] },
  { header: '模块', question: '需要哪些模块？', multiSelect: true, options: [{ label: 'A' }, { label: 'B' }] },
]

function draft(state: Record<number, { selected: string[]; other?: string }>): QuestionDraftState {
  const base = emptyDraft(items)
  for (const [index, value] of Object.entries(state)) {
    base[Number(index)] = { selected: value.selected, other: value.other ?? '' }
  }
  return base
}

describe('buildAnswers', () => {
  it('状态转请求体：未选「其他」不带 other', () => {
    expect(buildAnswers(items, draft({ 0: { selected: ['简约'] }, 1: { selected: ['A', 'B'] } }))).toEqual([
      { selected: ['简约'] },
      { selected: ['A', 'B'] },
    ])
  })

  it('选「其他」时 selected 带 __other__ 标记且 other 为 trim 后文本', () => {
    const answers = buildAnswers(items, draft({ 0: { selected: [QUESTION_OTHER], other: ' 顺便加点动效 ' } }))
    expect(answers[0]).toEqual({ selected: [QUESTION_OTHER], other: '顺便加点动效' })
  })

  it('选了「其他」但文本为空时不带 other（由校验拦截）', () => {
    const answers = buildAnswers(items, draft({ 0: { selected: [QUESTION_OTHER], other: '   ' } }))
    expect(answers[0]).toEqual({ selected: [QUESTION_OTHER] })
  })
})

describe('validateQuestionDraft', () => {
  it('未选择返回 required，普通选择通过', () => {
    expect(validateQuestionDraft(items[0]!, { selected: [], other: '' })).toBe('question.required')
    expect(validateQuestionDraft(items[0]!, { selected: ['简约'], other: '' })).toBeNull()
  })

  it('选择「其他」时校验非空文本与 2000 字上限', () => {
    expect(validateQuestionDraft(items[0]!, { selected: [QUESTION_OTHER], other: '   ' })).toBe('question.otherRequired')
    expect(validateQuestionDraft(items[0]!, { selected: [QUESTION_OTHER], other: 'x'.repeat(2001) })).toBe('question.otherTooLong')
    expect(validateQuestionDraft(items[0]!, { selected: [QUESTION_OTHER], other: 'x'.repeat(2000) })).toBeNull()
  })
})

describe('validateAnswers', () => {
  it('每题至少选 1 项', () => {
    expect(validateAnswers(items, draft({ 1: { selected: ['A'] } }))).toBe('question.required')
  })

  it('选「其他」必须有非空文本', () => {
    expect(validateAnswers(items, draft({ 0: { selected: [QUESTION_OTHER], other: '' }, 1: { selected: ['A'] } }))).toBe('question.otherRequired')
  })

  it('「其他」文本不能超过 2000 字', () => {
    expect(validateAnswers(items, draft({ 0: { selected: [QUESTION_OTHER], other: 'x'.repeat(2001) }, 1: { selected: ['A'] } }))).toBe('question.otherTooLong')
    expect(validateAnswers(items, draft({ 0: { selected: [QUESTION_OTHER], other: 'x'.repeat(2000) }, 1: { selected: ['A'] } }))).toBeNull()
  })

  it('全部通过返回 null', () => {
    expect(validateAnswers(items, draft({ 0: { selected: ['简约'] }, 1: { selected: ['A'] } }))).toBeNull()
  })
})

describe('optionIndexForDigit', () => {
  it('数字键 1-9 映射第 n 个选项，超界返回 null', () => {
    expect(optionIndexForDigit('1', items[0]!)).toBe(0)
    expect(optionIndexForDigit('2', items[0]!)).toBe(1)
    expect(optionIndexForDigit('3', items[0]!)).toBeNull()
    expect(optionIndexForDigit('0', items[0]!)).toBeNull()
    expect(optionIndexForDigit('a', items[0]!)).toBeNull()
  })
})

describe('shouldSubmitOnEnter', () => {
  it('文本框内 Enter/Shift+Enter 不提交,Cmd/Ctrl+Enter 提交,其余位置 Enter 提交,非 Enter 不提交', () => {
    const base = { key: 'Enter', inTextarea: true, metaKey: false, ctrlKey: false }
    expect(shouldSubmitOnEnter(base)).toBe(false)
    expect(shouldSubmitOnEnter({ ...base, metaKey: true })).toBe(true)
    expect(shouldSubmitOnEnter({ ...base, ctrlKey: true })).toBe(true)
    expect(shouldSubmitOnEnter({ ...base, inTextarea: false })).toBe(true)
    expect(shouldSubmitOnEnter({ ...base, key: 'a', inTextarea: false })).toBe(false)
  })
})
