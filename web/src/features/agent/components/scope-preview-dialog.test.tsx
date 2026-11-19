import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { ScopePreviewDialog } from './agent-scope-pane'
import type { AgentScopePreviewGroup } from '@/features/agent/lib/agent-scope-preview'
import type { CanonicalModelItem } from '@/features/sites/api/sites'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'
import zhComponents from '@/locales/zh/components.json'

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
    SelectTrigger: ({
      children,
      variant: _variant,
      filterLabel,
      active = false,
      ...props
    }: React.ComponentProps<'button'> & { variant?: 'default' | 'filter'; filterLabel?: React.ReactNode; active?: boolean }) => {
      void _variant
      const select = React.useContext(context)
      return React.createElement(
        'button',
        { ...props, type: 'button', role: 'combobox', disabled: select.disabled, 'data-value': select.value },
        active ? children : filterLabel ?? children,
      )
    },
    SelectValue: () => {
      const select = React.useContext(context)
      return React.createElement('span', null, select.value)
    },
    SelectContent: ({ children }: { children?: React.ReactNode }) => React.createElement('div', null, children),
    SelectItem: ({ children, value }: { children?: React.ReactNode; value: string }) => {
      const select = React.useContext(context)
      return React.createElement('button', { type: 'button', role: 'option', 'data-value': value, disabled: select.disabled, onClick: () => select.onValueChange(value) }, children)
    },
  }
})

vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))

const i18n = createInstance()
beforeAll(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    ns: ['agent', 'common', 'components'],
    resources: { zh: { agent: zhAgent, common: zhCommon, components: zhComponents } },
  })
})

function canonicalItem(partial: Pick<CanonicalModelItem, 'id' | 'model_key' | 'display_name' | 'provider'>): CanonicalModelItem {
  return {
    ...partial,
    category: 'chat',
    capabilities: {},
    status: 'active',
    created_at: '',
    updated_at: '',
    site_model_count: 1,
    site_count: 1,
    aliases: [],
  }
}

const sampleCanonicalById = new Map<string, CanonicalModelItem>([
  ['can-ds', canonicalItem({ id: 'can-ds', model_key: 'deepseek-v4.1-flash', display_name: 'DeepSeek V4.1 Flash', provider: 'deepseek' })],
])

const sampleGroups: AgentScopePreviewGroup[] = [
  {
    site: {
      site_id: 'site-helion',
      site_name: 'Helion-国模',
      site_type: 'openai',
      enabled: true,
      models: [],
    },
    models: [
      { id: 'h-kimi', upstream_model_name: 'kimi-k3.1-pro-upstream', display_name: 'Kimi', canonical_model_id: '', model_key: 'kimi-k3.1-pro' },
      // display_name 与 upstream_model_name 完全相同，验证仍始终渲染两行
      { id: 'h-ds', upstream_model_name: 'deepseek-v4.1-flash', display_name: 'deepseek-v4.1-flash', canonical_model_id: '', model_key: 'deepseek-v4.1-flash' },
    ],
  },
  {
    site: {
      site_id: 'site-ewo',
      site_name: 'ewo-xlyra',
      site_type: 'openai',
      enabled: true,
      models: [],
    },
    models: [
      { id: 'e-glm', upstream_model_name: 'glm-5.3', display_name: 'GLM', canonical_model_id: '', model_key: 'glm-5.3' },
      { id: 'e-kimi-1', upstream_model_name: 'kimi-k3.1-pro', display_name: 'Kimi', canonical_model_id: '', model_key: 'kimi-k3.1-pro' },
      // canonical_model_id 提供标准展示名
      { id: 'e-ds', upstream_model_name: 'deepseek-v4.1-flash-001', display_name: 'deepseek-v4.1-flash-001', canonical_model_id: 'can-ds', model_key: 'deepseek-v4.1-flash' },
    ],
  },
]

function rowButtons(container: MiniNode): MiniNode[] {
  return findMiniNodes(
    container,
    (node) => node.tagName === 'BUTTON' && node.getAttribute('role') !== 'combobox' && node.getAttribute('role') !== 'option' && node.textContent !== zhAgent.settings.close,
  )
}

function rowInfo(row: MiniNode) {
  const display = findMiniNodes(row, (node) => Boolean(node.getAttribute('class')?.includes('text-sm')))[0]?.textContent
  const real = findMiniNodes(row, (node) => Boolean(node.getAttribute('class')?.includes('text-muted-soft')))[0]?.textContent
  const site = findMiniNodes(row, (node) => Boolean(node.getAttribute('class')?.includes('text-faint')))[0]?.textContent
  return { display, real, site }
}

function searchInput(container: MiniNode): MiniNode {
  const input = findMiniNodes(container, (node) => node.tagName === 'INPUT')[0]
  if (!input) throw new Error('找不到搜索输入框')
  return input
}

async function flush() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

async function typeInto(node: MiniNode, value: string) {
  const inputNode = node as unknown as { type?: string }
  inputNode.type ??= 'text'
  await act(async () => { node.dispatchEvent(miniEvent('focusin', { target: node })) })
  const prototypeSetter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set
  prototypeSetter?.call(node, value)
  await act(async () => {
    node.dispatchEvent(miniEvent('input', { target: node }))
    node.dispatchEvent(miniEvent('change', { target: node }))
  })
  await flush()
}

async function chooseFilter(container: MiniNode, label: string) {
  const option = findMiniNodes(container, (node) => node.getAttribute('role') === 'option' && node.textContent === label)[0]
  if (!option) throw new Error(`找不到筛选选项: ${label}`)
  await act(async () => { option.dispatchEvent(miniEvent('click')) })
  await flush()
}

