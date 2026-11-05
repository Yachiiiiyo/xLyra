import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentTimeline } from '@/features/agent/components/agent-timeline'
import type { AgentRun, AgentTimelineItem } from '@/features/agent/lib/agent-events'
import zhAgent from '@/locales/zh/agent.json'

const rendered = vi.hoisted(() => ({ paths: [] as string[] }))
vi.mock('@/features/agent/components/agent-generated-image', () => ({
  AgentGeneratedImage: ({ path, compact = false }: { path: string; compact?: boolean }) => {
    rendered.paths.push(path)
    return <span data-generated-image={path} data-compact={compact} />
  },
}))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})
beforeEach(() => { rendered.paths = [] })

function timeline(reply: string, status: AgentRun['status'] = 'done'): AgentTimelineItem[] {
  const image = { path: 'images/a.png', mime: 'image/png', bytes: 1 }
  return [{
    kind: 'run', id: 'run', run: {
      id: 'run', status, finalText: reply, permissions: [], startedAt: 0, endedAt: 1, elapsedMs: 1,
      steps: [
        { id: 'image', kind: 'tool', title: 'image_generation', status: status === 'running' ? 'running' : 'done', images: status === 'done' ? [image] : undefined },
        { id: 'text', kind: 'text', title: 'text', status: 'done', detail: reply },
      ],
    },
  }]
}

function render(reply: string, sessionId = 'session-1') {
  return renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={timeline(reply)} sessionId={sessionId} /></I18nextProvider>)
}

describe('AgentTimeline 生图结果', () => {
  it('回复已引用图片时不在画廊重复', () => {
    const html = render('结果：![猫](images/a.png)')
    expect(rendered.paths).toEqual(['images/a.png'])
    expect(html).not.toContain('data-generated-gallery')
  })

  it('回复未引用图片时显示兜底画廊', () => {
    const html = render('生成好了')
    expect(rendered.paths).toEqual(['images/a.png'])
    expect(html).toContain('data-generated-gallery')
    expect(html.indexOf('data-generated-gallery')).toBeLessThan(html.indexOf('生成好了'))
  })

  it('回复里引用了工具没产出过的图片路径(模型编造)不渲染,正确图片由画廊兜底', () => {
    const html = render('![猫](images/wrong.png)')
    expect(rendered.paths).toEqual(['images/a.png'])
    expect(html).toContain('data-generated-gallery')
    expect(html).not.toContain('wrong.png')
  })

  it('没调用过生图工具却写了图片引用时整段不渲染图片', () => {
    const items: AgentTimelineItem[] = [{
      kind: 'run', id: 'run', run: {
        id: 'run', status: 'done', finalText: '已完成\n\n![说明](images/pelican.png)', permissions: [], startedAt: 0, endedAt: 1, elapsedMs: 1,
        steps: [{ id: 'text', kind: 'text', title: 'text', status: 'done', detail: '已完成\n\n![说明](images/pelican.png)' }],
      },
    }]
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={items} sessionId="session-1" /></I18nextProvider>)
    expect(rendered.paths).toEqual([])
    expect(html).not.toContain('pelican.png')
    expect(html).toContain('已完成')
  })

  it('之前轮次产出的图片,后面轮次的回复引用它仍能显示', () => {
    const image = { path: 'images/a.png', mime: 'image/png', bytes: 1 }
    const items: AgentTimelineItem[] = [
      { kind: 'run', id: 'r1', run: { id: 'r1', status: 'done', finalText: '好了', permissions: [], startedAt: 0, endedAt: 1, elapsedMs: 1, steps: [{ id: 's1', kind: 'tool', title: 'image_generation', status: 'done', images: [image] }, { id: 't1', kind: 'text', title: 'text', status: 'done', detail: '好了' }] } },
      { kind: 'run', id: 'r2', run: { id: 'r2', status: 'done', finalText: '如上图 ![猫](images/a.png)', permissions: [], startedAt: 2, endedAt: 3, elapsedMs: 1, steps: [{ id: 't2', kind: 'text', title: 'text', status: 'done', detail: '如上图 ![猫](images/a.png)' }] } },
    ]
    renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={items} sessionId="session-1" /></I18nextProvider>)
    expect(rendered.paths).toContain('images/a.png')
    expect(rendered.paths.filter((path) => path === 'images/a.png').length).toBeGreaterThanOrEqual(2)
  })

  it('未传 sessionId 时不渲染会话图片', () => {
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={timeline('![猫](images/a.png)')} /></I18nextProvider>)
    expect(rendered.paths).toEqual([])
    expect(html).toContain('<img src="images/a.png" alt="猫"/>')
    expect(html).not.toContain('data-generated-gallery')
  })

  it('运行中生图步骤显示生成文案', () => {
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={timeline('', 'running')} sessionId="session-1" /></I18nextProvider>)
    expect(html).toContain(zhAgent.chat.image.generating)
  })
})

