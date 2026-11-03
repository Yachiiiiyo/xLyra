import * as React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentScopeConfigDialog } from './agent-scope-config-dialog'
import { updateAgentScopeSettings } from '@/features/agent/api/agent'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

const mutation = vi.hoisted(() => ({ options: undefined as undefined | { mutationFn: () => Promise<unknown> } }))
const runtime = vi.hoisted(() => ({ value: { allowed_site_ids: ['site-1'], allowed_site_model_ids: ['model-1'], site_policy: 'allow_list', model_policy: 'allow_list', image_model: { site_id: 'site-1', model: 'image-key' } } as Record<string, unknown> }))
const siteModels = vi.hoisted(() => ({ value: {} as Record<string, Array<{ id: string; upstream_model_name: string; display_name: string; canonical_model_id?: string; status: string; capabilities: Record<string, unknown> }>>, failed: false }))
const confirmOpen = vi.hoisted(() => ({ value: false }))
const selectedScope = vi.hoisted(() => ({ selectedSiteModelIds: ['model-1'] }))

vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof React>()
  return {
    ...actual,
    useState: <T,>(initial: T | (() => T)) => actual.useState(
      confirmOpen.value && initial === false ? true as T : initial,
    ),
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
    if (key === 'runtime') return { data: runtime.value, isLoading: false }
    if (key === 'sites') return siteModels.failed
      ? { data: undefined, isLoading: false, isError: true, refetch: vi.fn() }
      : { data: { items: [
        { id: 'site-1', name: 'Alpha', enabled: true, site_type: 'openai' },
        { id: 'site-2', name: 'Beta', enabled: true, site_type: 'openai' },
        { id: 'site-3', name: 'Disabled', enabled: false, site_type: 'openai' },
      ] }, isLoading: false, isError: false, refetch: vi.fn() }
    return { data: { items: [] }, isLoading: false, isError: false, refetch: vi.fn() }
  },
  useQueries: ({ queries }: { queries: Array<{ queryKey: string[] }> }) => queries.map((query) => ({
    data: { items: siteModels.value[query.queryKey.at(-1) as string] ?? [] },
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  })),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: (options: { mutationFn: () => Promise<unknown> }) => {
    mutation.options = options
    return { mutate: vi.fn(), isPending: false }
  },
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  updateAgentScopeSettings: vi.fn(),
}))
vi.mock('@/features/api-keys/lib/use-site-model-scope', () => ({
  useSiteModelScope: () => ({
    siteModelRows: [], allSiteModelIds: ['model-1'], selectedSiteModelIds: selectedScope.selectedSiteModelIds,
    availableSiteModelIds: new Set(['model-1']), siteModelsLoading: false,
  }),
}))
vi.mock('@/features/api-keys/components/site-model-scope-picker', () => ({ SiteModelScopePicker: () => null }))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    ns: ['agent', 'common'],
    resources: { zh: { agent: zhAgent, common: zhCommon } },
  })
})
beforeEach(() => {
  confirmOpen.value = false
  siteModels.failed = false
  selectedScope.selectedSiteModelIds = ['model-1']
  runtime.value = { allowed_site_ids: ['site-1'], allowed_site_model_ids: ['model-1'], site_policy: 'allow_list', model_policy: 'allow_list', image_model: { site_id: 'site-1', model: 'image-key' } }
  siteModels.value = {
    'site-1': [{ id: 'sm-1', upstream_model_name: 'gpt-image-1', display_name: 'GPT Image', status: 'active', capabilities: { output_modalities: ['image'] } }],
    'site-2': [
      { id: 'sm-2', upstream_model_name: 'flux', display_name: 'FLUX', status: 'active', capabilities: { supported_endpoint_types: ['openai-image'] } },
      { id: 'sm-3', upstream_model_name: 'gpt-4o', display_name: 'GPT-4o', status: 'active', capabilities: { supported_endpoint_types: ['openai'] } },
    ],
    'site-3': [{ id: 'sm-4', upstream_model_name: 'dall-e-3', display_name: 'DALL-E 3', status: 'active', capabilities: { output_modalities: ['image'] } }],
  }
})

function render() {
  return renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentScopeConfigDialog open onOpenChange={() => {}} /></I18nextProvider>)
}

describe('AgentScopeConfigDialog 保存', () => {
  it('请求体只保留范围配置且不包含任何生图字段', async () => {
    render()
    await mutation.options?.mutationFn()
    expect(updateAgentScopeSettings).toHaveBeenCalledWith({
      site_policy: 'allow_list', model_policy: 'allow_list', allowed_site_ids: ['site-1'],
      allowed_site_model_ids: ['model-1'],
    })
    expect(updateAgentScopeSettings).not.toHaveBeenCalledWith(expect.objectContaining({ image_model: expect.anything() }))
    expect(updateAgentScopeSettings).not.toHaveBeenCalledWith(expect.objectContaining({ image_models: expect.anything() }))
  })
})

describe('AgentScopeConfigDialog 放弃更改确认', () => {
  it('放弃更改的确认弹窗叠在配置弹窗之上', () => {
    confirmOpen.value = true
    const html = render()
    expect(html).toContain(zhAgent.settings.scope.discardTitle)
    const layers = [...html.matchAll(/class="([^"]*z-\[(\d+)\][^"]*)"/g)].map((match) => ({ className: match[1], z: Number(match[2]) }))
    expect(layers.length).toBe(4)
    // 配置弹窗（overlay + content）为 z-[60]，确认弹窗（overlay + content）必须更高
    expect(layers.map((layer) => layer.z)).toEqual([60, 60, 70, 70])
  })
})
