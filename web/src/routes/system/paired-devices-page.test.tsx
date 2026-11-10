import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { act, createElement } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@/lib/toast'
import commonZh from '@/locales/zh/common.json'
import zh from '@/locales/zh/settings.json'
import { DevicesSettingsPage } from '@/routes/system/paired-devices-page'
import { devicesQueryKeys, type DeviceItem, type DevicePairing } from '@/features/auth/api/paired-devices'

const listDevices = vi.hoisted(() => vi.fn())
const createDevicePairing = vi.hoisted(() => vi.fn())
const revokeDevice = vi.hoisted(() => vi.fn())
const revokeAllDevices = vi.hoisted(() => vi.fn())

vi.mock('@/features/auth/api/paired-devices', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/auth/api/paired-devices')>()
  return { ...actual, listDevices, createDevicePairing, revokeDevice, revokeAllDevices }
})

vi.mock('@/lib/toast', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('@/components/common/copy-to-clipboard', () => ({
  copyToClipboard: vi.fn(async () => true),
}))

// 假 DOM 下 qrcode 库无法工作，换成占位渲染
vi.mock('@/components/common/qr-code', () => ({
  QRCode: () => createElement('div'),
}))

// Radix Dialog 依赖真实 DOM，换成 open 时才渲染子元素的简化实现
vi.mock('@/components/ui/dialog', () => {
  const passthrough = (tag: string) => (props: { children?: React.ReactNode }) => createElement(tag, props)
  return {
    Dialog: ({ open, children }: { open: boolean; children?: React.ReactNode }) => open ? createElement('div', null, children) : null,
    DialogContent: passthrough('div'),
    DialogHeader: passthrough('div'),
    DialogBody: passthrough('div'),
    DialogTitle: passthrough('h2'),
    DialogDescription: passthrough('p'),
    DialogFooter: ({ children, cancel, confirm }: { children?: React.ReactNode; cancel?: React.ReactNode; confirm?: React.ReactNode }) =>
      createElement('div', null, children, cancel, confirm),
  }
})

vi.mock('@/components/common/confirm-dialog', () => ({
  ConfirmDialog: ({ open, title, confirmLabel, onConfirm }: {
    open: boolean
    title: string
    confirmLabel: string
    onConfirm: () => void
  }) => open ? createElement('div', null, [
    createElement('p', { key: 'title' }, title),
    createElement('button', { key: 'confirm', type: 'button', onClick: onConfirm }, confirmLabel),
  ]) : null,
}))

const i18n = createInstance()

beforeAll(async () => {
  class NoopObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  class NoopMutationObserver {
    observe() {}
    disconnect() {}
    takeRecords() { return [] }
  }
  vi.stubGlobal('ResizeObserver', NoopObserver)
  vi.stubGlobal('IntersectionObserver', NoopObserver)
  vi.stubGlobal('MutationObserver', NoopMutationObserver)
  vi.stubGlobal('getComputedStyle', () => ({ getPropertyValue: () => '', ...{} }))
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => setTimeout(() => cb(0), 0) as unknown as number)
  vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id))
  vi.stubGlobal('location', { origin: 'http://192.168.1.10:5173' })
  await i18n.init({
    lng: 'zh',
    defaultNS: 'settings',
    ns: ['settings', 'common'],
    resources: { zh: { settings: zh, common: commonZh } },
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  })
})

afterAll(() => {
  vi.unstubAllGlobals()
})

beforeEach(() => {
  listDevices.mockResolvedValue([])
  createDevicePairing.mockResolvedValue(pairing())
  revokeDevice.mockResolvedValue(undefined)
  revokeAllDevices.mockResolvedValue(undefined)
})

