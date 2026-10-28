import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { act, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { Cloud } from 'lucide-react'
import { APIError } from '@/lib/http'
import { getAppNavSections } from '@/lib/navigation'
import { toast } from '@/lib/toast'
import commonZh from '@/locales/zh/common.json'
import zh from '@/locales/zh/settings.json'
import { XlyraCloudSettingsPage } from '@/routes/system/xlyra-cloud-settings-page'
import type { CloudBinding } from '@/features/settings/api/cloud-binding'
import type { CloudDeveloper } from '@/features/settings/api/cloud-developer'

const fetchCloudBinding = vi.hoisted(() => vi.fn())
const fetchCloudDeveloper = vi.hoisted(() => vi.fn())
const pairCloudBinding = vi.hoisted(() => vi.fn())
const cancelCloudPairing = vi.hoisted(() => vi.fn())
const disconnectCloudBinding = vi.hoisted(() => vi.fn())

vi.mock('@/features/settings/api/cloud-developer', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/cloud-developer')>()
  return { ...actual, fetchCloudDeveloper }
})

vi.mock('@/features/settings/api/cloud-binding', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/cloud-binding')>()
  return { ...actual, fetchCloudBinding, pairCloudBinding, cancelCloudPairing, disconnectCloudBinding }
})

vi.mock('@/lib/toast', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('@/components/ui/dialog', async () => {
  const { createElement, Fragment } = await import('react')
  const pass = (tag: string) => ({ children, ...props }: { children?: ReactNode }) =>
    createElement(tag, props, children)
  return {
    Dialog: ({ open, onOpenChange, children }: { open?: boolean; onOpenChange?: (open: boolean) => void; children?: ReactNode }) =>
      (open ? createElement(Fragment, null, onOpenChange ? createElement('button', {
        type: 'button',
        'data-testid': 'mock-dialog-dismiss',
        onClick: () => onOpenChange(false),
      }) : null, children) : null),
    DialogContent: pass('div'),
    DialogHeader: pass('div'),
    DialogTitle: pass('h2'),
    DialogDescription: pass('p'),
    DialogFooter: ({ cancel, confirm }: { cancel?: ReactNode; confirm?: ReactNode }) =>
      createElement('div', null, cancel, confirm),
    DialogBody: pass('div'),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  class NoopObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal('ResizeObserver', NoopObserver)
  vi.stubGlobal('IntersectionObserver', NoopObserver)
  await i18n.init({
    lng: 'zh',
    defaultNS: 'settings',
    ns: ['settings', 'common'],
    resources: { zh: { settings: zh, common: commonZh } },
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  })
})

beforeEach(() => {
  fetchCloudDeveloper.mockResolvedValue({ status: 'none', namespaces: [], keys: [], plugins: [] })
})

afterEach(() => {
  fetchCloudBinding.mockReset()
  fetchCloudDeveloper.mockReset()
  pairCloudBinding.mockReset()
  cancelCloudPairing.mockReset()
  disconnectCloudBinding.mockReset()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
  vi.useRealTimers()
})

function binding(overrides: Partial<CloudBinding> = {}): CloudBinding {
  return {
    status: 'unbound',
    instance_name: '',
    user_display_name: '',
    cloud_url: '',
    bound_at: null,
    last_sync_at: null,
    last_error: '',
    last_error_at: null,
    pairing: null,
    max_poll_seconds: 0,
    ...overrides,
  }
}

describe('xLyra Cloud settings page', () => {
  it('shows a loading skeleton without the danger section', async () => {
    fetchCloudBinding.mockReturnValue(new Promise(() => {}))
    const mounted = await mountPage()
    try {
      expect(mounted.container.textContent).toContain('系统')
      expect(mounted.container.textContent).toContain('把这台 xLyra 连接到你的 xLyra 账号')
      expect(findButton(mounted.container, '刷新')).not.toBeNull()
      expect(findTestId(mounted.container, 'cloud-binding-loading')).not.toBeNull()
      expect(findButton(mounted.container, '连接账号')).toBeNull()
      expect(mounted.container.textContent).not.toContain('危险操作')
    } finally {
      await mounted.unmount()
    }
  })

  it('renders unbound, revoked, and a read failure', async () => {
    fetchCloudBinding.mockResolvedValue(binding())
    const unbound = await mountPage()
    try {
      expect(unbound.container.textContent).toContain('未连接')
      expect(unbound.container.textContent).toContain('连接后可以在 xLyra Cloud 控制台里查看和管理这台实例。')
      expect(unbound.container.textContent).toContain('不包含站点地址、密钥或配置')
      expect(findButton(unbound.container, '连接账号')).not.toBeNull()
      expect(findButton(unbound.container, '刷新')).not.toBeNull()
      await click(findButton(unbound.container, '刷新'))
      expect(fetchCloudBinding).toHaveBeenCalledTimes(2)
      expect(unbound.container.textContent).not.toContain('危险操作')
    } finally {
      await unbound.unmount()
    }

    fetchCloudBinding.mockResolvedValue(binding({ status: 'revoked' }))
    const revoked = await mountPage()
    try {
      expect(revoked.container.textContent).toContain('连接已解除')
      expect(revoked.container.textContent).toContain('这个实例已在账号侧被解除连接。')
      expect(findButton(revoked.container, '重新连接')).not.toBeNull()
      expect(revoked.container.textContent).not.toContain('危险操作')
    } finally {
      await revoked.unmount()
    }

    fetchCloudBinding.mockRejectedValue(new APIError('Cloud 暂不可用', 503, 'cloud_binding_unavailable'))
    const failed = await mountPage()
    try {
      expect(failed.container.textContent).toContain('无法读取连接状态')
      expect(failed.container.textContent).toContain('Cloud 暂不可用')
      expect(failed.container.textContent).not.toContain('危险操作')
      fetchCloudBinding.mockResolvedValue(binding())
      await click(findButton(failed.container, '重试'))
      expect(failed.container.textContent).toContain('未连接')
    } finally {
      await failed.unmount()
    }
  })

  it('starts pairing from connect and from reconnect', async () => {
    const pairing = binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    })
    fetchCloudBinding.mockResolvedValue(binding())
    pairCloudBinding.mockResolvedValue(pairing)
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '连接账号'))
      expect(pairCloudBinding).toHaveBeenCalledOnce()
      expect(mounted.container.textContent).toContain('等待连接')
      expect(findTagText(mounted.container, 'H2', '配对')).not.toBeNull()
      expect(mounted.container.textContent).toContain('在已登录的 xLyra Cloud 控制台点「连接实例」')
      expect(fieldValue(findTestId(mounted.container, 'cloud-pairing-code'))).toBe('GYD4-LMGT')
      expect(findButton(mounted.container, '查看配对码')).not.toBeNull()
      expect(mounted.container.textContent).not.toContain('危险操作')
    } finally {
      await mounted.unmount()
    }

    fetchCloudBinding.mockResolvedValue(binding({ status: 'revoked' }))
    pairCloudBinding.mockClear()
    const revoked = await mountPage()
    try {
      await click(findButton(revoked.container, '重新连接'))
      expect(pairCloudBinding).toHaveBeenCalledOnce()
    } finally {
      await revoked.unmount()
    }
  })

  it('toasts the pair error', async () => {
    fetchCloudBinding.mockResolvedValue(binding())
    pairCloudBinding.mockRejectedValue(new APIError('已经连接', 409, 'cloud_binding_exists'))
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '连接账号'))
      expect(toast.error).toHaveBeenCalledWith('已经连接')
    } finally {
      await mounted.unmount()
    }
  })

  it('counts down, then offers regenerate', async () => {
    vi.useFakeTimers()
    const expiresAt = new Date(Date.now() + 5000).toISOString()
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: expiresAt },
    }))
    const mounted = await mountPage()
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })
      expect(mounted.container.textContent).toContain('剩余 00:05')
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1000)
      })
      expect(mounted.container.textContent).toContain('剩余 00:04')
      await act(async () => {
        await vi.advanceTimersByTimeAsync(4000)
      })
      expect(mounted.container.textContent).toContain('配对码已过期')
      pairCloudBinding.mockResolvedValue(binding({
        status: 'pairing',
        cloud_url: 'https://xlyra.yachiyo.im',
        pairing: { user_code: 'NEW1-CODE', expires_at: new Date(Date.now() + 60_000).toISOString() },
      }))
      expect(findButton(mounted.container, '重新生成')).not.toBeNull()
      await click(findButton(mounted.container, '重新生成'))
      expect(pairCloudBinding).toHaveBeenCalledOnce()
      expect(fieldValue(findTestId(mounted.container, 'cloud-pairing-code'))).toBe('NEW1-CODE')
    } finally {
      await mounted.unmount()
    }
  })

  it('polls while pairing and toasts when the account is connected', async () => {
    vi.useFakeTimers()
    const pairing = binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    })
    const bound = binding({
      status: 'bound',
      instance_name: 'edge-1',
      user_display_name: 'Ada',
      cloud_url: 'https://xlyra.yachiyo.im',
      bound_at: '2026-10-26T01:02:03Z',
    })
    fetchCloudBinding.mockResolvedValueOnce(pairing).mockResolvedValueOnce(bound)
    const mounted = await mountPage()
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })
      expect(fetchCloudBinding).toHaveBeenCalledTimes(1)
      expect(mounted.container.textContent).toContain('等待连接')
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2000)
      })
      await flushUpdates()
      expect(fetchCloudBinding).toHaveBeenCalledTimes(2)
      expect(toast.success).toHaveBeenCalledWith('已连接到 xLyra Cloud')
      expect(mounted.container.textContent).toContain('已连接')
    } finally {
      await mounted.unmount()
    }
  })

  it('does not poll while the page is hidden', async () => {
    vi.useFakeTimers()
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    }))
    const mounted = await mountPage({ visibility: 'hidden' })
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })
      expect(fetchCloudBinding).toHaveBeenCalledTimes(1)
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000)
      })
      expect(fetchCloudBinding).toHaveBeenCalledTimes(1)
    } finally {
      await mounted.unmount()
    }
  })

  it('hides console links for unsafe cloud urls and keeps them for https', async () => {
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      cloud_url: 'javascript:alert(1)',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    }))
    const scriptURL = await mountPage()
    try {
      expect(findButton(scriptURL.container, '复制并打开 Cloud')).toBeNull()
      expect(scriptURL.container.textContent).not.toContain('在 xLyra Cloud 打开')
    } finally {
      await scriptURL.unmount()
    }

    fetchCloudBinding.mockResolvedValue(binding({
      status: 'bound',
      cloud_url: 'http://evil.example',
      user_display_name: 'Ada',
      instance_name: 'edge-1',
      bound_at: '2026-10-26T01:02:03Z',
    }))
    const evil = await mountPage()
    try {
      expect(findTestId(evil.container, 'cloud-manage-link')).toBeNull()
      expect(evil.container.textContent).not.toContain('在官网管理')
    } finally {
      await evil.unmount()
    }

    fetchCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    }))
    const safe = await mountPage()
    const open = vi.fn()
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('open', open)
    vi.stubGlobal('navigator', { clipboard: { writeText }, userAgent: 'node' })
    try {
      const button = findButton(safe.container, '复制并打开 Cloud')
      expect(button).not.toBeNull()
      await click(button)
      expect(open).toHaveBeenCalledWith('https://xlyra.yachiyo.im/console/#connect=GYD4-LMGT', '_blank', 'noopener,noreferrer')
    } finally {
      await safe.unmount()
    }
  })

  it('dismisses the pairing guide without cancelling, then reopens it', async () => {
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    }))
    const mounted = await mountPage()
    try {
      expect(fieldValue(findTestId(mounted.container, 'cloud-pairing-code'))).toBe('GYD4-LMGT')
      await click(findTestId(mounted.container, 'mock-dialog-dismiss'))
      expect(findTestId(mounted.container, 'cloud-pairing-code')).toBeNull()
      expect(cancelCloudPairing).not.toHaveBeenCalled()
      expect(mounted.container.textContent).toContain('等待连接')
      await click(findButton(mounted.container, '查看配对码'))
      expect(fieldValue(findTestId(mounted.container, 'cloud-pairing-code'))).toBe('GYD4-LMGT')
    } finally {
      await mounted.unmount()
    }
  })

  it('copies the pairing code, opens Cloud, and drops pairing', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const open = vi.fn()
    vi.stubGlobal('navigator', { clipboard: { writeText }, userAgent: 'node' })
    vi.stubGlobal('open', open)
    fetchCloudBinding
      .mockResolvedValueOnce(binding({
        status: 'pairing',
        cloud_url: 'https://xlyra.yachiyo.im',
        pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
      }))
      .mockResolvedValue(binding())
    cancelCloudPairing.mockResolvedValue(undefined)
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '复制并打开 Cloud'))
      expect(writeText).toHaveBeenCalledWith('GYD4-LMGT')
      expect(toast.success).toHaveBeenCalledWith('已复制配对码')
      expect(open).toHaveBeenCalledWith('https://xlyra.yachiyo.im/console/#connect=GYD4-LMGT', '_blank', 'noopener,noreferrer')

      await click(findButton(mounted.container, '取消配对'))
      expect(cancelCloudPairing).toHaveBeenCalledOnce()
      expect(mounted.container.textContent).toContain('未连接')
    } finally {
      await mounted.unmount()
    }
  })

  it('asks to regenerate when the pairing code is gone', async () => {
    fetchCloudBinding.mockResolvedValue(binding({ status: 'pairing', pairing: null, cloud_url: 'https://xlyra.yachiyo.im' }))
    pairCloudBinding.mockResolvedValue(binding({
      status: 'pairing',
      cloud_url: 'https://xlyra.yachiyo.im',
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2099-01-01T00:00:00Z' },
    }))
    const mounted = await mountPage()
    try {
      expect(mounted.container.textContent).toContain('配对码已不可见，请重新生成')
      expect(findTestId(mounted.container, 'cloud-pairing-code')).toBeNull()
      await click(findButton(mounted.container, '重新生成'))
      expect(pairCloudBinding).toHaveBeenCalledOnce()
      expect(fieldValue(findTestId(mounted.container, 'cloud-pairing-code'))).toBe('GYD4-LMGT')
    } finally {
      await mounted.unmount()
    }
  })

  it('shows the bound developer namespaces, keys, and published plugins', async () => {
    const profile: CloudDeveloper = {
      status: 'active',
      display_name: 'Ada',
      namespaces: [{ name: 'yachiyo', status: 'active' }],
      keys: [{ fingerprint: 'SHA256:abc', label: '笔记本', status: 'active' }],
      plugins: [{ id: 'yachiyo.site-balance-alert', latest_version: '1.0.2', latest_status: 'published' }],
    }
    fetchCloudDeveloper.mockResolvedValue(profile)
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'bound',
      user_display_name: 'Ada',
      cloud_url: 'https://xlyra.yachiyo.im',
    }))
    const mounted = await mountPage()
    try {
      expect(mounted.container.textContent).toContain('开发者身份')
      expect(mounted.container.textContent).toContain('yachiyo')
      expect(mounted.container.textContent).toContain('SHA256:abc')
      expect(mounted.container.textContent).toContain('1 把')
      expect(mounted.container.textContent).toContain('yachiyo.site-balance-alert')
      expect(mounted.container.textContent).toContain('最新已发布 1.0.2')
      expect(mounted.container.textContent).toContain('已发布')
    } finally {
      await mounted.unmount()
    }
  })

  it('shows cloud unavailable instead of not a developer when the developer profile cannot be read', async () => {
    fetchCloudDeveloper.mockResolvedValue({ status: 'none', unavailable: true, namespaces: [], keys: [], plugins: [] })
    fetchCloudBinding.mockResolvedValue(binding({
      status: 'bound',
      user_display_name: 'Ada',
      cloud_url: 'https://xlyra.yachiyo.im',
    }))
    const mounted = await mountPage()
    try {
      expect(mounted.container.textContent).toContain('暂时无法读取开发者身份')
      expect(mounted.container.textContent).not.toContain('还不是开发者')
      expect(mounted.container.textContent).not.toContain('这个账号还不是开发者')
    } finally {
      await mounted.unmount()
    }
  })

  it('shows the bound account, a sync error, and confirms disconnect', async () => {
    const bound = binding({
      status: 'bound',
      instance_name: 'edge-1',
      user_display_name: 'Ada',
      cloud_url: 'https://xlyra.yachiyo.im',
      bound_at: '2026-10-26T01:02:03Z',
      last_sync_at: null,
      last_error: 'timeout',
      last_error_at: '2026-10-26T02:03:04Z',
    })
    fetchCloudBinding.mockResolvedValueOnce(bound).mockResolvedValue(binding())
    disconnectCloudBinding.mockResolvedValue(undefined)
    const mounted = await mountPage()
    try {
      expect(mounted.container.textContent).toContain('还不是开发者')
    expect(mounted.container.textContent).toContain('已连接到 Ada 的 xLyra 账号')
      expect(mounted.container.textContent).toContain('实例名称：edge-1')
      expect(mounted.container.textContent).toContain('连接于')
      expect(mounted.container.textContent).toContain('最近心跳')
      expect(mounted.container.textContent).toContain('尚未心跳')
      expect(mounted.container.textContent).toContain('最近一次同步失败：timeout')
      expect(mounted.container.textContent).toContain('这不影响 xLyra 的正常使用，会自动重试')
      expect(mounted.container.textContent).toContain('危险操作')
      const manage = findTestId(mounted.container, 'cloud-manage-link')
      expect(manage?.getAttribute('href')).toBe('https://xlyra.yachiyo.im/console/')
      expect(manage?.getAttribute('target')).toBe('_blank')
      expect(findTagText(mounted.container, 'H2', '断开与 xLyra Cloud 的连接？')).toBeNull()

      await click(findButton(mounted.container, '断开连接'))
      const title = findTagText(mounted.container, 'H2', '断开与 xLyra Cloud 的连接？')
      const dialog = title?.parentNode?.parentNode ?? null
      expect(dialog?.textContent).toContain('断开与 xLyra Cloud 的连接？')
      expect(dialog?.textContent).toContain('需要重新输入配对码才能再次连接')
      await click(findButton(dialog, '断开'))
      expect(disconnectCloudBinding).toHaveBeenCalledOnce()
      expect(toast.success).toHaveBeenCalledWith('已断开连接')
      expect(mounted.container.textContent).toContain('未连接')
      expect(mounted.container.textContent).not.toContain('危险操作')
    } finally {
      await mounted.unmount()
    }
  })
})

