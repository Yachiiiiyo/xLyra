import * as React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentScopePane } from './agent-scope-pane'
import type { AgentImageModelSelection } from '@/features/agent/api/agent'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

const entry = vi.hoisted(() => ({ current: 'closed' as 'closed' | 'preview' | 'config' | 'image', stateIndex: 0 }))
const config = vi.hoisted(() => ({ open: false }))
const imageConfig = vi.hoisted(() => ({ open: false }))
const overview = vi.hoisted(() => ({ imageModels: [] as AgentImageModelSelection[], sites: [] as Array<{ site_id: string; site_name: string; site_type: string; enabled: boolean; models: Array<{ id: string; upstream_model_name: string; display_name: string; canonical_model_id: string; model_key?: string }> }> }))

vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof React>()
  return {
    ...actual,
    useState: <T,>(initial: T | (() => T)) => {
      if (initial !== false) return actual.useState(initial)
      const index = entry.stateIndex++
      const open = (entry.current === 'preview' && index === 0)
        || (entry.current === 'config' && index === 1)
        || (entry.current === 'image' && index === 2)
      return actual.useState(open as T)
    },
  }
})
vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children, open }: { children?: React.ReactNode; open?: boolean }) => open === false ? null : React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    const key = queryKey.at(-1)
    if (key === 'runtime') return { data: { allowed_site_ids: [], allowed_site_model_ids: [], site_policy: 'allow_all', model_policy: 'allow_all', image_models: overview.imageModels }, isLoading: false }
    if (key === 'sites') return { data: { items: overview.sites.map((site) => ({ id: site.site_id, name: site.site_name, site_type: site.site_type, enabled: site.enabled })) }, isLoading: false }
    if (queryKey.at(-2) === 'models' && key === 'all') return {
      data: Object.fromEntries(overview.sites.map((site) => [site.site_id, site.models.map((model) => ({ ...model, site_id: site.site_id, capabilities: {}, status: 'active' }))])),
      isLoading: false,
    }
    return { data: { items: [] }, isLoading: false }
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: () => ({ mutate: vi.fn(), isPending: false }),
}))
vi.mock('@/features/agent/components/agent-scope-config-dialog', () => ({
  AgentScopeConfigDialog: (props: { open: boolean }) => {
    config.open = props.open
    return props.open ? React.createElement('div', null, zhAgent.settings.scope.configDialogTitle) : null
  },
}))
vi.mock('@/features/agent/components/agent-image-model-dialog', () => ({
  AgentImageModelDialog: (props: { open: boolean }) => {
    imageConfig.open = props.open
    return props.open ? React.createElement('div', null, zhAgent.settings.scope.imageDialogTitle) : null
  },
}))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', ns: ['agent', 'common', 'components'], resources: { zh: { agent: zhAgent, common: zhCommon } } })
})
beforeEach(() => {
  entry.current = 'closed'
  config.open = false
  imageConfig.open = false
  overview.imageModels = []
  overview.sites = [{ site_id: 'site-1', site_name: 'Site', site_type: 'openai', enabled: true, models: [{ id: 'model-1', upstream_model_name: 'gpt', display_name: 'GPT', canonical_model_id: '' }] }]
})

function render() {
  entry.stateIndex = 0
  return renderToStaticMarkup(<I18nextProvider i18n={i18n}><MemoryRouter><AgentScopePane /></MemoryRouter></I18nextProvider>)
}

describe('AgentScopePane 概览', () => {
  it('统计文字可聚焦，配置入口存在且没有内嵌保存按钮', () => {
    const html = render()
    expect(html).toContain('<button type="button"')
    expect(html).toContain(zhAgent.settings.scope.allSites)
    expect(html.match(/>配置</g)).toHaveLength(2)
    expect(html).not.toContain(zhAgent.settings.save)
    expect(html).not.toContain(zhAgent.settings.scope.configDialogTitle)
    expect(html).toContain(zhAgent.settings.scope.imageNotConfigured)
  })

  it('点击统计文字后打开只读查看弹窗', () => {
    entry.current = 'preview'
    const html = render()
    expect(html).toContain(zhAgent.settings.scope.previewSearch)
    expect(config.open).toBe(false)
    expect(imageConfig.open).toBe(false)
  })

  it('可用模型配置入口只打开白名单配置弹窗', () => {
    entry.current = 'config'
    const html = render()
    expect(html).toContain(zhAgent.settings.scope.configDialogTitle)
    expect(config.open).toBe(true)
    expect(imageConfig.open).toBe(false)
  })

  it('生图配置入口只打开生图多选弹窗', () => {
    entry.current = 'image'
    const html = render()
    expect(html).toContain(zhAgent.settings.scope.imageDialogTitle)
    expect(config.open).toBe(false)
    expect(imageConfig.open).toBe(true)
  })

  it('生图摘要按已选顺序用箭头连接模型名，不显示站点名', () => {
    overview.imageModels = [
      { model: 'second', site_ids: ['site-1'] },
      { model: 'gpt', site_ids: ['site-1'] },
    ]
    overview.sites = [{ ...overview.sites[0], models: [
      { id: 'model-1', upstream_model_name: 'gpt', display_name: 'GPT', canonical_model_id: '', model_key: 'gpt' },
      { id: 'model-2', upstream_model_name: 'second', display_name: 'Second', canonical_model_id: '', model_key: 'second' },
    ] }]
    const html = render()
    expect(html).toContain('Second</span></span><span> › <span>GPT</span>')
    expect(html).not.toContain('(Site)')
  })

  it('生图摘要不论限制几个站点都只显示模型名', () => {
    overview.imageModels = [
      { model: 'shared', site_ids: ['site-1', 'site-2'] },
      { model: 'unrestricted', site_ids: [] },
    ]
    overview.sites = [
      { ...overview.sites[0], models: [{ id: 'model-1', upstream_model_name: 'shared', display_name: 'Shared', canonical_model_id: '', model_key: 'shared' }] },
      { ...overview.sites[0], site_id: 'site-2', site_name: 'Beta', models: [
        { id: 'model-2', upstream_model_name: 'shared', display_name: 'Shared', canonical_model_id: '', model_key: 'shared' },
        { id: 'model-3', upstream_model_name: 'unrestricted', display_name: 'Unrestricted', canonical_model_id: '', model_key: 'unrestricted' },
      ] },
    ]
    const html = render()
    expect(html).toContain('Shared</span></span><span> › <span>Unrestricted</span>')
    expect(html).not.toContain('Beta')
  })

  it('生图摘要为没有已启用选中站点提供的模型添加琥珀色和提示', () => {
    overview.imageModels = [
      { model: 'gone-model', site_ids: [] },
      { model: 'gpt', site_ids: [] },
    ]
    const html = render()
    expect(html).toContain(`<span class="text-amber-500" title="${zhAgent.settings.scope.imageSiteUnavailable}">gone-model</span>`)
    expect(html).toContain(' › ')
    overview.sites = [{ ...overview.sites[0], enabled: false }]
    const disabledHtml = render()
    expect(disabledHtml).toContain(`<span class="text-amber-500" title="${zhAgent.settings.scope.imageSiteUnavailable}">gpt</span>`)
  })
})