afterEach(() => {
  listDevices.mockReset()
  createDevicePairing.mockReset()
  revokeDevice.mockReset()
  revokeAllDevices.mockReset()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

function device(overrides: Partial<DeviceItem> = {}): DeviceItem {
  return {
    id: '11111111-1111-1111-1111-111111111111',
    name: '小夜的 iPhone',
    platform: 'iOS',
    created_at: '2025-01-01T00:00:00Z',
    last_seen_at: '2025-06-01T00:00:00Z',
    last_ip: '203.0.113.10',
    current: false,
    ...overrides,
  }
}

function pairing(overrides: Partial<DevicePairing> = {}): DevicePairing {
  return {
    pairing_id: '22222222-2222-2222-2222-222222222222',
    code: 'ABCD-2345',
    expires_at: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
    ...overrides,
  }
}

describe('devices settings page', () => {
  it('渲染设备列表：名称、平台、IP、相对时间', async () => {
    listDevices.mockResolvedValue([
      device(),
      device({ id: '33333333-3333-3333-3333-333333333333', name: '备用 iPad', platform: 'iPadOS', current: true }),
    ])
    const mounted = await mountPage()
    try {
      const text = mounted.container.textContent
      expect(text).toContain('小夜的 iPhone')
      expect(text).toContain('iOS')
      expect(text).toContain('203.0.113.10')
      expect(text).toContain('备用 iPad')
      expect(text).toContain('当前设备')
    } finally {
      await mounted.unmount()
    }
  })

  it('生成配对码先确认地址，继续后展示配对码与地址，新设备连上后自动关闭弹窗', async () => {
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '生成配对码'))
      // 确认步骤不调接口，先展示当前地址
      expect(createDevicePairing).not.toHaveBeenCalled()
      expect(mounted.container.textContent).toContain('确认服务器地址')
      expect(mounted.container.textContent).toContain('http://192.168.1.10:5173')
      await click(findButton(mounted.container, '地址正确，继续'))
      expect(createDevicePairing).toHaveBeenCalledOnce()
      const text = mounted.container.textContent
      expect(text).toContain('ABCD-2345')
      expect(text).toContain('http://192.168.1.10:5173')
      expect(text).toContain('后过期')
      // 模拟轮询拉到设备列表里多出一台，弹窗应自动关闭并提示已连接
      listDevices.mockResolvedValue([device()])
      await act(async () => {
        await mounted.client.refetchQueries({ queryKey: devicesQueryKeys.list() })
      })
      await flushUpdates()
      expect(toast.success).toHaveBeenCalledWith('已连接：小夜的 iPhone')
      expect(mounted.container.textContent).not.toContain('ABCD-2345')
    } finally {
      await mounted.unmount()
    }
  })

  it('取消地址确认弹窗不会调用配对接口', async () => {
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '生成配对码'))
      expect(mounted.container.textContent).toContain('确认服务器地址')
      await click(findButton(mounted.container, '取消'))
      expect(createDevicePairing).not.toHaveBeenCalled()
      expect(mounted.container.textContent).not.toContain('确认服务器地址')
    } finally {
      await mounted.unmount()
    }
  })

  it('吊销设备需要确认后才调用 DELETE', async () => {
    listDevices.mockResolvedValue([device()])
    revokeDevice.mockResolvedValue(undefined)
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '吊销'))
      expect(revokeDevice).not.toHaveBeenCalled()
      // 确认对话框出现后再点一次吊销
      await click(findButtons(mounted.container, '吊销').at(-1))
      expect(revokeDevice).toHaveBeenCalledWith('11111111-1111-1111-1111-111111111111')
      expect(toast.success).toHaveBeenCalledWith('设备已吊销')
    } finally {
      await mounted.unmount()
    }
  })

  it('全部吊销走确认并调用 revoke-all', async () => {
    listDevices.mockResolvedValue([device()])
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '全部吊销'))
      expect(revokeAllDevices).not.toHaveBeenCalled()
      await click(findButtons(mounted.container, '全部吊销').at(-1))
      expect(revokeAllDevices).toHaveBeenCalledOnce()
      expect(toast.success).toHaveBeenCalledWith('已吊销全部设备')
    } finally {
      await mounted.unmount()
    }
  })

  it('空列表显示空状态文案', async () => {
    listDevices.mockResolvedValue([])
    const mounted = await mountPage()
    try {
      const text = mounted.container.textContent
      expect(text).toContain('暂无已连接的设备')
      expect(text).not.toContain('小夜的 iPhone')
      const revokeAll = findButton(mounted.container, '全部吊销')
      expect(revokeAll?.hasAttribute('disabled')).toBe(true)
    } finally {
      await mounted.unmount()
    }
  })
})