describe('settings navigation', () => {
  it('puts xLyra Cloud and plugins in the system section after operations', () => {
    const sections = getAppNavSections((key) => key)
    const keys = sections.map((section) => section.key)
    expect(keys.indexOf('system')).toBe(keys.indexOf('operations') + 1)
    const system = sections.find((section) => section.key === 'system')
    expect(system?.label).toBe('nav.system')
    expect(system?.items.map((item) => item.to)).toEqual(['/system/xlyra-cloud', '/system/plugins'])
    expect(system?.items[0]?.icon).toBe(Cloud)
    const settings = sections.flatMap((section) => section.items).find((item) => item.to === '/settings')
    const children = (settings?.children ?? []).map((item) => item.to)
    expect(children).not.toContain('/system/xlyra-cloud')
    expect(children).not.toContain('/system/plugins')
  })
})

async function flushUpdates() {
  if (vi.isFakeTimers()) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    return
  }
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function click(node: MiniNode | null) {
  await act(async () => {
    node?.dispatchEvent(clickEvent(node!))
  })
  await flushUpdates()
}

async function mountPage(options?: { visibility?: DocumentVisibilityState }) {
  const doc = new MiniDocument()
  doc.visibilityState = options?.visibility ?? 'visible'
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as {
    window: unknown
    document: unknown
    HTMLElement?: unknown
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  const previous = {
    window: globals.window,
    document: globals.document,
    HTMLIFrameElement: (globals as { HTMLIFrameElement?: unknown }).HTMLIFrameElement,
  }
  class FrameElement {}
  globals.window = globalThis
  globals.document = doc
  ;(globals as { HTMLIFrameElement?: unknown }).HTMLIFrameElement = FrameElement
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const restore = () => {
    globals.window = previous.window
    globals.document = previous.document
    ;(globals as { HTMLIFrameElement?: unknown }).HTMLIFrameElement = previous.HTMLIFrameElement
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
  })
  try {
    const { createRoot } = await import('react-dom/client')
    const root = createRoot(container as unknown as Element)
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>
            <XlyraCloudSettingsPage />
          </I18nextProvider>
        </QueryClientProvider>,
      )
    })
    await flushUpdates()
    return {
      container,
      async unmount() {
        await act(async () => {
          root.unmount()
        })
        restore()
      },
    }
  } catch (error) {
    restore()
    throw error
  }
}

