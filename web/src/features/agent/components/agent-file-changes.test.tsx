import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it } from 'vitest'
import { AgentDiffView } from '@/features/agent/components/agent-diff-view'
import { AgentRunChanges } from '@/features/agent/components/agent-run-changes'
import { AgentTimeline } from '@/features/agent/components/agent-timeline'
import type { AgentRun, AgentTimelineItem, AgentWorkStep } from '@/features/agent/lib/agent-events'
import type { AgentFileChange } from '@/features/agent/lib/run-changes'
import zhAgent from '@/locales/zh/agent.json'

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

function render(node: React.ReactNode) {
  return renderToStaticMarkup(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>)
}

const patch = '--- a.txt\n+++ a.txt\n@@ -1 +1 @@\n-old\n+new\n'
const writeStep: AgentWorkStep = { id: 'write', kind: 'tool', title: 'write', status: 'done', input: '{"path":"src/a.txt","content":"hello"}' }
const editStep: AgentWorkStep = { id: 'edit', kind: 'tool', title: 'edit', status: 'done', input: '{"path":"src/b.txt"}', patch }

function timelineWith(steps: AgentWorkStep[]): AgentTimelineItem[] {
  const run: AgentRun = {
    id: 'run-1', status: 'done', finalText: '回复文字', permissions: [], startedAt: 0, endedAt: 1,
    steps: [...steps, { id: 'text', kind: 'text', title: 'text', status: 'done', detail: '回复文字' }],
  }
  return [{ kind: 'run', id: run.id, run }]
}

describe('AgentTimeline 本轮变更', () => {
  it('在回复之后渲染 write/edit 变更区', () => {
    const html = render(<AgentTimeline items={timelineWith([writeStep, editStep])} sessionId="session-1" onOpenChange={() => {}} />)
    expect(html).toContain(zhAgent.changes.title)
    expect(html).toContain('src/a.txt')
    expect(html).toContain('src/b.txt')
    expect(html.indexOf('回复文字')).toBeLessThan(html.indexOf(zhAgent.changes.title))
  })

  it('运行中不显示变更区,结束后才出现', () => {
    const items = timelineWith([writeStep])
    const running = items.map((item) => item.kind === 'run' ? { ...item, run: { ...item.run, status: 'running' as const } } : item)
    expect(render(<AgentTimeline items={running} sessionId="session-1" onOpenChange={() => {}} />)).not.toContain(zhAgent.changes.title)
  })

  it('没有回调或 sessionId 时不渲染', () => {
    const items = timelineWith([writeStep])
    expect(render(<AgentTimeline items={items} sessionId="session-1" />)).not.toContain(zhAgent.changes.title)
    expect(render(<AgentTimeline items={items} onOpenChange={() => {}} />)).not.toContain(zhAgent.changes.title)
  })

  it('error 步骤不计入变更', () => {
    const html = render(<AgentTimeline items={timelineWith([{ ...editStep, status: 'error' }])} sessionId="session-1" onOpenChange={() => {}} />)
    expect(html).not.toContain(zhAgent.changes.title)
  })
})

describe('AgentRunChanges', () => {
  it('默认只显示五个文件并提供展开入口', () => {
    const changes: AgentFileChange[] = Array.from({ length: 6 }, (_, index) => ({
      path: `file-${index + 1}.txt`, kind: 'write', additions: 1, deletions: null, patches: [], content: 'x',
    }))
    const html = render(<AgentRunChanges changes={changes} onOpen={() => {}} />)
    expect(html.match(/data-change-path=/g)).toHaveLength(5)
    expect(html).toContain('展开全部 6 个文件')
    expect(html).not.toContain('file-6.txt')
    expect(render(<AgentRunChanges changes={[]} onOpen={() => {}} />)).toBe('')
  })

  it('后端清单项按 added/modified/deleted 渲染状态与删除线', () => {
    const server = (path: string, status: 'added' | 'modified' | 'deleted', over: Partial<AgentFileChange> = {}): AgentFileChange => ({
      path, kind: status === 'added' ? 'write' : 'edit', additions: 1, deletions: 1, patches: [],
      status, turnId: 'turn-1', size: 10, binary: false, hasDiff: true, truncated: false, source: 'server', ...over,
    })
    const html = render(<AgentRunChanges changes={[server('a.txt', 'added'), server('b.txt', 'modified'), server('c.txt', 'deleted')]} onOpen={() => {}} />)
    expect(html).toContain(zhAgent.changes.kindAdded)
    expect(html).toContain(zhAgent.changes.kindModified)
    expect(html).toContain(zhAgent.changes.kindDeleted)
    expect(html).toContain('line-through')
  })

  it('二进制文件显示大小而不是 +n −m', () => {
    const binary: AgentFileChange = {
      path: 'b.bin', kind: 'edit', additions: 0, deletions: 0, patches: [],
      status: 'modified', turnId: 'turn-1', size: 4096, binary: true, hasDiff: false, truncated: false, source: 'server',
    }
    const html = render(<AgentRunChanges changes={[binary]} onOpen={() => {}} />)
    const row = html.slice(html.indexOf('<button'))
    expect(row).toContain('4 KB')
    expect(row).not.toContain('+')
  })

  it('total 大于列表长度时提示未列出的文件,incomplete 时提示可能不完整', () => {
    const one: AgentFileChange = {
      path: 'a.txt', kind: 'write', additions: 1, deletions: 0, patches: [],
      status: 'added', turnId: 'turn-1', size: 2, binary: false, hasDiff: true, truncated: false, source: 'server',
    }
    const html = render(<AgentRunChanges changes={[one]} total={8} incomplete onOpen={() => {}} />)
    expect(html).toContain(zhAgent.changes.omitted.replace('{{count}}', '7'))
    expect(html).toContain(zhAgent.changes.incomplete)
    const plain = render(<AgentRunChanges changes={[one]} onOpen={() => {}} />)
    expect(plain).not.toContain(zhAgent.changes.omitted.replace('{{count}}', '0'))
    expect(plain).not.toContain(zhAgent.changes.incomplete)
  })
})

describe('AgentTimeline 后端变更', () => {
  it('有 run_changes 时渲染后端清单(状态文字来自后端 kind)', () => {
    const run: AgentRun = {
      id: 'run-1', status: 'done', finalText: '回复', permissions: [], startedAt: 0, endedAt: 1, steps: [],
      changes: { turnId: 'turn-1', total: 1, incomplete: false, files: [{ path: 'images/out.png', kind: 'added', size: 8, additions: 0, deletions: 0, has_diff: true }] },
    }
    const html = render(<AgentTimeline items={[{ kind: 'run', id: 'run-1', run }]} sessionId="session-1" onOpenChange={() => {}} />)
    expect(html).toContain(zhAgent.changes.kindAdded)
    expect(html).toContain('images/out.png')
  })
})

describe('AgentDiffView', () => {
  it('渲染多 hunk 行号与增删行标签', () => {
    const twoHunks = '--- a.txt\n+++ a.txt\n@@ -1 +1,2 @@\n-old\n+new\n+next\n@@ -10,2 +11,2 @@\n same\n-before\n+after\n'
    const html = render(<AgentDiffView patches={[twoHunks]} />)
    expect(html).toContain('@@ -1 +1,2 @@')
    expect(html).toContain('@@ -10,2 +11,2 @@')
    expect(html).toContain(`aria-label="${zhAgent.changes.addedLine}"`)
    expect(html).toContain(`aria-label="${zhAgent.changes.removedLine}"`)
    expect(html).toContain('>10</span>')
    expect(html).toContain('>11</span>')
  })
})
