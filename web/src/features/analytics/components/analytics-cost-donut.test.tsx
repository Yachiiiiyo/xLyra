import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it } from 'vitest'
import type { AnalyticsBreakdownItem, AnalyticsUsage } from '@/features/analytics/api/analytics'
import translations from '@/locales/zh/analytics.json'
import { AnalyticsCostDonut } from './analytics-cost-donut'

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { analytics: translations } },
    interpolation: { escapeValue: false },
  })
})

function breakdownItem(overrides: Partial<AnalyticsBreakdownItem> & Pick<AnalyticsBreakdownItem, 'key' | 'label'>): AnalyticsBreakdownItem {
  return {
    id: overrides.key,
    requests: 10,
    success_count: 10,
    failure_count: 0,
    success_rate: 1,
    prompt_tokens: 1000,
    completion_tokens: 500,
    cached_tokens: 0,
    total_tokens: 1500,
    cost: 0,
    avg_latency_ms: 100,
    max_latency_ms: 200,
    ...overrides,
  }
}

function makeUsage(models: AnalyticsBreakdownItem[]): AnalyticsUsage {
  const totalCost = models.reduce((sum, item) => sum + item.cost, 0)
  const totalTokens = models.reduce((sum, item) => sum + item.total_tokens, 0)
  const totalRequests = models.reduce((sum, item) => sum + item.requests, 0)
  return {
    meta: {
      from: '2026-11-01',
      to: '2026-11-26',
      days: 26,
      timezone: 'UTC',
      generated_at: '2026-11-26T12:00:00Z',
      group_by: 'model',
      currency: 'USD',
      available_currencies: ['USD'],
      granularity: 'day',
      filters: { site_ids: [], model_keys: [], api_key_ids: [], success: true },
    },
    totals: {
      requests: totalRequests,
      success_count: totalRequests,
      failure_count: 0,
      success_rate: 1,
      prompt_tokens: totalTokens,
      completion_tokens: 0,
      cached_tokens: 0,
      total_tokens: totalTokens,
      cache_hit_rate: 0,
      cost: totalCost,
      cost_by_currency: { USD: totalCost },
      avg_latency_ms: 100,
      max_latency_ms: 200,
      avg_upstream_latency_ms: 80,
    },
    breakdowns: {
      site: [],
      model: models,
      api_key: [],
      matrix: [],
    },
    series: [],
    api_key_contributions: [],
  }
}

describe('AnalyticsCostDonut', () => {
  it('keeps unpriced models visible alongside priced models and notes pricing gaps', () => {
    const usage = makeUsage([
      breakdownItem({ key: 'gpt-5', label: 'gpt-5', cost: 12.5, total_tokens: 200_000, requests: 20 }),
      breakdownItem({ key: 'k3.1-pro', label: 'k3.1-pro', cost: 0, total_tokens: 1_500_000, requests: 80 }),
    ])

    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AnalyticsCostDonut
          usage={usage}
          onDrillDown={() => undefined}
          onClearDrillDown={() => undefined}
          activeSiteIds={[]}
          activeModelKeys={[]}
          activeApiKeyIds={[]}
        />
      </I18nextProvider>,
    )

    expect(markup).toContain('gpt-5')
    expect(markup).toContain('k3.1-pro')
    expect(markup).toContain('无计费')
    expect(markup).toContain('含 1 项无估算费用')
  })

  it('falls back to token breakdown instead of empty state when all usage is unpriced', () => {
    const usage = makeUsage([
      breakdownItem({ key: 'k3.1-pro', label: 'k3.1-pro', cost: 0, total_tokens: 2_000_000, requests: 40 }),
      breakdownItem({ key: 'k3', label: 'k3', cost: 0, total_tokens: 500_000, requests: 10 }),
    ])

    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AnalyticsCostDonut
          usage={usage}
          onDrillDown={() => undefined}
          onClearDrillDown={() => undefined}
          activeSiteIds={[]}
          activeModelKeys={[]}
          activeApiKeyIds={[]}
        />
      </I18nextProvider>,
    )

    expect(markup).not.toContain('暂无费用数据')
    expect(markup).toContain('总 Tokens')
    expect(markup).toContain('当前按 Tokens 展示构成')
    expect(markup).toContain('k3.1-pro')
    expect(markup).toContain('k3')
    expect(markup).toContain('无计费')
  })

  it('renders empty state when there is no usage at all', () => {
    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AnalyticsCostDonut
          usage={makeUsage([])}
          onDrillDown={() => undefined}
          onClearDrillDown={() => undefined}
          activeSiteIds={[]}
          activeModelKeys={[]}
          activeApiKeyIds={[]}
        />
      </I18nextProvider>,
    )

    expect(markup).toContain('暂无费用数据')
  })
})
