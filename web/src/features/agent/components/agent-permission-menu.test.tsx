import { act, useState } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentPermissionMenu } from '@/features/agent/components/agent-permission-menu'
import { loadPermissionMode, PERMISSION_MODE_KEY, type PermissionMode } from '@/features/agent/lib/agent-permission-mode'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'

vi.mock('@radix-ui/react-popover', async () => {
  const React = await import('react')
  const pass = ({ children }: { children?: React.ReactNode }) => React.createElement(React.Fragment, null, children)
  return {
    Root: pass,
    Trigger: ({ children }: { children?: React.ReactNode }) => children,
    Portal: pass,
    Content: ({ children }: { children?: React.ReactNode }) => React.createElement('div', null, children),
  }
})

vi.mock('@/components/ui/dialog', async () => {
  const React = await import('react')
  const pass = (tag: string) => ({ children }: { children?: React.ReactNode }) => React.createElement(tag, null, children)
  return {
    Dialog: ({ open, children }: { open?: boolean; children?: React.ReactNode }) => open ? React.createElement(React.Fragment, null, children) : null,
    DialogContent: pass('div'),
    DialogHeader: pass('div'),
    DialogBody: pass('div'),
    DialogTitle: pass('h2'),
    DialogDescription: pass('p'),
    DialogFooter: ({ cancel, confirm }: { cancel?: React.ReactNode; confirm?: React.ReactNode }) => React.createElement('div', null, cancel, confirm),
  }
})

const i18n = createInstance()
const storageKey = PERMISSION_MODE_KEY
let values: Map<string, string>
let getItem: ReturnType<typeof vi.fn>
let setItem: ReturnType<typeof vi.fn>

beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

beforeEach(() => {
  values = new Map()
  getItem = vi.fn((key: string) => values.get(key) ?? null)
  setItem = vi.fn((key: string, value: string) => { values.set(key, value) })
  vi.stubGlobal('localStorage', { getItem, setItem })
  vi.stubGlobal('window', { localStorage: { getItem, setItem } })
})

afterEach(() => { vi.unstubAllGlobals() })

function PermissionHarness({ initial, classifierModel = '', runtimeSettingsLoaded = true }: { initial: PermissionMode; classifierModel?: string; runtimeSettingsLoaded?: boolean }) {
  const [mode, setMode] = useState(initial)
  return (
    <>
      <AgentPermissionMenu value={mode} onChange={setMode} classifierModel={classifierModel} runtimeSettingsLoaded={runtimeSettingsLoaded} />
      <output data-current-mode={mode}>{mode}</output>
    </>
  )
}

function renderMenu(value: PermissionMode = 'ask', classifierModel = '', runtimeSettingsLoaded = true) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <AgentPermissionMenu value={value} onChange={() => {}} classifierModel={classifierModel} runtimeSettingsLoaded={runtimeSettingsLoaded} />
    </I18nextProvider>,
  )
}

function currentMode(container: MiniNode) {
  return findMiniNodes(container, (node) => node.tagName === 'OUTPUT')[0]?.textContent
}

function option(container: MiniNode, hint: string) {
  const node = findMiniNodes(container, (candidate) => candidate.tagName === 'BUTTON' && (candidate.textContent?.includes(hint) ?? false))[0]
  if (!node) throw new Error(`找不到权限选项: ${hint}`)
  return node
}

async function click(node: MiniNode) {
  await act(async () => { node.dispatchEvent(miniEvent('click')) })
}

describe('AgentPermissionMenu', () => {
  it('按询问、替我审批、完全访问排列,显示当前项与对应提示', () => {
    const html = renderMenu('auto')
    const orderedHtml = renderMenu('ask')
    expect(orderedHtml.indexOf(zhAgent.composer.permissionAsk)).toBeLessThan(orderedHtml.indexOf(zhAgent.composer.permissionAuto))
    expect(orderedHtml.indexOf(zhAgent.composer.permissionAuto)).toBeLessThan(orderedHtml.indexOf(zhAgent.composer.permissionFull))
    expect(html).toContain(zhAgent.composer.permissionAskHint)
    expect(html).toContain(zhAgent.composer.permissionAutoHint)
    expect(html).toContain(zhAgent.composer.permissionFullHint)
    expect(html).toContain(zhAgent.composer.permissionAutoUnsetHint)
    expect(html).toContain('aria-label="权限模式"')
    expect(html).toContain(`title="${zhAgent.composer.permissionAutoHint}"`)
    expect(html).toContain('aria-checked="true"')
    expect(html).toContain('hidden sm:inline')
    expect(html).toContain('lucide-shield-ellipsis')
  })

  it('仅在运行时设置已加载且审批模型为空时显示未配置提示', () => {
    expect(renderMenu('ask', '', false)).not.toContain(zhAgent.composer.permissionAutoUnsetHint)
    expect(renderMenu('ask', 'classifier-model', true)).not.toContain(zhAgent.composer.permissionAutoUnsetHint)
    expect(renderMenu('ask', '', true)).toContain(zhAgent.composer.permissionAutoUnsetHint)
  })

  it('三档可直接切换并写入 localStorage,完全访问确认后才生效', async () => {
    const mounted = await mountMini(<I18nextProvider i18n={i18n}><PermissionHarness initial="ask" /></I18nextProvider>)
    try {
      await click(option(mounted.container, zhAgent.composer.permissionAutoHint))
      expect(currentMode(mounted.container)).toBe('auto')
      expect(setItem).toHaveBeenLastCalledWith(storageKey, 'auto')

      await click(option(mounted.container, zhAgent.composer.permissionAskHint))
      expect(currentMode(mounted.container)).toBe('ask')
      expect(setItem).toHaveBeenLastCalledWith(storageKey, 'ask')

      await click(option(mounted.container, zhAgent.composer.permissionFullHint))
      expect(currentMode(mounted.container)).toBe('ask')
      expect(mounted.container.textContent).toContain(zhAgent.composer.fullConfirmTitle)
      expect(setItem).not.toHaveBeenCalledWith(storageKey, 'full')

      const confirm = findMiniNodes(mounted.container, (node) => node.tagName === 'BUTTON' && node.textContent === zhAgent.composer.fullConfirmAccept)[0]
      if (!confirm) throw new Error('找不到完全访问确认按钮')
      await click(confirm)
      expect(currentMode(mounted.container)).toBe('full')
      expect(setItem).toHaveBeenLastCalledWith(storageKey, 'full')

      await click(option(mounted.container, zhAgent.composer.permissionAutoHint))
      expect(currentMode(mounted.container)).toBe('auto')
      expect(mounted.container.textContent).not.toContain(zhAgent.composer.fullConfirmTitle)
      expect(setItem).toHaveBeenLastCalledWith(storageKey, 'auto')
    } finally { await mounted.unmount() }
  })

  it('localStorage 接受三档,非法值与读取异常回退询问', () => {
    for (const mode of ['ask', 'auto', 'full'] as const) {
      values.set(storageKey, mode)
      expect(loadPermissionMode()).toBe(mode)
    }
    values.set(storageKey, 'invalid')
    expect(loadPermissionMode()).toBe('ask')
    values.delete(storageKey)
    expect(loadPermissionMode()).toBe('ask')
    getItem.mockImplementation(() => { throw new Error('denied') })
    expect(loadPermissionMode()).toBe('ask')
  })
})
