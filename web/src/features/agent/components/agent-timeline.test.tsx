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

  it('错误引用仍渲染，正确图片由画廊兜底', () => {
    const html = render('![猫](images/wrong.png)')
    expect(rendered.paths).toEqual(['images/a.png', 'images/wrong.png'])
    expect(html).toContain('data-generated-gallery')
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