type MiniListener = (event: MiniEvent) => void

const miniListeners = new WeakMap<object, Map<string, Set<MiniListener>>>()

function trackListener(target: object, type: string, listener: MiniListener) {
  const byType = miniListeners.get(target) ?? new Map<string, Set<MiniListener>>()
  const set = byType.get(type) ?? new Set<MiniListener>()
  set.add(listener)
  byType.set(type, set)
  miniListeners.set(target, byType)
}

interface MiniEvent {
  type: string
  target: MiniNode | null
  currentTarget: MiniNode | MiniDocument | null
  bubbles: boolean
  cancelable: boolean
  defaultPrevented: boolean
  button: number
  eventPhase: number
  timeStamp: number
  preventDefault: () => void
  stopPropagation: () => void
  stopped: boolean
}

class MiniNode {
  nodeType = 1
  nodeName: string
  tagName: string
  ownerDocument: MiniDocument
  parentNode: MiniNode | null = null
  childNodes: MiniNode[] = []
  nodeValue: string | null = null
  attributes: Record<string, string> = {}
  style: { setProperty: (name: string, value: string) => void } & Record<string, string>
  offsetWidth = 0
  offsetHeight = 0

  constructor(tag: string, ownerDocument: MiniDocument) {
    this.tagName = tag.toUpperCase()
    this.nodeName = this.tagName
    this.ownerDocument = ownerDocument
    const values: Record<string, string> = {}
    this.style = new Proxy(values, {
      get: (target, prop) => (prop === 'setProperty'
        ? (name: string, value: string) => { target[name] = value }
        : target[String(prop)]),
      set: (target, prop, value) => {
        target[String(prop)] = String(value)
        return true
      },
    }) as MiniNode['style']
  }

