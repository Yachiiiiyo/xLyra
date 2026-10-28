import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { hubDeveloperQueryKeys } from '@/features/settings/api/hub-developer'
import type { JSPluginListItem } from '@/features/settings/api/js-plugins'
import { APIError } from '@/lib/http'
import common from '@/locales/zh/common.json'
import zh from '@/locales/zh/settings.json'
import { showMarketSubmit, publishedFromReview, samePublishedVersion, submitErrorText } from './market-submit-model'
import { MarketSubmitActions, MarketSubmitDialog } from './market-submit'

vi.mock('@/components/ui/dialog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/ui/dialog')>()
  const { createElement, Fragment } = await import('react')
  const pass = (tag: string) => ({ children, ...props }: { children?: unknown }) =>
    createElement(tag, props, children as never)
  return {
    ...actual,
    Dialog: ({ open, children }: { open?: boolean; children?: unknown }) =>
      (open ? createElement(Fragment, null, children as never) : null),
    DialogContent: pass('div'),
    DialogTitle: pass('h2'),
    DialogDescription: pass('p'),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { settings: zh, common } },
    interpolation: { escapeValue: false },
  })
})

function plugin(fields: Partial<JSPluginListItem>): JSPluginListItem {
  return {
    id: 'acme.demo',
    source: 'uploaded',
    enabled_version: '1.2.0',
    version_count: 1,
    local_version: '1.2.0',
    ...fields,
  }
}

function render(node: ReactNode, client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
    </QueryClientProvider>,
  )
}

describe('market submit entry', () => {
  it('hides the button unless the plugin is owned and ahead of the market', () => {
    expect(showMarketSubmit(plugin({ owned: false, market_ahead: true }))).toBe(false)
    expect(showMarketSubmit(plugin({ owned: true, market_ahead: false }))).toBe(false)
    expect(showMarketSubmit(plugin({}))).toBe(false)
    const hidden = render(<MarketSubmitActions plugin={plugin({ owned: false, market_ahead: true, submission: { id: 's', status: 'checking' } })} onOpen={() => {}} />)
    expect(hidden).not.toContain('提交到市场')
    expect(hidden).not.toContain('检查中')
    const notAhead = render(<MarketSubmitActions plugin={plugin({ owned: true, market_ahead: false })} onOpen={() => {}} />)
    expect(notAhead).not.toContain('提交到市场')
    const published = render(<MarketSubmitActions plugin={plugin({ owned: true, market_ahead: true, local_version: '1.0.0', submission: { id: 's', status: 'published', version: '1.0.0' } })} onOpen={() => {}} />)
    expect(published).not.toContain('提交到市场')
    expect(published).not.toContain('已发布')
    expect(publishedFromReview(undefined, 'published')).toBe(false)
    expect(publishedFromReview('published', 'published')).toBe(false)
    expect(publishedFromReview('in_review', 'published')).toBe(true)
    expect(publishedFromReview('checking', 'in_review')).toBe(false)
  })

  it('shows the button, and disables it while a submission is still open', () => {
    const open = render(<MarketSubmitActions plugin={plugin({ owned: true, market_ahead: true, market_version: '1.0.0' })} onOpen={() => {}} />)
    expect(open).toContain('提交到市场')
    expect(open).not.toContain('disabled=""')
    const pending = render(
      <MarketSubmitActions
        plugin={plugin({
          owned: true,
          market_ahead: true,
          submission: { id: 'sub-1', status: 'in_review', review_note: '等一下' },
        })}
        onOpen={() => {}}
      />,
    )
    expect(pending).toContain('审核中')
    expect(pending).toContain('提交到市场')
    expect(pending).toContain('disabled=""')
  })

  it('returns false from samePublishedVersion when either version is empty', () => {
    expect(samePublishedVersion(plugin({
      local_version: '1.2.0',
      submission: { id: 's', status: 'published', version: '' },
    }))).toBe(false)
    expect(samePublishedVersion(plugin({
      local_version: '',
      enabled_version: '',
      submission: { id: 's', status: 'published', version: '1.2.0' },
    }))).toBe(false)
  })

  it('shows hub unavailable instead of not a developer when developer identity cannot be read', () => {
    const markup = render(
      <MarketSubmitActions
        plugin={plugin({ owned: true, market_ahead: true })}
        developerUnavailable
        onOpen={() => {}}
      />,
    )
    expect(markup).toContain('暂时无法读取开发者身份，提交到市场暂不可用')
    expect(markup).not.toContain('还不是开发者')
    expect(markup).not.toContain('当前账号不是已激活的开发者')
    expect(markup).toContain('disabled=""')
    const text = submitErrorText(i18n.getFixedT('zh', 'settings'), new APIError('当前账号不是已激活的开发者', 502, 'hub_unavailable'))
    expect(text).toBe('暂时无法读取开发者身份，提交到市场暂不可用')
    expect(submitErrorText(i18n.getFixedT('zh', 'settings'), new APIError('当前账号不是已激活的开发者', 409, 'not_developer'))).toBe('当前账号不是已激活的开发者')
  })

  it('does not show a clickable submit button when the cache is empty and the query is pending review', () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    client.setQueryData(hubDeveloperQueryKeys.submissions('acme.demo'), { id: 'sub-1', status: 'in_review' })
    const markup = render(
      <MarketSubmitActions plugin={plugin({ owned: true, market_ahead: true })} onOpen={() => {}} />,
      client,
    )
    expect(markup).toContain('审核中')
    expect(markup).toMatch(/<button[^>]*disabled=""[^>]*aria-label="提交到市场"|<button[^>]*aria-label="提交到市场"[^>]*disabled=""/)
  })
})

describe('market submit dialog', () => {
  const base = plugin({ owned: true, market_ahead: true, market_version: '1.0.0', local_version: '1.2.0' })

  it('shows the version comparison, changelog field, and package summary', () => {
    const markup = render(
      <MarketSubmitDialog
        plugin={base}
        hosts={['api.example.com']}
        permissions={['notify']}
        signer="SHA256:abc"
        onClose={() => {}}
        onSubmit={() => {}}
      />,
    )
    expect(markup).toContain('本地版本')
    expect(markup).toContain('1.2.0')
    expect(markup).toContain('市场版本')
    expect(markup).toContain('1.0.0')
    expect(markup).toContain('更新日志')
    expect(markup).toContain('api.example.com')
    expect(markup).toContain('notify')
    expect(markup).toContain('SHA256:abc')
    expect(markup).toContain('acme.demo')
    expect(markup).toContain('<textarea')
  })

  it('says the market has no copy, and shows submitting and error states', () => {
    const empty = render(
      <MarketSubmitDialog
        plugin={{ ...base, market_version: '' }}
        hosts={[]}
        permissions={[]}
        signer=""
        onClose={() => {}}
        onSubmit={() => {}}
      />,
    )
    expect(empty).toContain('市场尚无此插件')
    expect(empty).toContain('无')
    const busy = render(
      <MarketSubmitDialog
        plugin={base}
        hosts={[]}
        permissions={[]}
        signer="SHA256:abc"
        submitting
        error="版本号必须高于市场已发布版本"
        onClose={() => {}}
        onSubmit={() => {}}
      />,
    )
    expect(busy).toContain('提交中…')
    expect(busy).toContain('版本号必须高于市场已发布版本')
    expect(busy).toContain('disabled=""')
  })

  it('renders nothing while closed', () => {
    const markup = render(
      <MarketSubmitDialog plugin={null} hosts={[]} permissions={[]} signer="" onClose={() => {}} onSubmit={() => {}} />,
    )
    expect(markup).toBe('')
  })
})
