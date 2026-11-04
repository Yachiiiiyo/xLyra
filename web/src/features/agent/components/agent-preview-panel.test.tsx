import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentPreviewPanel } from '@/features/agent/components/agent-preview-panel'
import type { AgentFileChange } from '@/features/agent/lib/run-changes'
import zhAgent from '@/locales/zh/agent.json'

vi.mock('@/components/glass/liquid-glass', () => ({
  LiquidGlass: ({ as: Tag = 'div', variant: _variant, children, ...props }: React.ComponentPropsWithoutRef<'div'> & { as?: 'div' | 'aside'; variant?: string }) => {
    void _variant
    return <Tag {...props}>{children}</Tag>
  },
}))
vi.mock('@/components/ui/button', () => ({
  Button: React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement>>(function Button({ children, ...props }, ref) {
    return <button ref={ref} type="button" {...props}>{children}</button>
  }),
}))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

const patch = '--- edit.txt\n+++ edit.txt\n@@ -1 +1 @@\n-old\n+new\n'
const changes: AgentFileChange[] = [
  { path: 'write.txt', kind: 'write', additions: 2, deletions: 1, patches: [patch], content: 'alpha\nbeta' },
  { path: 'edit.txt', kind: 'edit', additions: 1, deletions: 1, patches: [patch] },
]

describe('AgentPreviewPanel 桌面版', () => {
  it('按文件类型选择默认标签，切换文件重置标签，Esc 关闭', async () => {
    const mounted = await mountPanel()
    try {
      expect(selectedTab(mounted.container)).toBe(zhAgent.preview.tabSource)
      expect(mounted.container.textContent).toContain('alpha')
      await act(async () => { tab(mounted.container, zhAgent.preview.tabDiff).dispatchEvent(inputEvent('click')) })
      expect(selectedTab(mounted.container)).toBe(zhAgent.preview.tabDiff)
      const select = findAll(mounted.container, (node) => node.tagName === 'SELECT')[0]!
      await act(async () => {
        select.value = 'edit.txt'
        select.dispatchEvent(inputEvent('change'))
      })
      expect(mounted.path).toBe('edit.txt')
      expect(selectedTab(mounted.container)).toBe(zhAgent.preview.tabDiff)
      await act(async () => { mounted.keydown?.({ key: 'Escape' }) })
      expect(mounted.onClose).toHaveBeenCalledOnce()
    } finally {
      await mounted.unmount()
    }
  })
})

function selectedTab(container: MiniNode) {
  return findAll(container, (node) => node.getAttribute('role') === 'tab' && node.getAttribute('aria-selected') === 'true')[0]?.textContent
}

function tab(container: MiniNode, label: string) {
  return findAll(container, (node) => node.getAttribute('role') === 'tab' && node.textContent === label)[0]!
}

