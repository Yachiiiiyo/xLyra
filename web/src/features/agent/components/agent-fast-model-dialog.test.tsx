import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentFastModelDialog } from './agent-fast-model-dialog'
import { updateAgentFastModel } from '@/features/agent/api/agent'
import type { AgentFastModelCandidate } from '@/features/agent/lib/agent-fast-model'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

const candidates: AgentFastModelCandidate[] = [
  { id: 'gpt-fast', name: 'GPT Fast', siteNames: ['Alpha', 'Beta'] },
  { id: 'claude-fast', name: 'Claude Fast', siteNames: ['Beta'] },
]
const mutation = vi.hoisted(() => ({ options: undefined as undefined | { mutationFn: () => Promise<unknown>; onSuccess?: () => Promise<void> | void } }))
const onOpenChange = vi.hoisted(() => vi.fn())
const invalidateQueries = vi.hoisted(() => vi.fn())

vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children, open }: { children?: React.ReactNode; open?: boolean }) => open === false ? null : React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@tanstack/react-query', () => ({
  useQueryClient: () => ({ invalidateQueries }),
  useMutation: (options: { mutationFn: () => Promise<unknown>; onSuccess?: () => Promise<void> | void }) => {
    mutation.options = options
    return { mutate: vi.fn(() => { void options.mutationFn() }), isPending: false }
  },
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  updateAgentFastModel: vi.fn(),
}))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()
beforeAll(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  await i18n.init({ lng: 'zh', defaultNS: 'agent', ns: ['agent', 'common'], resources: { zh: { agent: zhAgent, common: zhCommon } } })
})
beforeEach(() => {
  mutation.options = undefined
  onOpenChange.mockReset()
  invalidateQueries.mockReset().mockResolvedValue(undefined)
  vi.mocked(updateAgentFastModel).mockReset().mockResolvedValue(undefined)
})

async function mountDialog(current = '', items = candidates) {
  return mountMini(
    <I18nextProvider i18n={i18n}>
      <AgentFastModelDialog open onOpenChange={onOpenChange} candidates={items} current={current} loading={false} />
    </I18nextProvider>,
  )
}

function radios(container: MiniNode) {
  return findMiniNodes(container, (node) => node.getAttribute('role') === 'radio')
}

function findButton(container: MiniNode, label: string) {
  return findMiniNodes(container, (node) => node.tagName === 'BUTTON' && node.textContent.trim() === label)[0] ?? null
}

function disabled(node: MiniNode | null) {
  return Boolean(node?.hasAttribute('disabled') || (node as (MiniNode & { disabled?: boolean }) | null)?.disabled)
}

async function click(node: MiniNode | null) {
  expect(node).not.toBeNull()
  await act(async () => { node!.dispatchEvent(miniEvent('click')) })
}

describe('AgentFastModelDialog', () => {
  it('未改动时保存禁用，选择后可保存，改回原值后再次禁用', async () => {
    const mounted = await mountDialog()
    try {
      expect(disabled(findButton(mounted.container, zhAgent.settings.save))).toBe(true)
      expect(radios(mounted.container)[0]?.getAttribute('aria-checked')).toBe('true')
      await click(radios(mounted.container)[1])
      expect(radios(mounted.container)[1]?.getAttribute('aria-checked')).toBe('true')
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.unsavedChanges)
      expect(disabled(findButton(mounted.container, zhAgent.settings.save))).toBe(false)
      await click(radios(mounted.container)[0])
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.unsavedChanges)
      expect(disabled(findButton(mounted.container, zhAgent.settings.save))).toBe(true)
    } finally { await mounted.unmount() }
  })

  it('保存选中的模型并在成功后失效设置查询、关闭弹窗', async () => {
    const mounted = await mountDialog()
    try {
      await click(radios(mounted.container)[1])
      await click(findButton(mounted.container, zhAgent.settings.save))
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('gpt-fast')
      await act(async () => { await mutation.options?.onSuccess?.() })
      expect(invalidateQueries).toHaveBeenCalledOnce()
      expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
    } finally { await mounted.unmount() }
  })

  it('选择「不指定」会清除已有配置', async () => {
    const mounted = await mountDialog('gpt-fast')
    try {
      expect(radios(mounted.container)[1]?.getAttribute('aria-checked')).toBe('true')
      await click(radios(mounted.container)[0])
      await click(findButton(mounted.container, zhAgent.settings.save))
      expect(updateAgentFastModel).toHaveBeenCalledExactlyOnceWith('')
    } finally { await mounted.unmount() }
  })

  it('没有聊天模型候选时保留「不指定」并显示空态', async () => {
    const mounted = await mountDialog('', [])
    try {
      expect(radios(mounted.container)).toHaveLength(1)
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.fastEmpty)
    } finally { await mounted.unmount() }
  })

  it('关闭有改动的弹窗时要求确认丢弃', async () => {
    const mounted = await mountDialog()
    try {
      await click(radios(mounted.container)[1])
      await click(findButton(mounted.container, zhAgent.settings.cancel))
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.discardTitle)
      expect(onOpenChange).not.toHaveBeenCalled()
      await click(findButton(mounted.container, zhAgent.settings.scope.discardConfirm))
      expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
      expect(updateAgentFastModel).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })
})
