import { describe, expect, it } from 'vitest'
import type { AgentArtifactFile } from '@/features/agent/api/agent'
import { agentGeneratedFileCount, sortArtifactFiles } from '@/features/agent/lib/artifacts'

function file(overrides: Partial<AgentArtifactFile>): AgentArtifactFile {
  return { path: 'a.txt', size: 10, mtime: '2026-09-03T00:00:00.000Z', kind: 'text', agent_generated: false, ...overrides }
}

describe('agentGeneratedFileCount', () => {
  it('「文件 · N」入口只统计 Agent 产生的文件', () => {
    expect(agentGeneratedFileCount(null)).toBe(0)
    expect(agentGeneratedFileCount(undefined)).toBe(0)
    expect(agentGeneratedFileCount({ files: [file({ agent_generated: true }), file({}), file({ agent_generated: true })], truncated: false })).toBe(2)
    expect(agentGeneratedFileCount({ files: [file({})], truncated: false })).toBe(0)
  })
})

describe('sortArtifactFiles', () => {
  it('Agent 产生的在前,同组内按修改时间倒序', () => {
    const sorted = sortArtifactFiles([
      file({ path: 'user-old.txt', mtime: '2026-09-01T00:00:00.000Z' }),
      file({ path: 'agent-new.txt', mtime: '2026-09-03T00:00:00.000Z', agent_generated: true }),
      file({ path: 'user-new.txt', mtime: '2026-09-02T00:00:00.000Z' }),
      file({ path: 'agent-old.txt', mtime: '2026-09-02T00:00:00.000Z', agent_generated: true }),
    ])
    expect(sorted.map((item) => item.path)).toEqual(['agent-new.txt', 'agent-old.txt', 'user-new.txt', 'user-old.txt'])
  })

  it('不修改原数组,非法时间排在最后', () => {
    const files = [file({ path: 'ok.txt', mtime: '2026-09-02T00:00:00.000Z' }), file({ path: 'bad.txt', mtime: 'not-a-date' })]
    const sorted = sortArtifactFiles(files)
    expect(files.map((item) => item.path)).toEqual(['ok.txt', 'bad.txt'])
    expect(sorted.map((item) => item.path)).toEqual(['ok.txt', 'bad.txt'])
  })
})
