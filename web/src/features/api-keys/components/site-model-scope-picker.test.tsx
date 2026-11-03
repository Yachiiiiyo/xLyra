import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { SiteModelScopePicker } from './site-model-scope-picker'
import type { Site } from '@/features/sites/api/sites'
import zhApiKeys from '@/locales/zh/api-keys.json'

const layout = vi.hoisted(() => ({ mobile: true }))

vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => layout.mobile }))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'api-keys', resources: { zh: { 'api-keys': zhApiKeys } } })
})

const sites = [
  { id: 'alpha', name: 'Alpha Site', slug: 'alpha', site_type: 'openai', enabled: true },
  { id: 'beta', name: 'Beta Site', slug: 'beta', site_type: 'anthropic', enabled: true },
] as Site[]
const models = [
  { id: 'alpha-model', site_id: 'alpha', upstream_model_name: 'alpha-model', display_name: 'Alpha Model', canonical_model_id: 'canonical-alpha', capabilities: {}, status: 'active', created_at: '', updated_at: '' },
  { id: 'beta-model', site_id: 'beta', upstream_model_name: 'beta-model', display_name: 'Beta Model', canonical_model_id: 'canonical-beta', capabilities: {}, status: 'active', created_at: '', updated_at: '' },
]

function render() {
  const scope = {
    sortedSites: sites,
    enabledSites: sites,
    inheritedSiteIdSet: new Set<string>(),
    selectedDirectSiteIdSet: new Set(['alpha', 'beta']),
    effectiveSiteIds: ['alpha', 'beta'],
    effectiveModelPolicy: 'allow_list',
    siteModelsLoading: false,
    siteModelRows: models.map((model) => ({ site: sites.find((site) => site.id === model.site_id)!, model })),
    selectedSiteModelIds: ['alpha-model'],
    validSelectedSiteModelCount: 1,
  }
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <SiteModelScopePicker
        scope={scope as never}
        canonicalModels={[
          { id: 'canonical-alpha', model_key: 'alpha-key', display_name: 'Alpha', capabilities: {} },
          { id: 'canonical-beta', model_key: 'beta-key', display_name: 'Beta', capabilities: {} },
        ] as never}
        sitePolicy="allow_list"
        modelPolicy="allow_list"
        siteIds={['alpha', 'beta']}
        siteModelIds={['alpha-model']}
        onChange={() => {}}
      />
    </I18nextProvider>,
  )
}

describe('SiteModelScopePicker 移动端', () => {
  it('移动端使用站点切换条和整宽模型列表', () => {
    layout.mobile = true
    const html = render()
    expect(html).toContain('data-mobile-site-tab="alpha"')
    expect(html).toContain('Alpha Site')
    expect(html).toContain('1/1')
    expect(html).toContain('Beta Site')
    expect(html).toContain('0/1')
    expect(html).toContain('alpha-key')
    expect(html).toContain('role="tablist"')
    expect(html).toContain('max-h-[264px]')
    expect(html).toContain('whitespace-nowrap')
    expect(html).not.toContain('height:320px')
  })

  it('桌面端也使用站点 Tabs 与整宽模型列表', () => {
    layout.mobile = false
    const html = render()
    expect(html).toContain('data-mobile-site-tab="alpha"')
    expect(html).toContain('Alpha Site')
    expect(html).toContain('1/1')
    expect(html).toContain('Beta Site')
    expect(html).toContain('0/1')
    expect(html).toContain('alpha-key')
    expect(html).toContain('role="tablist"')
    expect(html).toContain('max-h-[264px]')
    expect(html).toContain('whitespace-nowrap')
    expect(html).not.toContain('height:320px')
  })
})