  get nextSibling(): MiniNode | null {
    if (!this.parentNode) return null
    const index = this.parentNode.childNodes.indexOf(this)
    return this.parentNode.childNodes[index + 1] ?? null
  }

  get textContent() {
    if (this.nodeType === 3 || this.nodeType === 8) return this.nodeValue ?? ''
    return this.childNodes.map((child) => child.textContent).join('')
  }

  set textContent(value: string) {
    this.childNodes = []
    if (value) this.appendChild(this.ownerDocument.createTextNode(value))
  }

  set className(value: string) {
    this.attributes.class = String(value)
  }

  get className() {
    return this.attributes.class ?? ''
  }

  setAttribute(name: string, value: string) {
    this.attributes[name] = String(value)
  }

  getAttribute(name: string) {
    return this.attributes[name] ?? null
  }

  removeAttribute(name: string) {
    delete this.attributes[name]
  }

  hasAttribute(name: string) {
    return Object.prototype.hasOwnProperty.call(this.attributes, name)
  }

  focus() {}

  appendChild<T extends MiniNode>(child: T) {
    return this.insertBefore(child, null)
  }

  insertBefore<T extends MiniNode>(child: T, before: MiniNode | null) {
    if (child.parentNode) child.parentNode.removeChild(child)
    child.parentNode = this
    if (!before) this.childNodes.push(child)
    else {
      const index = this.childNodes.indexOf(before)
      if (index < 0) throw new Error('insertBefore target is not a child')
      this.childNodes.splice(index, 0, child)
    }
    return child
  }

