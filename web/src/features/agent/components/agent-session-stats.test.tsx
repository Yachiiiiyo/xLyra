import type { ReactNode } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentSessionStats, ContextWindowDetails } from '@/features/agent/components/agent-session-stats'
import type { AgentSessionStats as AgentSessionStatsData } from '@/features/agent/api/agent'
import zhAgent from '@/locales/zh/agent.json'

const layout = vi.hoisted(() => ({ mobile: false }))

vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => layout.mobile }))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    resources: { zh: { agent: zhAgent } },
    interpolation: { escapeValue: false },
  })
})

const fullStats: AgentSessionStatsData = {
  turns: 2,
  steps: 44,
  total_tokens: 2_600_000,
  input_tokens: 2_400_000,
  output_tokens: 200_000,
  cache_read_tokens: 1_980_000,
  cache_write_tokens: 120_000,
  cache_hit_rate: 0.83,
  tokens_per_second: 159,
  context_tokens: 54_000,
  context_window: 200_000,
  compact_ratio: 0.9,
}

const emptyStats: AgentSessionStatsData = {
  turns: 0,
  steps: 0,
  total_tokens: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cache_hit_rate: null,
  tokens_per_second: null,
  context_tokens: 0,
  context_window: 0,
  compact_ratio: 0.9,
}

function render(stats: AgentSessionStatsData | null, reserve = false) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <AgentSessionStats stats={stats} reserve={reserve} className="mt-2 text-xs text-faint" />
    </I18nextProvider>,
  )
}

function renderContent(content: ReactNode) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      {content}
    </I18nextProvider>,
  )
}

describe('AgentSessionStats 桌面端', () => {
  it('依次显示轮/步、tok/s、累计 token、缓存命中与上下文圆环', () => {
    layout.mobile = false
    const html = render(fullStats)
    expect(html).toContain('2 轮 44 步')
    expect(html).toContain('159 tok/s')
    expect(html).toContain('2.6M tok')
    expect(html).toContain('缓存命中 83%')
    expect(html).toContain('27%')
    expect(html).toContain('aria-label="总 Tokens"')
    expect(html).toContain('aria-label="上下文窗口"')
    expect(html).toContain('justify-self-start')
    expect(html).toContain('justify-self-center')
    expect(html).toContain('justify-self-end')
    expect(html).not.toContain('Enter')
  })

  it('空值项隐藏', () => {
    layout.mobile = false
    const html = render({ ...fullStats, tokens_per_second: null, cache_hit_rate: null })
    expect(html).toContain('2 轮 44 步')
    expect(html).toContain('2.6M tok')
    expect(html).not.toContain('tok/s')
    expect(html).not.toContain('缓存命中')
    expect(html).toContain('27%')
  })

  it('轮与步都为 0 时整行不渲染', () => {
    layout.mobile = false
    expect(render({ ...fullStats, turns: 0, steps: 0 })).toBe('')
  })

  it('无统计数据时不渲染', () => {
    layout.mobile = false
    expect(render(null)).toBe('')
  })
})

describe('AgentSessionStats 移动端', () => {
  it('只显示 tok/s、缓存命中与上下文圆环', () => {
    layout.mobile = true
    const html = render(fullStats)
    expect(html).toContain('159 tok/s')
    expect(html).toContain('缓存命中 83%')
    expect(html).toContain('27%')
    expect(html).toContain('aria-label="总 Tokens"')
    expect(html).toContain('aria-label="上下文窗口"')
    expect(html).not.toContain('2 轮 44 步')
    expect(html).not.toContain('2.6M tok')
    expect(html).toContain('justify-center')
    expect(html).not.toContain('Enter')
  })

  it('空值项隐藏，全部为空时不渲染', () => {
    layout.mobile = true
    const partial = render({ ...fullStats, tokens_per_second: null, cache_hit_rate: null })
    expect(partial).not.toContain('tok/s')
    expect(partial).not.toContain('缓存命中')
    expect(partial).toContain('27%')

    const empty = render({ ...fullStats, tokens_per_second: null, cache_hit_rate: null, context_window: 0 })
    expect(empty).toBe('')
  })
})

