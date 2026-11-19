import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { Site } from '@/features/sites/api/sites'
import translations from '@/locales/zh/sites.json'
import { movePlugin } from '@/features/sites/lib/site-plugin-order'
import { SitePluginOrderSection } from './site-plugin-order'

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: [{ id: 'alpha', name: 'Alpha 插件' }, { id: 'beta', name: 'Beta 插件' }] }),
  useMutation: () => ({ isPending: false, mutate: () => undefined }),
  useQueryClient: () => ({ invalidateQueries: async () => undefined }),
}))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({ lng: 'zh', resources: { zh: { sites: translations } }, interpolation: { escapeValue: false } })
})

function siteWith(pluginIds: string[] | undefined): Site {
  return {
    id: 'site-1',
    name: '示例站点',
    slug: 'demo',
    site_type: 'openai',
    base_url: 'https://example.com',
    enabled: true,
    gateway_config: pluginIds ? { plugin_ids: pluginIds } : undefined,
  } as Site
}

function renderSection(pluginIds: string[] | undefined) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <SitePluginOrderSection site={siteWith(pluginIds)} />
    </I18nextProvider>,
  )
}

describe('SitePluginOrderSection', () => {
  it('renders nothing when no plugin or only one plugin is mounted', () => {
    expect(renderSection(undefined)).toBe('')
    expect(renderSection([])).toBe('')
    expect(renderSection(['only'])).toBe('')
  })

  it('renders mounted plugins in plugin_ids order and disables save until the order changes', () => {
    const markup = renderSection(['alpha', 'beta'])
    expect(markup.indexOf('Alpha 插件')).toBeGreaterThan(-1)
    expect(markup.indexOf('Alpha 插件')).toBeLessThan(markup.indexOf('Beta 插件'))
    expect(markup).toContain('alpha')
    expect(markup).toContain('beta')
    expect(markup).toContain('越靠前优先级越高')
    expect(markup).toContain('disabled=""')
    expect(markup).toContain('保存顺序')
  })
})

describe('movePlugin', () => {
  it('moves a plugin before the drop target and ignores unknown ids', () => {
    expect(movePlugin(['a', 'b', 'c'], 'c', 'a')).toEqual(['c', 'a', 'b'])
    expect(movePlugin(['a', 'b', 'c'], 'a', 'a')).toEqual(['a', 'b', 'c'])
    expect(movePlugin(['a', 'b'], 'missing', 'a')).toEqual(['a', 'b'])
  })
})