async function clickClose(container: MiniNode) {
  const closeBtn = findMiniNodes(container, (node) => node.tagName === 'BUTTON' && node.textContent === zhAgent.settings.close)[0]
  if (!closeBtn) throw new Error('找不到关闭按钮')
  await act(async () => { closeBtn.dispatchEvent(miniEvent('click')) })
  await flush()
}

describe('ScopePreviewDialog', () => {
  it('左侧始终渲染两行模型名（展示名 + 上游真实名），右侧为站点名，按站点名与模型名二级排序', async () => {
    const mounted = await mountMini(
      <I18nextProvider i18n={i18n}>
        <ScopePreviewDialog open onOpenChange={() => {}} groups={sampleGroups} loading={false} canonicalById={sampleCanonicalById} />
      </I18nextProvider>,
    )
    try {
      expect(mounted.container.textContent).not.toContain('个模型')
      const rows = rowButtons(mounted.container)
      expect(rows.map(rowInfo)).toEqual([
        { display: 'DeepSeek V4.1 Flash', real: 'deepseek-v4.1-flash-001', site: 'ewo-xlyra' },
        { display: 'GLM', real: 'glm-5.3', site: 'ewo-xlyra' },
        { display: 'Kimi', real: 'kimi-k3.1-pro', site: 'ewo-xlyra' },
        { display: 'deepseek-v4.1-flash', real: 'deepseek-v4.1-flash', site: 'Helion-国模' },
        { display: 'Kimi', real: 'kimi-k3.1-pro-upstream', site: 'Helion-国模' },
      ])
    } finally {
      await mounted.unmount()
    }
  })

  it('模型下拉筛选可按模型名过滤并可切回全部模型', async () => {
    const mounted = await mountMini(
      <I18nextProvider i18n={i18n}>
        <ScopePreviewDialog open onOpenChange={() => {}} groups={sampleGroups} loading={false} canonicalById={sampleCanonicalById} />
      </I18nextProvider>,
    )
    try {
      const combobox = findMiniNodes(mounted.container, (node) => node.getAttribute('role') === 'combobox')[0]
      expect(combobox?.textContent).toBe(zhAgent.settings.scope.allModels)

      await chooseFilter(mounted.container, 'kimi-k3.1-pro')
      expect(combobox?.textContent).toBe('kimi-k3.1-pro')
      expect(rowButtons(mounted.container).map(rowInfo)).toEqual([
        { display: 'Kimi', real: 'kimi-k3.1-pro', site: 'ewo-xlyra' },
        { display: 'Kimi', real: 'kimi-k3.1-pro-upstream', site: 'Helion-国模' },
      ])

      await chooseFilter(mounted.container, zhAgent.settings.scope.allModels)
      expect(combobox?.textContent).toBe(zhAgent.settings.scope.allModels)
      expect(rowButtons(mounted.container)).toHaveLength(5)
    } finally {
      await mounted.unmount()
    }
  })

  it('文本搜索支持展示名、上游真实名并与模型筛选取交集，无匹配时展示空态提示', async () => {
    const mounted = await mountMini(
      <I18nextProvider i18n={i18n}>
        <ScopePreviewDialog open onOpenChange={() => {}} groups={sampleGroups} loading={false} canonicalById={sampleCanonicalById} />
      </I18nextProvider>,
    )
    try {
      await typeInto(searchInput(mounted.container), 'V4.1 Flash')
      expect(rowButtons(mounted.container).map(rowInfo)).toEqual([
        { display: 'DeepSeek V4.1 Flash', real: 'deepseek-v4.1-flash-001', site: 'ewo-xlyra' },
      ])

      await typeInto(searchInput(mounted.container), '')
      await chooseFilter(mounted.container, 'kimi-k3.1-pro')
      await typeInto(searchInput(mounted.container), 'pro-upstream')
      expect(rowButtons(mounted.container).map(rowInfo)).toEqual([
        { display: 'Kimi', real: 'kimi-k3.1-pro-upstream', site: 'Helion-国模' },
      ])

      await typeInto(searchInput(mounted.container), 'nonexistent')
      expect(rowButtons(mounted.container)).toHaveLength(0)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.noModels)
    } finally {
      await mounted.unmount()
    }
  })

  it('关闭弹窗后重置搜索框与模型筛选器', async () => {
    let setOpenRef: ((open: boolean) => void) | null = null
    function Harness() {
      const [open, setOpen] = React.useState(true)
      setOpenRef = setOpen
      return (
        <I18nextProvider i18n={i18n}>
          <ScopePreviewDialog open={open} onOpenChange={setOpen} groups={sampleGroups} loading={false} canonicalById={sampleCanonicalById} />
        </I18nextProvider>
      )
    }
    const mounted = await mountMini(<Harness />)
    try {
      await chooseFilter(mounted.container, 'kimi-k3.1-pro')
      expect(rowButtons(mounted.container)).toHaveLength(2)
      await typeInto(searchInput(mounted.container), 'ewo')
      expect(rowButtons(mounted.container).map(rowInfo)).toEqual([
        { display: 'Kimi', real: 'kimi-k3.1-pro', site: 'ewo-xlyra' },
      ])

      await clickClose(mounted.container)
      expect(rowButtons(mounted.container)).toHaveLength(0)

      await act(async () => { setOpenRef?.(true) })
      await flush()
      expect(searchInput(mounted.container).value).toBe('')
      expect(findMiniNodes(mounted.container, (node) => node.getAttribute('role') === 'combobox')[0]?.textContent).toBe(zhAgent.settings.scope.allModels)
      expect(rowButtons(mounted.container)).toHaveLength(5)
    } finally {
      await mounted.unmount()
    }
  })
})
