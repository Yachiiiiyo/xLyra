import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentScopePane } from './agent-scope-pane'
import { updateAgentFastModel } from '@/features/agent/api/agent'
import { findMiniNodes, mountMini } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

type QueryStatus = { isLoading: boolean; isFetching: boolean; isSuccess: boolean; isError: boolean }
const ready = (): QueryStatus => ({ isLoading: false, isFetching: false, isSuccess: true, isError: false })
const queryState = vi.hoisted(() => ({
  fastModel: '',
  statuses: {
    runtime: { isLoading: false, isFetching: false, isSuccess: true, isError: false },
    sites: { isLoading: false, isFetching: false, isSuccess: true, isError: false },
    canonical: { isLoading: false, isFetching: false, isSuccess: true, isError: false },
    models: { isLoading: false, isFetching: false, isSuccess: true, isError: false },
  },
}))
const invalidateQueries = vi.hoisted(() => vi.fn())

vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children, open }: { children?: React.ReactNode; open?: boolean }) => open === false ? null : React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    const key = queryKey.at(-1)
    if (key === 'runtime') return {
      data: { allowed_site_ids: [], allowed_site_model_ids: [], site_policy: 'allow_all', model_policy: 'allow_all', image_models: [], fast_model: queryState.fastModel, prompt_suggestion_enabled: true },
      ...queryState.statuses.runtime,
    }
    if (key === 'sites') return {
      data: { items: [{ id: 'site-1', name: 'Alpha', site_type: 'openai', enabled: true }] },
      ...queryState.statuses.sites,
    }
    if (queryKey.at(-2) === 'models' && key === 'all') return {
      data: { 'site-1': [{ id: 'model-1', site_id: 'site-1', upstream_model_name: 'gpt-upstream', display_name: 'GPT Fast', canonical_model_id: 'canonical-fast', capabilities: {}, status: 'active' }] },
      ...queryState.statuses.models,
    }
    return {
      data: { items: [{ id: 'canonical-fast', model_key: 'gpt-fast', display_name: 'GPT Fast', category: 'text' }] },
      ...queryState.statuses.canonical,
    }
  },
  useQueryClient: () => ({ invalidateQueries }),
  useMutation: () => ({ mutate: vi.fn(), isPending: false }),
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  updateAgentFastModel: vi.fn(),
}))
vi.mock('@/features/agent/components/agent-scope-config-dialog', () => ({ AgentScopeConfigDialog: () => null }))
vi.mock('@/features/agent/components/agent-image-model-dialog', () => ({ AgentImageModelDialog: () => null }))
vi.mock('@/features/agent/components/agent-fast-model-dialog', () => ({ AgentFastModelDialog: () => null }))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))
vi.mock('@/components/ui/switch', () => ({ Switch: () => React.createElement('button', { type: 'button', role: 'switch' }) }))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()
beforeAll(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  await i18n.init({ lng: 'zh', defaultNS: 'agent', ns: ['agent', 'common'], resources: { zh: { agent: zhAgent, common: zhCommon } } })
})
beforeEach(() => {
  queryState.fastModel = ''
  queryState.statuses = { runtime: ready(), sites: ready(), canonical: ready(), models: ready() }
  invalidateQueries.mockReset().mockResolvedValue(undefined)
  vi.mocked(updateAgentFastModel).mockReset().mockResolvedValue(undefined)
})

function view() {
  return <I18nextProvider i18n={i18n}><AgentScopePane /></I18nextProvider>
}

async function flush() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

describe('AgentScopePane 快速模型', () => {
  it('候选加载中显示 Skeleton，不闪烁未配置提示，也不清理配置', async () => {
    queryState.fastModel = 'removed'
    queryState.statuses.models = { isLoading: true, isFetching: true, isSuccess: false, isError: false }
    const mounted = await mountMini(view())
    try {
      expect(findMiniNodes(mounted.container, (node) => node.getAttribute('class')?.includes('skeleton-shimmer'))).not.toHaveLength(0)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('未配置时明确提示回退当前对话模型', async () => {
    const mounted = await mountMini(view())
    try {
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelNotConfigured)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('有效配置显示模型名与用途，不触发清理', async () => {
    queryState.fastModel = 'gpt-fast'
    const mounted = await mountMini(view())
    try {
      expect(mounted.container.textContent).toContain('GPT Fast')
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelConfigured)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('失效配置按未配置展示，只静默清理一次并失效设置查询', async () => {
    queryState.fastModel = 'removed'
    const mounted = await mountMini(view())
    try {
      await flush()
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelNotConfigured)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('')
      expect(invalidateQueries).toHaveBeenCalledOnce()
      await mounted.render(view())
      await flush()
      expect(updateAgentFastModel).toHaveBeenCalledOnce()
      expect(invalidateQueries).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })

  it('查询失败或仍在 fetching 时不清理', async () => {
    queryState.fastModel = 'removed'
    queryState.statuses.sites = { isLoading: false, isFetching: false, isSuccess: false, isError: true }
    const failed = await mountMini(view())
    try {
      await flush()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await failed.unmount() }

    queryState.statuses.sites = { ...ready(), isFetching: true }
    const fetching = await mountMini(view())
    try {
      await flush()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await fetching.unmount() }
  })
})
