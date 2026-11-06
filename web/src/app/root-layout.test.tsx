import { createInstance } from 'i18next'
import { act, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeAll, afterEach, describe, expect, it, vi } from 'vitest'
import { RootLayout } from '@/app/root-layout'
import zhComponents from '@/locales/zh/components.json'
import { useAuthStore } from '@/stores/auth-store'

vi.mock('@/app/use-agent-prefetch', () => ({ useAgentPrefetch: vi.fn() }))

vi.mock('@/components/auth/auth-guard', async () => {
  const React = await import('react')
  return {
    ProtectedRoute: ({ children }: { children?: ReactNode }) => React.createElement(React.Fragment, null, children),
  }
})

vi.mock('@/components/layout/app-shell', async () => {
  const React = await import('react')
  return {
    AppShell: ({ children }: { children?: ReactNode }) =>
      React.createElement('div', { 'data-app-shell': 'true' }, children),
  }
})

vi.mock('@/components/layout/ambient-background', async () => {
  const React = await import('react')
  return {
    AmbientBackground: () => React.createElement('div', { 'data-ambient-background': 'true' }),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { components: zhComponents } },
    interpolation: { escapeValue: false },
  })
})

const initialAuthState = useAuthStore.getState()

afterEach(() => {
  useAuthStore.setState(initialAuthState, true)
  vi.unstubAllGlobals()
})

describe('RootLayout error 状态', () => {
  it('整页渲染全屏错误页，不渲染 AppShell', async () => {
    useAuthStore.setState({ status: 'error', errorMessage: '网关超时 status 503', errorStatus: null })
    const mounted = await mountLayout()
    try {
      const { container } = mounted
      expect(findByClass(container, 'app-viewport')).not.toBeNull()
      expect(findAttr(container, 'data-ambient-background')).not.toBeNull()
      expect(findAttr(container, 'data-app-shell')).toBeNull()
      expect(findAttr(container, 'data-child-route')).toBeNull()
      expect(container.textContent).toContain('503')
      expect(container.textContent).toContain('HTTP 503')
      expect(container.textContent).toContain('后端服务不可用')
      expect(container.textContent).toContain('后端服务暂时异常')
      expect(findButton(container, '刷新页面')).not.toBeNull()
    } finally {
      await mounted.unmount()
    }
  })

  it('无状态码时回退到 502', async () => {
    useAuthStore.setState({ status: 'error', errorMessage: null, errorStatus: null })
    const mounted = await mountLayout()
    try {
      expect(mounted.container.textContent).toContain('502')
      expect(mounted.container.textContent).toContain('HTTP 502')
    } finally {
      await mounted.unmount()
    }
  })

  it('非 error 状态仍渲染 AppShell 与子路由', async () => {
    useAuthStore.setState({ status: 'authenticated' })
    const mounted = await mountLayout()
    try {
      expect(findAttr(mounted.container, 'data-app-shell')).not.toBeNull()
      expect(findAttr(mounted.container, 'data-child-route')).not.toBeNull()
    } finally {
      await mounted.unmount()
    }
  })

  it('点击「刷新页面」调用 window.location.reload', async () => {
    useAuthStore.setState({ status: 'error', errorMessage: null, errorStatus: null })
    const reload = vi.fn()
    vi.stubGlobal('location', { reload })
    const mounted = await mountLayout()
    try {
      const button = findButton(mounted.container, '刷新页面')
      expect(button).not.toBeNull()
      await click(button)
      expect(reload).toHaveBeenCalledOnce()
    } finally {
      await mounted.unmount()
    }
  })
})

async function mountLayout() {
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
  try {
    const { createRoot } = await import('react-dom/client')
    const root = createRoot(container as unknown as Element)
    await act(async () => {
      root.render(
        <I18nextProvider i18n={i18n}>
          <MemoryRouter initialEntries={['/']}>
            <Routes>
              <Route element={<RootLayout />}>
                <Route path="/" element={<div data-child-route="true" />} />
              </Route>
            </Routes>
          </MemoryRouter>
        </I18nextProvider>,
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

async function flushUpdates() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function click(node: MiniNode | null) {
  await act(async () => {
    node?.dispatchEvent(clickEvent(node!, 'mousedown'))
    node?.dispatchEvent(clickEvent(node!, 'click'))
  })
  await flushUpdates()
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

function clickEvent(target: MiniNode, type: string): MiniEvent {
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

function findAttr(node: MiniNode | null, name: string): MiniNode | null {
  if (!node) return null
  if (node.hasAttribute(name)) return node
  for (const child of node.childNodes) {
    const found = findAttr(child, name)
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