  removeChild<T extends MiniNode>(child: T) {
    const index = this.childNodes.indexOf(child)
    if (index < 0) throw new Error('removeChild target is not a child')
    this.childNodes.splice(index, 1)
    child.parentNode = null
    return child
  }

  addEventListener(type: string, listener: MiniListener) {
    trackListener(this, type, listener)
  }

  removeEventListener(type: string, listener: MiniListener) {
    miniListeners.get(this)?.get(type)?.delete(listener)
  }

  dispatchEvent(event: MiniEvent) {
    event.target = this
    const chain: Array<MiniNode | MiniDocument> = [this]
    let parent: MiniNode | null = this.parentNode
    while (parent) {
      chain.push(parent)
      parent = parent.parentNode
    }
    chain.push(this.ownerDocument)
    for (const current of chain) {
      event.currentTarget = current
      for (const listener of miniListeners.get(current)?.get(event.type) ?? []) listener.call(current, event)
      if (event.stopped) break
    }
    return true
  }
}

class MiniDocument {
  nodeType = 9
  nodeName = '#document'
  documentElement: MiniNode
  body: MiniNode
  defaultView = globalThis
  visibilityState: DocumentVisibilityState = 'visible'

  constructor() {
    this.documentElement = new MiniNode('html', this)
    this.body = new MiniNode('body', this)
    this.documentElement.appendChild(this.body)
  }