async function mountPanel() {
  const doc = new MiniDocument()
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as { window: unknown; document: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean }
  const previous = { window: globals.window, document: globals.document }
  let keydown: ((event: { key: string }) => void) | null = null
  globals.window = {
    document: doc,
    HTMLIFrameElement: class {},
    addEventListener(type: string, listener: (event: { key: string }) => void) { if (type === 'keydown') keydown = listener },
    removeEventListener(type: string) { if (type === 'keydown') keydown = null },
  }
  doc.defaultView = globals.window
  globals.document = doc
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const onClose = vi.fn()
  let path = 'write.txt'
  const { createRoot } = await import('react-dom/client')
  const root = createRoot(container as unknown as Element)
  const render = () => root.render(
    <I18nextProvider i18n={i18n}>
      <AgentPreviewPanel changes={changes} path={path} onSelectPath={(next) => { path = next; render() }} onClose={onClose} mobile={false} />
    </I18nextProvider>,
  )
  await act(async () => { render() })
  return {
    container,
    onClose,
    get path() { return path },
    get keydown() { return keydown },
    async unmount() {
      await act(async () => { root.unmount() })
      globals.window = previous.window
      globals.document = previous.document
    },
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
  stopped: boolean
  preventDefault: () => void
  stopPropagation: () => void
}
class MiniNode {
  nodeType = 1
  nodeName: string
  tagName: string
  namespaceURI = 'http://www.w3.org/1999/xhtml'
  ownerDocument: MiniDocument
  parentNode: MiniNode | null = null
  childNodes: MiniNode[] = []
  nodeValue: string | null = null
  attributes: Record<string, string> = {}
  value = ''
  style = { setProperty: () => {} }
  constructor(tag: string, ownerDocument: MiniDocument) {
    this.tagName = tag.toUpperCase()
    this.nodeName = this.tagName
    this.ownerDocument = ownerDocument
  }
  get firstChild() { return this.childNodes[0] ?? null }
  get options() { return this.childNodes }
  get lastChild() { return this.childNodes[this.childNodes.length - 1] ?? null }
  get nextSibling(): MiniNode | null {
    if (!this.parentNode) return null
    return this.parentNode.childNodes[this.parentNode.childNodes.indexOf(this) + 1] ?? null
  }
  get textContent(): string {
    if (this.nodeType === 3 || this.nodeType === 8) return this.nodeValue ?? ''
    return this.childNodes.map((child) => child.textContent).join('')
  }
  set textContent(value: string) {
    this.childNodes = []
    if (value) this.appendChild(this.ownerDocument.createTextNode(value))
  }
  setAttribute(name: string, value: string) { this.attributes[name] = String(value) }
  getAttribute(name: string) { return this.attributes[name] ?? null }
  removeAttribute(name: string) { delete this.attributes[name] }
  hasAttribute(name: string) { return Object.prototype.hasOwnProperty.call(this.attributes, name) }
  setAttributeNS(_namespace: string, name: string, value: string) { this.setAttribute(name, value) }
  appendChild<T extends MiniNode>(child: T) { return this.insertBefore(child, null) }
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
  addEventListener(type: string, listener: MiniListener) { trackListener(this, type, listener) }
  removeEventListener(type: string, listener: MiniListener) { miniListeners.get(this)?.get(type)?.delete(listener) }
  dispatchEvent(event: MiniEvent) {
    event.target = this
    const chain: Array<MiniNode | MiniDocument> = [this]
    let parent = this.parentNode
    while (parent) { chain.push(parent); parent = parent.parentNode }
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
  defaultView: unknown = globalThis
  activeElement = null
  constructor() {
    this.documentElement = new MiniNode('html', this)
    this.body = new MiniNode('body', this)
    this.documentElement.appendChild(this.body)
  }
  createElement(tag: string) {
    const node = new MiniNode(tag, this)
    if (tag === '#text') { node.nodeType = 3; node.nodeName = '#text'; node.tagName = '#text' }
    return node
  }
  createElementNS(_namespace: string, tag: string) { return this.createElement(tag) }
  createTextNode(text: string) { const node = this.createElement('#text'); node.nodeValue = text; return node }
  addEventListener(type: string, listener: MiniListener) { trackListener(this, type, listener) }
  removeEventListener(type: string, listener: MiniListener) { miniListeners.get(this)?.get(type)?.delete(listener) }
}
function inputEvent(type: string): MiniEvent {
  return {
    type, target: null, currentTarget: null, bubbles: true, cancelable: true, defaultPrevented: false,
    button: 0, ctrlKey: false, eventPhase: 2, timeStamp: 0, stopped: false,
    preventDefault() { this.defaultPrevented = true }, stopPropagation() { this.stopped = true },
  }
}

function findAll(node: MiniNode, predicate: (node: MiniNode) => boolean): MiniNode[] {
  return [...(predicate(node) ? [node] : []), ...node.childNodes.flatMap((child) => findAll(child, predicate))]
}