async function flushUpdates() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function dispatch(node: MiniNode, type: string) {
  await act(async () => {
    node.dispatchEvent(inputEvent(node, type))
  })
  await flushUpdates()
}

async function click(node: MiniNode | null | undefined) {
  await dispatch(node!, 'mousedown')
  await dispatch(node!, 'click')
}

async function mountPage() {
  const doc = new MiniDocument()
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as {
    window: unknown
    document: unknown
    HTMLIFrameElement?: unknown
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  const previous = {
    window: globals.window,
    document: globals.document,
    HTMLIFrameElement: globals.HTMLIFrameElement,
  }
  class FrameElement {}
  globals.window = globalThis
  globals.document = doc
  globals.HTMLIFrameElement = FrameElement
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const restore = () => {
    globals.window = previous.window
    globals.document = previous.document
    globals.HTMLIFrameElement = previous.HTMLIFrameElement
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
            <DevicesSettingsPage />
          </I18nextProvider>
        </QueryClientProvider>,
      )
    })
    await flushUpdates()
    return {
      container,
      client,
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
  ctrlKey: boolean
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
  offsetWidth = 0
  offsetHeight = 0
  style: { setProperty: (name: string, value: string) => void } & Record<string, string>

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

  querySelector(selector: string): MiniNode | null {
    return this.querySelectorAll(selector)[0] ?? null
  }

  querySelectorAll(selector: string): MiniNode[] {
    const [attr, value] = /^\[([^=]+)="?([^"\]]+)"?\]$/.exec(selector)?.slice(1) ?? []
    if (!attr) return []
    return this.collectByAttr(attr, value)
  }

  collectByAttr(attr: string, value: string): MiniNode[] {
    const found: MiniNode[] = []
    const walk = (node: MiniNode) => {
      for (const child of node.childNodes) {
        if (child.nodeType !== 1) continue
        const attrValue = attr === 'role' ? (child as MiniNode & { role?: string }).role ?? child.getAttribute('role') : child.getAttribute(attr)
        if (attrValue === value) found.push(child)
        walk(child)
      }
    }
    walk(this)
    return found
  }

  contains(node: MiniNode | null): boolean {
    if (!node) return false
    let current: MiniNode | null = node
    while (current) {
      if (current === this) return true
      current = current.parentNode
    }
    return false
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

  attachEvent() {}

  detachEvent() {}

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
    if (index < 0) throw new Error('node is not a child')
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

  addEventListener(type: string, listener: MiniListener) {
    trackListener(this, type, listener)
  }

  removeEventListener(type: string, listener: MiniListener) {
    miniListeners.get(this)?.get(type)?.delete(listener)
  }
}

function inputEvent(target: MiniNode, type: string): MiniEvent {
  return {
    type,
    target,
    currentTarget: target,
    bubbles: true,
    cancelable: true,
    defaultPrevented: false,
    button: 0,
    ctrlKey: false,
    eventPhase: 2,
    timeStamp: 0,
    stopped: false,
    preventDefault() { this.defaultPrevented = true },
    stopPropagation() { this.stopped = true },
  }
}

function collectButtons(node: MiniNode | null, label: string, found: MiniNode[] = []): MiniNode[] {
  if (!node) return found
  if (node.tagName === 'BUTTON' && node.textContent.trim() === label) found.push(node)
  for (const child of node.childNodes) {
    collectButtons(child, label, found)
  }
  return found
}

function findButton(node: MiniNode | null, label: string): MiniNode | null {
  return collectButtons(node, label)[0] ?? null
}

function findButtons(node: MiniNode | null, label: string): MiniNode[] {
  return collectButtons(node, label)
}
