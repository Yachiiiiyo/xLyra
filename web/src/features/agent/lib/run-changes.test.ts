import { describe, expect, it } from 'vitest'

import type { AgentRun, AgentRunChanges, AgentWorkStep } from '@/features/agent/lib/agent-events'
import { changePatches, parseUnifiedPatch, patchStats, runDisplayChanges, runFileChanges, serverFileChanges } from '@/features/agent/lib/run-changes'

const PATCH = '--- a.txt\n+++ a.txt\n@@ -1,2 +1,2 @@\n alpha\n-beta\n+gamma\n'
const SECOND_PATCH = '--- a.txt\n+++ a.txt\n@@ -3 +3,2 @@\n delta\n+epsilon\n'

function runWith(steps: AgentWorkStep[]): AgentRun {
  return { id: 'run-1', status: 'done', steps, finalText: '', permissions: [], startedAt: 0 }
}

function toolStep(title: string, input: string, overrides: Partial<AgentWorkStep> = {}): AgentWorkStep {
  return { id: `${title}-step`, kind: 'tool', title, input, status: 'done', ...overrides }
}

describe('runFileChanges', () => {
  it('汇总单次 edit', () => {
    expect(runFileChanges(runWith([toolStep('edit', '{"path":"a.txt"}', { patch: PATCH })]))).toEqual([
      { path: 'a.txt', kind: 'edit', additions: 1, deletions: 1, patches: [PATCH] },
    ])
  })

  it('按顺序汇总同一文件的多次 edit', () => {
    const changes = runFileChanges(runWith([
      toolStep('EDIT', '{"path":"a.txt"}', { patch: PATCH }),
      toolStep('edit', '{"path":"a.txt"}', { patch: SECOND_PATCH }),
    ]))
    expect(changes).toEqual([
      { path: 'a.txt', kind: 'edit', additions: 2, deletions: 1, patches: [PATCH, SECOND_PATCH] },
    ])
  })

  it('write 使用最后一次内容并按行计数', () => {
    const changes = runFileChanges(runWith([
      toolStep('write', '{"path":"new.txt","content":"old"}'),
      toolStep('write', '{"path":"new.txt","content":"one\\ntwo\\n"}'),
      toolStep('write', '{"path":"empty.txt","content":""}'),
    ]))
    expect(changes).toEqual([
      { path: 'new.txt', kind: 'write', additions: 2, deletions: null, patches: [], content: 'one\ntwo\n' },
      { path: 'empty.txt', kind: 'write', additions: 0, deletions: null, patches: [], content: '' },
    ])
  })

  it('同一文件 write 后 edit 时源码为最终内容,保留 diff', () => {
    const changes = runFileChanges(runWith([
      toolStep('write', '{"path":"a.txt","content":"one\\ntwo\\n"}'),
      toolStep('edit', '{"path":"a.txt","edits":[{"oldText":"two","newText":"2"}]}', { patch: PATCH }),
    ]))
    expect(changes).toEqual([
      { path: 'a.txt', kind: 'write', additions: 2, deletions: null, patches: [PATCH], content: 'one\n2\n' },
    ])
  })

  it('旧会话中无 patch 的 edit 仍列出', () => {
    expect(runFileChanges(runWith([toolStep('edit', '{"path":"a.txt"}')]))).toEqual([
      { path: 'a.txt', kind: 'edit', additions: 0, deletions: 0, patches: [] },
    ])
  })

  it('跳过 error 步骤与非 JSON input', () => {
    const changes = runFileChanges(runWith([
      toolStep('edit', '{"path":"error.txt"}', { status: 'error', patch: PATCH }),
      toolStep('write', 'not json'),
      toolStep('read', '{"path":"read.txt"}'),
    ]))
    expect(changes).toEqual([])
  })

  it('超大 write 不保留 content 但仍计算行数', () => {
    const content = 'x'.repeat(256 * 1024 + 1)
    const input = JSON.stringify({ path: 'large.txt', content })
    expect(runFileChanges(runWith([toolStep('write', input)]))).toEqual([
      { path: 'large.txt', kind: 'write', additions: 1, deletions: null, patches: [], contentTruncated: true },
    ])
  })
})

describe('runFileChanges write 与 edit 混合', () => {
  it('write 后 edit:源码是套用 edit 之后的最终内容', () => {
    const [change] = runFileChanges(runWith([
      toolStep('write', JSON.stringify({ path: 'a.html', content: 'one\ntwo\nthree\n' }), { id: 'w' }),
      toolStep('edit', JSON.stringify({ path: 'a.html', edits: [{ oldText: 'two', newText: 'TWO\n2b' }] }), { id: 'e', patch: PATCH }),
    ]))
    expect(change?.content).toBe('one\nTWO\n2b\nthree\n')
    expect(change?.additions).toBe(4)
    expect(change?.deletions).toBeNull()
    expect(change?.patches).toEqual([PATCH])
  })

  it('edit 套不上(匹配不唯一)时不再显示过期的源码', () => {
    const [change] = runFileChanges(runWith([
      toolStep('write', JSON.stringify({ path: 'a.txt', content: 'x\nx\n' }), { id: 'w' }),
      toolStep('edit', JSON.stringify({ path: 'a.txt', edits: [{ oldText: 'x', newText: 'y' }] }), { id: 'e', patch: PATCH }),
    ]))
    expect(change?.content).toBeUndefined()
  })

  it('edit 后又 write:以最后一次 write 为准,清掉此前的 diff', () => {
    const [change] = runFileChanges(runWith([
      toolStep('edit', '{"path":"a.txt"}', { id: 'e', patch: PATCH }),
      toolStep('write', JSON.stringify({ path: 'a.txt', content: 'fresh\n' }), { id: 'w' }),
    ]))
    expect(change?.content).toBe('fresh\n')
    expect(change?.patches).toEqual([])
    expect(change?.additions).toBe(1)
  })
})

