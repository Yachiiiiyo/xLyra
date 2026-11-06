import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { act, createElement, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@/lib/toast'
import commonZh from '@/locales/zh/common.json'
import zh from '@/locales/zh/settings.json'
import { AppearanceSettingsPage } from '@/routes/settings/global/appearance-settings-page'
import type { AppearanceConfig } from '@/features/appearance/api/appearance'

const updateAppearance = vi.hoisted(() => vi.fn())
const useAppearanceQuery = vi.hoisted(() => vi.fn())
const convertImageToWebp = vi.hoisted(() => vi.fn())

vi.mock('@/features/appearance/api/appearance', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/appearance/api/appearance')>()
  return { ...actual, updateAppearance, useAppearanceQuery }
})

vi.mock('@/lib/toast', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('@/lib/image-webp', () => ({ convertImageToWebp }))

// 假 DOM 下 Radix Tabs 的内容挂载不可靠，这里换成同时渲染所有面板的简化实现
vi.mock('@/components/ui/tabs', () => {
  const passthrough = (tag: string) => ({ children }: { children?: ReactNode }) => createElement(tag, null, children)
  return {
    Tabs: passthrough('div'),
    TabsList: passthrough('div'),
    TabsTrigger: passthrough('button'),
    TabsContent: passthrough('div'),
  }
})

// Radix Switch 依赖真实 DOM 的 HTMLInputElement，假 DOM 下换成简化按钮
vi.mock('@/components/ui/switch', () => ({
  Switch: ({ label, checked, disabled, onCheckedChange }: {
    label?: string
    checked?: boolean
    disabled?: boolean
    onCheckedChange?: (checked: boolean) => void
  }) => createElement('button', { type: 'button', disabled, onClick: () => onCheckedChange?.(!checked) }, label),
}))

vi.mock('@/lib/draw-starfield', () => ({
  drawStarfield: vi.fn(),
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
  class FileReaderStub {
    result: string | null = null
    error: Error | null = null
    onload: (() => void) | null = null
    onerror: (() => void) | null = null

    readAsDataURL() {
      this.result = 'data:image/webp;base64,dGVzdA=='
      this.onload?.()
    }
  }
  vi.stubGlobal('FileReader', FileReaderStub)
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
  useAppearanceQuery.mockReturnValue({ data: defaultConfig(), isLoading: false, isFetching: false })
  convertImageToWebp.mockImplementation(async (file) => file)
})

afterEach(() => {
  useAppearanceQuery.mockReset()
  updateAppearance.mockReset()
  convertImageToWebp.mockReset()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

function defaultConfig(): AppearanceConfig {
  return { background_image: '', custom_background_images: [], hidden_menu_items: [], home_page: 'dashboard' as const }
}

const storedImage = '/api/v1/appearance/backgrounds/appearance-bg-0123456789abcdef.png'

describe('appearance settings page', () => {
  it('切换选中项后保存调用 PUT，成功后刷新并 toast', async () => {
    useAppearanceQuery.mockReturnValue({
      data: { background_image: storedImage, custom_background_images: [storedImage], hidden_menu_items: [], home_page: 'dashboard' },
      isLoading: false,
      isFetching: false,
    })
    updateAppearance.mockResolvedValue(defaultConfig())
    const mounted = await mountPage()
    try {
      // 初始：自定义图选中
      expect(mounted.container.textContent).toContain('自定义 1')
      const selected = findByClass(mounted.container, 'border-tint')
      expect(selected?.textContent).toContain('自定义 1')

      // 切换到「无（星点背景）」
      await click(findButton(mounted.container, '无（星点背景）'))
      const selectedAfter = findByClass(mounted.container, 'border-tint')
      expect(selectedAfter?.textContent).toContain('无（星点背景）')

      // 保存：PUT 带上切换后的草稿
      await click(findButton(mounted.container, '保存'))
      expect(updateAppearance).toHaveBeenCalledOnce()
      expect(updateAppearance.mock.calls[0][0]).toEqual({ background_image: '', custom_background_images: [storedImage], hidden_menu_items: [], home_page: 'dashboard' })
      expect(toast.success).toHaveBeenCalledWith('外观设置已保存')
    } finally {
      await mounted.unmount()
    }
  })

  it('原始图片超过 32MB 时被拒绝，不进行转码', async () => {
    const mounted = await mountPage()
    try {
      const input = findFileInput(mounted.container)
      expect(input).not.toBeNull()
      ;(input as MiniNode & { files?: Array<{ size: number }> }).files = [
        { size: 32 * 1024 * 1024 + 1 },
      ]
      await dispatch(input!, 'change')
      expect(toast.error).toHaveBeenCalledWith('图片不能超过 8MB')
      expect(convertImageToWebp).not.toHaveBeenCalled()
      expect(mounted.container.textContent).not.toContain('自定义 1')
    } finally {
      await mounted.unmount()
    }
  })

  it('原始图片超过 8MB 但转码后不超限时可以上传', async () => {
    const mounted = await mountPage()
    try {
      const original = { size: 9 * 1024 * 1024 }
      const converted = { size: 1024 }
      convertImageToWebp.mockResolvedValue(converted)
      const input = findFileInput(mounted.container)
      ;(input as MiniNode & { files?: Array<{ size: number }> }).files = [original]
      await dispatch(input!, 'change')
      expect(convertImageToWebp).toHaveBeenCalledWith(original)
      expect(toast.error).not.toHaveBeenCalled()
      expect(mounted.container.textContent).toContain('自定义 1')
    } finally {
      await mounted.unmount()
    }
  })

  it('转码后的图片仍超过 8MB 时被拒绝，不进图库', async () => {
    const mounted = await mountPage()
    try {
      const original = { size: 4 * 1024 * 1024 }
      convertImageToWebp.mockResolvedValue({ size: 8 * 1024 * 1024 + 1 })
      const input = findFileInput(mounted.container)
      ;(input as MiniNode & { files?: Array<{ size: number }> }).files = [original]
      await dispatch(input!, 'change')
      expect(toast.error).toHaveBeenCalledWith('图片不能超过 8MB')
      expect(updateAppearance).not.toHaveBeenCalled()
      expect(mounted.container.textContent).not.toContain('自定义 1')
    } finally {
      await mounted.unmount()
    }
  })

  it('重置丢弃草稿，恢复为已保存的配置', async () => {
    useAppearanceQuery.mockReturnValue({
      data: { background_image: storedImage, custom_background_images: [storedImage], hidden_menu_items: [], home_page: 'dashboard' },
      isLoading: false,
      isFetching: false,
    })
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '无（星点背景）'))
      expect(findByClass(mounted.container, 'border-tint')?.textContent).toContain('无（星点背景）')
      await click(findButton(mounted.container, '重置'))
      expect(findByClass(mounted.container, 'border-tint')?.textContent).toContain('自定义 1')
      expect(updateAppearance).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('切换到菜单设置 tab，关闭开关后保存包含 hidden_menu_items', async () => {
    updateAppearance.mockResolvedValue(defaultConfig())
    const mounted = await mountPage()
    try {
      // 切到菜单设置 tab（Radix Tabs 由 mousedown 触发）
      await click(findButton(mounted.container, '菜单设置'))
      expect(mounted.container.textContent).toContain('打开程序时默认进入的页面')

      // 设置菜单不可隐藏：开关被禁用
      const disabledSwitch = findSwitchByLabel(mounted.container, '设置')
      expect(disabledSwitch?.hasAttribute('disabled')).toBe(true)

      // 关掉「用量分析」开关
      await click(findSwitchByLabel(mounted.container, '用量分析'))
      await click(findButton(mounted.container, '保存'))
      expect(updateAppearance).toHaveBeenCalledOnce()
      expect(updateAppearance.mock.calls[0][0]).toMatchObject({
        hidden_menu_items: ['/analytics'],
        home_page: 'dashboard',
      })
    } finally {
      await mounted.unmount()
    }
  })

  it('选择首页为 Agent 后保存包含 home_page', async () => {
    updateAppearance.mockResolvedValue(defaultConfig())
    const mounted = await mountPage()
    try {
      await click(findButton(mounted.container, '菜单设置'))
      await click(findRadioByLabel(mounted.container, 'Agent'))
      await click(findButton(mounted.container, '保存'))
      expect(updateAppearance).toHaveBeenCalledOnce()
      expect(updateAppearance.mock.calls[0][0]).toMatchObject({
        hidden_menu_items: [],
        home_page: 'agent',
      })
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

async function click(node: MiniNode | null) {
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
            <AppearanceSettingsPage />
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

function findButton(node: MiniNode | null, label: string): MiniNode | null {
  if (!node) return null
  if (node.tagName === 'BUTTON' && node.textContent.trim() === label) return node
  for (const child of node.childNodes) {
    const found = findButton(child, label)
    if (found) return found
  }
  return null
}

function findFileInput(node: MiniNode | null): MiniNode | null {
  if (!node) return null
  if (node.tagName === 'INPUT') {
    const type = node.getAttribute('type') ?? (node as MiniNode & { type?: string }).type
    if (type === 'file') return node
  }
  for (const child of node.childNodes) {
    const found = findFileInput(child)
    if (found) return found
  }
  return null
}

function findByClass(node: MiniNode | null, className: string): MiniNode | null {
  if (!node) return null
  if (node.attributes.class?.split(/\s+/).includes(className)) return node
  for (const child of node.childNodes) {
    const found = findByClass(child, className)
    if (found) return found
  }
  return null
}

function findSwitchByLabel(node: MiniNode, label: string): MiniNode | null {
  return findButtonByLabelAndTag(node, label, 'BUTTON')
}

function findRadioByLabel(node: MiniNode, label: string): MiniNode | null {
  return findButtonByLabelAndTag(node, label, 'BUTTON')
}

function findButtonByLabelAndTag(node: MiniNode | null, label: string, tag: string): MiniNode | null {
  if (!node) return null
  if (node.tagName === tag && node.textContent.trim() === label) return node
  for (const child of node.childNodes) {
    const found = findButtonByLabelAndTag(child, label, tag)
    if (found) return found
  }
  return null
}
