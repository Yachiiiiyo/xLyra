import { describe, expect, it } from 'vitest'

import { reduceAgentEvent, timelineFromTranscript, type AgentTimeline } from '@/features/agent/lib/agent-events'
import type { AgentTranscriptEntry } from '@/features/agent/api/agent'

// Vite 的 ?raw glob 在类型上返回 unknown,运行时是逐文件的字符串内容
const rawFixtures = import.meta.glob('./translated-*.jsonl', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const rawTranscripts = import.meta.glob('./transcript-*.json', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('translated-* 事件夹具', () => {
  it('样本数量只增不减', () => {
    expect(Object.keys(rawFixtures).length).toBeGreaterThanOrEqual(13)
  })

  it('每个非空行都是带字符串 type 字段的合法 JSON', () => {
    for (const [path, content] of Object.entries(rawFixtures)) {
      const lines = content.split('\n')
      lines.forEach((line, index) => {
        if (!line.trim()) return
        let parsed: unknown
        try {
          parsed = JSON.parse(line)
        } catch (error) {
          throw new Error(`${path}:${index + 1} 不是合法 JSON: ${error}`)
        }
        const type = (parsed as Record<string, unknown>).type
        if (typeof type !== 'string' || type === '') {
          throw new Error(`${path}:${index + 1} 缺少非空字符串字段 type`)
        }
      })
    }
  })

  it('会话内允许 / 自动放行夹具回放出对应的权限展示', () => {
    const content = rawFixtures['./translated-session-auto.jsonl']
    if (typeof content !== 'string') throw new Error('缺少 translated-session-auto.jsonl 夹具')
    const timeline = content.split('\n').filter((line) => line.trim()).reduce<AgentTimeline>((current, line) => {
      const event = JSON.parse(line) as { type: string; data: unknown }
      return reduceAgentEvent(current, event)
    }, [])
    const run = timeline[0]
    if (run?.kind !== 'run') throw new Error('expected run')
    expect(run.run.permissions[0]).toMatchObject({ id: 'esc-s1', tool: 'write', decision: 'allowed' })
    expect(run.run.permissions[0]?.scope).toBeUndefined()
    expect(run.run.permissions[1]).toMatchObject({ id: 'esc-s2', tool: 'write', decision: 'allowed', auto: true })
    expect(run.run.status).toBe('done')
  })

  it('run_error 转录夹具回放出失败的 run', () => {
    const content = rawTranscripts['./transcript-session-auto.json']
    if (typeof content !== 'string') throw new Error('缺少 transcript-session-auto.json 夹具')
    const timeline = timelineFromTranscript(JSON.parse(content) as AgentTranscriptEntry[])
    const run = timeline[1]
    if (run?.kind !== 'run') throw new Error('expected run')
    expect(run.run.status).toBe('error')
    expect(run.run.errorMessage).toBe('上游连接中断')
    expect(run.run.permissions.map((request) => [request.decision, request.auto ?? false])).toEqual([
      ['allowed', false],
      ['allowed', true],
    ])
  })
})
