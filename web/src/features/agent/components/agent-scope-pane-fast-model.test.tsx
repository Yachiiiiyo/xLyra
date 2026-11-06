import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentScopePane } from './agent-scope-pane'
import { updateAgentClassifierModel, updateAgentFastModel } from '@/features/agent/api/agent'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

type QueryStatus = { isLoading: boolean; isFetching: boolean; isSuccess: boolean; isError: boolean }
const ready = (): QueryStatus => ({ isLoading: false, isFetching: false, isSuccess: true, isError: false })
const queryState = vi.hoisted(() => ({
  classifierModel: '',
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
vi.mock('@/components/ui/select', async () => {
  const React = await import('react')
  const context = React.createContext({ value: '', disabled: false, onValueChange: (value: string) => { void value } })
  return {
    Select: ({ children, value, disabled = false, onValueChange }: { children?: React.ReactNode; value: string; disabled?: boolean; onValueChange: (value: string) => void }) => (
      React.createElement(context.Provider, { value: { value, disabled, onValueChange } }, children)
    ),
    SelectTrigger: ({ children, ...props }: React.ComponentProps<'button'>) => {
      const select = React.useContext(context)
      return React.createElement('button', { ...props, type: 'button', role: 'combobox', disabled: select.disabled, 'data-value': select.value }, children)
    },
    SelectValue: ({ placeholder }: { placeholder?: string }) => {
      const select = React.useContext(context)
      return React.createElement('span', null, select.value === '__current_conversation_model__' ? placeholder : select.value)
    },
    SelectContent: ({ children }: { children?: React.ReactNode }) => React.createElement('div', null, children),
    SelectItem: ({ children, value }: { children?: React.ReactNode; value: string }) => {
      const select = React.useContext(context)
      return React.createElement('button', { type: 'button', role: 'option', 'data-value': value, disabled: select.disabled, onClick: () => select.onValueChange(value) }, children)
    },
  }
})
vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    const key = queryKey.at(-1)
    if (key === 'runtime') return {
      data: { allowed_site_ids: [], allowed_site_model_ids: [], site_policy: 'allow_all', model_policy: 'allow_all', image_models: [], classifier_model: queryState.classifierModel, fast_model: queryState.fastModel, prompt_suggestion_enabled: true },
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
  useMutation: (options: {
    mutationFn: (value: unknown) => Promise<unknown>
    onSuccess?: (data: unknown, value: unknown) => unknown
    onError?: (error: Error, value: unknown) => unknown
  }) => ({
    isPending: false,
    mutate: (value: unknown) => {
      void Promise.resolve()
        .then(() => options.mutationFn(value))
        .then((data) => options.onSuccess?.(data, value))
        .catch((error: Error) => options.onError?.(error, value))
    },
  }),
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  updateAgentClassifierModel: vi.fn(),
  updateAgentFastModel: vi.fn(),
}))
vi.mock('@/features/agent/components/agent-scope-config-dialog', () => ({ AgentScopeConfigDialog: () => null }))
vi.mock('@/features/agent/components/agent-image-model-dialog', () => ({ AgentImageModelDialog: () => null }))
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
  queryState.classifierModel = ''
  queryState.fastModel = ''
  queryState.statuses = { runtime: ready(), sites: ready(), canonical: ready(), models: ready() }
  invalidateQueries.mockReset().mockResolvedValue(undefined)
  vi.mocked(updateAgentClassifierModel).mockReset().mockResolvedValue(undefined)
  vi.mocked(updateAgentFastModel).mockReset().mockResolvedValue(undefined)
})

function view() {
  return <I18nextProvider i18n={i18n}><AgentScopePane /></I18nextProvider>
}

async function flush() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

function settingSection(container: MiniNode, title: string) {
  const section = findMiniNodes(container, (node) => node.tagName === 'SECTION')
    .find((node) => findMiniNodes(node, (child) => child.tagName === 'P' && child.textContent === title).length > 0)
  if (!section) throw new Error(`找不到设置区域: ${title}`)
  return section
}

async function choose(section: MiniNode, label: string) {
  const option = findMiniNodes(section, (node) => node.getAttribute('role') === 'option' && node.textContent === label)[0]
  if (!option) throw new Error(`找不到下拉选项: ${label}`)
  await act(async () => { option.dispatchEvent(miniEvent('click')) })
  await flush()
}