describe('AgentSessionStats 占位', () => {
  it('reserve 下 null 与全 0 统计在桌面端渲染默认行和可用浮窗触发元素', () => {
    layout.mobile = false
    for (const stats of [null, emptyStats]) {
      const html = render(stats, true)
      expect(html).toContain('0 轮 0 步')
      expect(html).toContain('0 tok')
      expect(html).toContain('>0%</span>')
      expect(html).toContain('min-h-5')
      expect(html).toContain('aria-label="总 Tokens"')
      expect(html).toContain('aria-label="上下文窗口"')
      expect(html).not.toContain('tok/s')
      expect(html).not.toContain('缓存命中')
    }
  })

  it('reserve 下 null 与全 0 统计在移动端只渲染默认圆环', () => {
    layout.mobile = true
    for (const stats of [null, emptyStats]) {
      const html = render(stats, true)
      expect(html).toContain('>0%</span>')
      expect(html).toContain('min-h-5')
      expect(html).toContain('aria-label="上下文窗口"')
      expect(html).not.toContain('0 轮 0 步')
      expect(html).not.toContain('0 tok')
      expect(html).not.toContain('缓存命中')
    }
  })

  it('不传 reserve 时 null 与全 0 统计仍不渲染', () => {
    for (const mobile of [false, true]) {
      layout.mobile = mobile
      expect(render(null)).toBe('')
      expect(render(emptyStats)).toBe('')
    }
  })

  it('占位与真实数据在桌面和移动端使用相同的固定高度类', () => {
    for (const mobile of [false, true]) {
      layout.mobile = mobile
      const placeholderClass = render(null, true).match(/^<div class="([^"]*)"/)?.[1]
      const populatedClass = render(fullStats, true).match(/^<div class="([^"]*)"/)?.[1]
      expect(placeholderClass).toBe(populatedClass)
      expect(placeholderClass).toContain('min-h-5')
      expect(placeholderClass).toContain('items-center')
    }
  })
})

describe('AgentSessionStats 上下文圆环', () => {
  it('70% 以下用弱化色，70% 起到水位线前用琥珀色，达到水位线用红色', () => {
    layout.mobile = false
    // 60% < 70%：弱化色
    expect(render({ ...fullStats, context_tokens: 120_000 })).toContain('stroke-label-3')
    // 75%：琥珀色
    const warning = render({ ...fullStats, context_tokens: 150_000 })
    expect(warning).toContain('stroke-warning')
    expect(warning).not.toContain('stroke-danger')
    // 90% 达到水位线：红色
    const danger = render({ ...fullStats, context_tokens: 180_000 })
    expect(danger).toContain('stroke-danger')
    expect(danger).not.toContain('stroke-warning')
  })

  it('圆环百分比封顶 100%', () => {
    layout.mobile = false
    expect(render({ ...fullStats, context_tokens: 400_000 })).toContain('100%')
  })

  it('圆环与百分比触发上下文浮窗，不再使用原生 title', () => {
    layout.mobile = false
    const html = render(fullStats)
    expect(html).toContain('aria-label="上下文窗口"')
    expect(html).toContain('role="button"')
    expect(html).toContain('tabindex="0"')
    expect(html).toContain('aria-haspopup="dialog"')
    expect(html).not.toContain('title="上下文已用')
  })
})

describe('ContextWindowDetails', () => {
  it('显示总上下文、当前上下文、占用比例与压缩水位线', () => {
    const html = renderContent(<ContextWindowDetails stats={fullStats} />)
    expect(html).toContain('总上下文')
    expect(html).toContain('200,000')
    expect(html).toContain('当前上下文')
    expect(html).toContain('54,000')
    expect(html).toContain('占用比例')
    expect(html).toContain('27%')
    expect(html).toContain('自动压缩水位线')
    expect(html).toContain('90% · 约 180,000 tokens')
  })

  it('占用比例封顶 100%', () => {
    const html = renderContent(<ContextWindowDetails stats={{ ...fullStats, context_tokens: 400_000 }} />)
    expect(html).toContain('100%')
  })
})