  createElement(tag: string) {
    const node = new MiniNode(tag, this)
    if (tag === '#text') {
      node.nodeType = 3
      node.nodeName = '#text'
      node.tagName = '#text'
    }
    return node
  }

  createElementNS(_namespace: string, tag: string) {
    return this.createElement(tag)
  }

  createTextNode(text: string) {
    const node = this.createElement('#text')
    node.nodeValue = text
    return node
  }

  createComment(text: string) {
    const node = this.createElement('#comment')
    node.nodeType = 8
    node.nodeValue = text
    return node
  }

  addEventListener(type: string, listener: MiniListener) {
    trackListener(this, type, listener)
  }

  removeEventListener(type: string, listener: MiniListener) {
    miniListeners.get(this)?.get(type)?.delete(listener)
  }
}

function clickEvent(target: MiniNode): MiniEvent {
  return {
    type: 'click',
    target,
    currentTarget: target,
    bubbles: true,
    cancelable: true,
    defaultPrevented: false,
    button: 0,
    eventPhase: 2,
    timeStamp: 0,
    stopped: false,
    preventDefault() { this.defaultPrevented = true },
    stopPropagation() { this.stopped = true },
  }
}

function fieldValue(node: MiniNode | null) {
  if (!node) return ''
  const value = (node as MiniNode & { value?: string }).value
  if (typeof value === 'string' && value.length > 0) return value
  return node.getAttribute('value') ?? node.textContent
}

function findTagText(node: MiniNode, tag: string, text: string): MiniNode | null {
  if (node.tagName === tag && node.textContent.includes(text)) return node
  for (const child of node.childNodes) {
    const found = findTagText(child, tag, text)
    if (found) return found
  }
  return null
}

function findButton(node: MiniNode | null, label: string): MiniNode | null {
  if (!node) return null
  if (node.tagName === 'BUTTON' && node.textContent.trim() === label) return node
  for (const child of node.childNodes) {
    const found = findButton(child, label)
    if (found) return found
  }
  return null
}

function findTestId(node: MiniNode, testId: string): MiniNode | null {
  if (node.getAttribute('data-testid') === testId) return node
  for (const child of node.childNodes) {
    const found = findTestId(child, testId)
    if (found) return found
  }
  return null
}
