import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

import {
  reduceAgentEvent,
  runElapsedMs,
  timelineFromTranscript,
  type AgentTimeline,
} from '@/features/agent/lib/agent-events'
import type { AgentTranscriptEntry } from '@/features/agent/api/agent'

const questionPayload = {
  question: {
    question_id: 'q1',
    tool_call_id: 'call-1',
    questions: [
      { header: '风格', question: '选哪种风格？', multiSelect: false, options: [{ label: '简约' }, { label: '复古' }] },
    ],
  },
}

function reduceAll(events: Array<{ type: string; data: unknown }>, initial: AgentTimeline = []) {
  return events.reduce((timeline, event) => reduceAgentEvent(timeline, event), initial)
}

function lastRunOf(timeline: AgentTimeline) {
  const item = timeline[timeline.length - 1]
  if (item?.kind !== 'run') throw new Error('no run')
  return item.run
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

describe('question_request / question_answered 事件映射', () => {
  it('映射为 pending 问题并暂停计时，回答后恢复', () => {
    vi.setSystemTime(0)
    let timeline = reduceAll([{ type: 'agent_start', data: {} }])
    vi.setSystemTime(10_000)
    timeline = reduceAgentEvent(timeline, { type: 'question_request', data: questionPayload })
    const run = lastRunOf(timeline)
    expect(run.questions).toHaveLength(1)
    expect(run.questions![0]).toMatchObject({ id: 'q1', toolCallId: 'call-1', status: 'pending', position: 0 })
    // 等待用户期间用时冻结
    expect(runElapsedMs(run, 40_000)).toBe(10_000)
    vi.setSystemTime(40_000)
    timeline = reduceAgentEvent(timeline, {
      type: 'question_answered',
      data: { question_id: 'q1', answers: [{ selected: ['简约'] }] },
    })
    const answered = lastRunOf(timeline)
    expect(answered.questions![0]).toMatchObject({ status: 'answered', answers: [{ selected: ['简约'] }] })
    expect(answered.pendingSince).toBeUndefined()
    expect(runElapsedMs(answered, 45_000)).toBe(15_000)
  })

  it('重复的 question_request 按 question_id 去重', () => {
    const timeline = reduceAll([
      { type: 'agent_start', data: {} },
      { type: 'question_request', data: questionPayload },
      { type: 'question_request', data: questionPayload },
    ])
    expect(lastRunOf(timeline).questions).toHaveLength(1)
  })

  it('skipped / cancelled / auto 映射到对应状态', () => {
    let timeline = reduceAll([
      { type: 'agent_start', data: {} },
      { type: 'question_request', data: questionPayload },
    ])
    timeline = reduceAgentEvent(timeline, { type: 'question_answered', data: { question_id: 'q1', skipped: true } })
    expect(lastRunOf(timeline).questions![0]?.status).toBe('skipped')

    timeline = reduceAll([
      { type: 'agent_start', data: {} },
      { type: 'question_request', data: { ...questionPayload, question: { ...questionPayload.question, question_id: 'q2' } } },
    ])
    timeline = reduceAgentEvent(timeline, { type: 'question_answered', data: { question_id: 'q2', cancelled: true } })
    expect(lastRunOf(timeline).questions![0]?.status).toBe('cancelled')

    timeline = reduceAll([
      { type: 'agent_start', data: {} },
      { type: 'question_request', data: { ...questionPayload, question: { ...questionPayload.question, question_id: 'q3' } } },
    ])
    timeline = reduceAgentEvent(timeline, { type: 'question_answered', data: { question_id: 'q3', skipped: true, auto: true } })
    expect(lastRunOf(timeline).questions![0]).toMatchObject({ status: 'skipped', auto: true })
  })

  it('未决提问的 run 被收尾时标记 cancelled 且等待时长不计入', () => {
    vi.setSystemTime(0)
    let timeline = reduceAll([{ type: 'agent_start', data: {} }])
    vi.setSystemTime(5_000)
    timeline = reduceAgentEvent(timeline, { type: 'question_request', data: questionPayload })
    vi.setSystemTime(12_000)
    timeline = reduceAgentEvent(timeline, { type: 'agent_cancelled', data: {} })
    const run = lastRunOf(timeline)
    expect(run.questions![0]?.status).toBe('cancelled')
    expect(run.elapsedMs).toBe(5_000)
  })
})

describe('转录 question 条目', () => {
  const base: AgentTranscriptEntry[] = [
    { type: 'message', message_id: 'm1', timestamp: '1970-01-01T00:00:00.000Z', message: { role: 'user', content: '问一下' } },
  ]

  it('未回答 → pending（刷新后仍可交互）', () => {
    const timeline = timelineFromTranscript([
      ...base,
      { type: 'question', question_id: 'q1', tool_call_id: 'c1', timestamp: '1970-01-01T00:00:01.000Z', questions: questionPayload.question.questions },
    ])
    const run = timeline[1]
    if (run?.kind !== 'run') throw new Error('expected run')
    expect(run.run.questions![0]).toMatchObject({ id: 'q1', status: 'pending' })
  })

  it('已回答 / 跳过 / 取消 → 对应状态与答案', () => {
    const timeline = timelineFromTranscript([
      ...base,
      { type: 'question', question_id: 'q1', timestamp: '1970-01-01T00:00:01.000Z', questions: questionPayload.question.questions, answers: [{ selected: ['简约'] }] },
    ])
    const timeline2 = timelineFromTranscript([
      ...base,
      { type: 'question', question_id: 'q2', timestamp: '1970-01-01T00:00:01.000Z', questions: questionPayload.question.questions, skipped: true },
    ])
    const timeline3 = timelineFromTranscript([
      ...base,
      { type: 'question', question_id: 'q3', timestamp: '1970-01-01T00:00:01.000Z', questions: questionPayload.question.questions, cancelled: true },
    ])
    expect((timeline[1] as { run: { questions: unknown[] } }).run.questions[0]).toMatchObject({ status: 'answered', answers: [{ selected: ['简约'] }] })
    expect((timeline2[1] as { run: { questions: unknown[] } }).run.questions[0]).toMatchObject({ status: 'skipped' })
    expect((timeline3[1] as { run: { questions: unknown[] } }).run.questions[0]).toMatchObject({ status: 'cancelled' })
  })
})