describe('serverFileChanges / runDisplayChanges', () => {
  const changes: AgentRunChanges = {
    turnId: 'turn-1',
    total: 3,
    incomplete: false,
    files: [
      { path: 'a.txt', kind: 'added', size: 12, additions: 3, deletions: 0, has_diff: true },
      { path: 'b.bin', kind: 'modified', size: 4096, additions: 0, deletions: 0, binary: true, has_diff: false },
      { path: 'c.txt', kind: 'deleted', size: 0, additions: 5, deletions: 5, truncated: true, has_diff: true },
    ],
  }

  it('serverFileChanges 把后端清单映射为变更区模型', () => {
    expect(serverFileChanges(changes)).toEqual([
      { path: 'a.txt', kind: 'write', additions: 3, deletions: 0, patches: [], status: 'added', turnId: 'turn-1', size: 12, binary: false, hasDiff: true, truncated: false, source: 'server' },
      { path: 'b.bin', kind: 'edit', additions: 0, deletions: 0, patches: [], status: 'modified', turnId: 'turn-1', size: 4096, binary: true, hasDiff: false, truncated: false, source: 'server' },
      { path: 'c.txt', kind: 'edit', additions: 0, deletions: 5, patches: [], status: 'deleted', turnId: 'turn-1', size: 0, binary: false, hasDiff: true, truncated: true, source: 'server' },
    ])
  })

  it('runDisplayChanges 以后端 run_changes 为权威，无则回退工具步骤推导', () => {
    const local = runWith([toolStep('write', '{"path":"a.txt","content":"one"}')])
    expect(runDisplayChanges({ ...local, changes })).toEqual(serverFileChanges(changes))
    expect(runDisplayChanges(local)).toEqual(runFileChanges(local))
  })
})

describe('changePatches', () => {
  it('write 起头的文件合成整份新增 diff,仅 edit 的文件用原 patch', () => {
    const [written] = runFileChanges(runWith([
      toolStep('write', JSON.stringify({ path: 'a.txt', content: 'one\ntwo\n' }), { id: 'w' }),
      toolStep('edit', JSON.stringify({ path: 'a.txt', edits: [{ oldText: 'two', newText: '2' }] }), { id: 'e', patch: PATCH }),
    ]))
    expect(patchStats(changePatches(written!)[0]!)).toEqual({ additions: 2, deletions: 0 })
    expect(parseUnifiedPatch(changePatches(written!)[0]!)[0]?.lines.map((line) => line.text)).toEqual(['one', '2'])
    const [edited] = runFileChanges(runWith([toolStep('edit', '{"path":"b.txt"}', { patch: PATCH })]))
    expect(changePatches(edited!)).toEqual([PATCH])
  })
})

describe('patchStats', () => {
  it('排除文件头和无换行标记', () => {
    const patch = `${PATCH}\\ No newline at end of file\n`
    expect(patchStats(patch)).toEqual({ additions: 1, deletions: 1 })
    expect(patchStats('')).toEqual({ additions: 0, deletions: 0 })
  })

  it('块内以 "-- "/"++ " 开头的内容行照常计数', () => {
    const patch = '--- a.md\n+++ a.md\n@@ -1,2 +1,2 @@\n--- old rule\n+++ new rule\n keep\n'
    expect(patchStats(patch)).toEqual({ additions: 1, deletions: 1 })
    expect(parseUnifiedPatch(patch)[0]?.lines).toEqual([
      { kind: 'del', text: '-- old rule', oldNo: 1 },
      { kind: 'add', text: '++ new rule', newNo: 1 },
      { kind: 'context', text: 'keep', oldNo: 2, newNo: 2 },
    ])
  })
})

describe('parseUnifiedPatch', () => {
  it('解析多 hunk 行号并支持省略行数', () => {
    const patch = 'Index: a.txt\n--- a.txt\n+++ a.txt\n@@ -1 +1,2 @@\n-old\n+new\n+next\n@@ -10,2 +11,2 @@\n same\n-before\n+after\n\\ No newline at end of file\n'
    expect(parseUnifiedPatch(patch)).toEqual([
      {
        header: '@@ -1 +1,2 @@', oldStart: 1, newStart: 1,
        lines: [
          { kind: 'del', text: 'old', oldNo: 1 },
          { kind: 'add', text: 'new', newNo: 1 },
          { kind: 'add', text: 'next', newNo: 2 },
        ],
      },
      {
        header: '@@ -10,2 +11,2 @@', oldStart: 10, newStart: 11,
        lines: [
          { kind: 'context', text: 'same', oldNo: 10, newNo: 11 },
          { kind: 'del', text: 'before', oldNo: 11 },
          { kind: 'add', text: 'after', newNo: 12 },
        ],
      },
    ])
    expect(parseUnifiedPatch('')).toEqual([])
  })
})