describe('AgentScopePane 审批模型与快速模型', () => {
  it('候选加载中显示 Skeleton，不闪烁未配置提示，也不清理配置', async () => {
    queryState.classifierModel = 'removed-classifier'
    queryState.fastModel = 'removed-fast'
    queryState.statuses.models = { isLoading: true, isFetching: true, isSuccess: false, isError: false }
    const mounted = await mountMini(view())
    try {
      expect(findMiniNodes(mounted.container, (node) => node.getAttribute('class')?.includes('skeleton-shimmer'))).not.toHaveLength(0)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.classifierModelUnsetHint)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('未配置时分别提示回退当前对话模型', async () => {
    const mounted = await mountMini(view())
    try {
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.classifierModelNotConfigured)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.classifierModelUnsetHint)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelNotConfigured)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('有效配置显示各自用途，不触发清理', async () => {
    queryState.classifierModel = 'gpt-fast'
    queryState.fastModel = 'gpt-fast'
    const mounted = await mountMini(view())
    try {
      expect(mounted.container.textContent).toContain('GPT Fast')
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.classifierModelDescription)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelConfigured)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.classifierModelUnsetHint)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('选择审批模型只调用审批模型更新接口并失效运行时设置', async () => {
    const mounted = await mountMini(view())
    try {
      await choose(settingSection(mounted.container, zhAgent.settings.scope.classifierModelTitle), 'GPT Fast')
      expect(updateAgentClassifierModel).toHaveBeenCalledExactlyOnceWith('gpt-fast')
      expect(updateAgentFastModel).not.toHaveBeenCalled()
      expect(invalidateQueries).toHaveBeenCalledExactlyOnceWith({ queryKey: ['settings', 'agent', 'runtime'] })
    } finally { await mounted.unmount() }
  })

  it('选择快速模型只调用快速模型更新接口', async () => {
    const mounted = await mountMini(view())
    try {
      await choose(settingSection(mounted.container, zhAgent.settings.scope.fastModelTitle), 'GPT Fast')
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('gpt-fast')
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('两项选择不指定时都向对应接口传空串', async () => {
    queryState.classifierModel = 'gpt-fast'
    queryState.fastModel = 'gpt-fast'
    const mounted = await mountMini(view())
    try {
      await choose(settingSection(mounted.container, zhAgent.settings.scope.classifierModelTitle), zhAgent.settings.scope.modelFollowCurrent)
      expect(updateAgentClassifierModel).toHaveBeenCalledExactlyOnceWith('')
      expect(updateAgentFastModel).not.toHaveBeenCalled()
      await choose(settingSection(mounted.container, zhAgent.settings.scope.fastModelTitle), zhAgent.settings.scope.modelFollowCurrent)
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('')
      expect(updateAgentClassifierModel).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })

  it('快速模型失效时只静默清理快速模型一次', async () => {
    queryState.classifierModel = 'gpt-fast'
    queryState.fastModel = 'removed-fast'
    const mounted = await mountMini(view())
    try {
      await flush()
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.classifierModelUnsetHint)
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('')
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(invalidateQueries).toHaveBeenCalledOnce()
      await mounted.render(view())
      await flush()
      expect(updateAgentFastModel).toHaveBeenCalledOnce()
      expect(invalidateQueries).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })

  it('审批模型失效时只静默清理审批模型一次', async () => {
    queryState.classifierModel = 'removed-classifier'
    queryState.fastModel = 'gpt-fast'
    const mounted = await mountMini(view())
    try {
      await flush()
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.classifierModelUnsetHint)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.fastModelUnsetHint)
      expect(updateAgentClassifierModel).toHaveBeenCalledExactlyOnceWith('')
      expect(updateAgentFastModel).not.toHaveBeenCalled()
      expect(invalidateQueries).toHaveBeenCalledOnce()
      await mounted.render(view())
      await flush()
      expect(updateAgentClassifierModel).toHaveBeenCalledOnce()
      expect(invalidateQueries).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })

  it('查询失败或仍在 fetching 时两项都不清理', async () => {
    queryState.classifierModel = 'removed-classifier'
    queryState.fastModel = 'removed-fast'
    queryState.statuses.sites = { isLoading: false, isFetching: false, isSuccess: false, isError: true }
    const failed = await mountMini(view())
    try {
      await flush()
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await failed.unmount() }

    queryState.statuses.sites = { ...ready(), isFetching: true }
    const fetching = await mountMini(view())
    try {
      await flush()
      expect(updateAgentClassifierModel).not.toHaveBeenCalled()
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await fetching.unmount() }
  })
})