describe('AgentTimeline 工作区链接', () => {
  it('相对文件链接打开预览，外链保留安全属性', () => {
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={timeline('[说明](docs/a.md) [站点](https://example.com)')} sessionId="session-1" /></I18nextProvider>)
    expect(html).toContain('<button type="button"')
    expect(html).toContain('说明')
    expect(html).toContain('<a href="https://example.com" target="_blank" rel="noopener noreferrer">站点</a>')
  })
})

describe('AgentTimeline 权限卡片', () => {
  function permissionTimeline(): AgentTimelineItem[] {
    return [{
      kind: 'run', id: 'run', run: {
        id: 'run', status: 'running', finalText: '', startedAt: 0, steps: [],
        permissions: [
          { id: 'p1', tool: 'bash', detail: 'cd . && cat > gen.js <<EOF\nconst a = 1;\nEOF', decision: 'allowed', scope: 'session', auto: true, position: 0 },
          { id: 'p2', tool: 'edit', detail: 'pelican-bicycle.html', decision: 'denied', position: 0 },
          { id: 'p3', tool: 'write', detail: 'notes.md', position: 0 },
        ],
      },
    }]
  }

  it('已处理的权限请求收成一行,待处理的仍是完整卡片', () => {
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={permissionTimeline()} sessionId="session-1" /></I18nextProvider>)
    // 只有待处理的 p3 保留完整卡片(标题「权限请求」+ 代码块 + 按钮)
    expect(html.split(zhAgent.permission.title)).toHaveLength(2)
    expect(html.match(/<pre/g) ?? []).toHaveLength(1)
    expect(html).toContain(zhAgent.permission.allow)
    // 已处理的只留工具名、首行与结果,多行内容不展开到正文里
    expect(html).toContain(zhAgent.permission.autoAllowed)
    expect(html).toContain(zhAgent.permission.denied)
    expect(html).toContain('cd . &amp;&amp; cat &gt; gen.js')
    expect(html).not.toMatch(/>const a = 1;</)
  })
})

describe('AgentTimeline 提问', () => {
  const items = { header: '风格', question: '选哪种风格?', multiSelect: false, options: [{ label: '简约' }, { label: '复古' }] }
  function questionTimeline(status: AgentRun['status'], question: NonNullable<AgentRun['questions']>[number]): AgentTimelineItem[] {
    return [{
      kind: 'run', id: 'run', run: {
        id: 'run', status, finalText: '我会按你的选择继续', permissions: [], startedAt: 0, endedAt: status === 'running' ? undefined : 1, elapsedMs: 1,
        questions: [question],
        steps: [
          { id: 'q', kind: 'tool', title: 'question', status: 'done' },
          { id: 'text', kind: 'text', title: 'text', status: 'done', detail: '我会按你的选择继续' },
        ],
      },
    }]
  }
  const renderQuestion = (list: AgentTimelineItem[]) => renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentTimeline items={list} sessionId="session-1" /></I18nextProvider>)

  it('待回答的提问卡片在回复文字之前,question 工具步骤不显示', () => {
    const html = renderQuestion(questionTimeline('running', { id: 'q1', toolCallId: 'c1', items: [items], status: 'pending' }))
    expect(html).toContain(zhAgent.question.title)
    expect(html).toContain('选哪种风格?')
    expect(html.indexOf(zhAgent.question.title)).toBeLessThan(html.indexOf('我会按你的选择继续'))
    expect(html).not.toContain('>question<')
  })

  it('已回答的摘要在回复区、回复文字之前,不再显示卡片', () => {
    const html = renderQuestion(questionTimeline('done', { id: 'q1', toolCallId: 'c1', items: [items], status: 'answered', answers: [{ selected: ['复古'] }] }))
    expect(html).toContain('选哪种风格?')
    expect(html).toContain('复古')
    expect(html).not.toContain(zhAgent.question.title)
    expect(html.indexOf('复古')).toBeLessThan(html.indexOf('我会按你的选择继续'))
  })

  it('选了「其他」时摘要显示补充文字;跳过与取消各有固定文案', () => {
    const other = renderQuestion(questionTimeline('done', { id: 'q1', toolCallId: 'c1', items: [items], status: 'answered', answers: [{ selected: ['__other__'], other: '再来点复古' }] }))
    expect(other).toContain('再来点复古')
    const skipped = renderQuestion(questionTimeline('done', { id: 'q1', toolCallId: 'c1', items: [items], status: 'skipped' }))
    expect(skipped).toContain(zhAgent.question.skippedLabel)
    const cancelled = renderQuestion(questionTimeline('done', { id: 'q1', toolCallId: 'c1', items: [items], status: 'cancelled' }))
    expect(cancelled).toContain(zhAgent.question.cancelledLabel)
  })
})
