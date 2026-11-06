import { act, type ReactNode } from 'react'

type MiniListener = (event: MiniEvent) => void
const listeners = new WeakMap<object, Map<string, Set<MiniListener>>>()

function trackListener(target: object, type: string, listener: MiniListener) {
  const byType = listeners.get(target) ?? new Map<string, Set<MiniListener>>()
  const set = byType.get(type) ?? new Set<MiniListener>()
  set.add(listener)
  byType.set(type, set)
  listeners.set(target, byType)
}

export interface MiniEvent {
  type: string
  target: MiniNode | null
  currentTarget: MiniNode | MiniDocument | null
  bubbles: boolean
  cancelable: boolean
  defaultPrevented: boolean
  button: number
  ctrlKey: boolean
  metaKey: boolean
  shiftKey: boolean
  altKey: boolean
  key?: string
  isComposing?: boolean
  eventPhase: number
  timeStamp: number
  stopped: boolean
  preventDefault(): void
  stopPropagation(): void
}

export class MiniNode {
  nodeType = 1
  nodeName: string
  tagName: string
  namespaceURI = 'http://www.w3.org/1999/xhtml'
  ownerDocument: MiniDocument
  parentNode: MiniNode | null = null
  childNodes: MiniNode[] = []
  nodeValue: string | null = null
  attributes: Record<string, string> = {}
  declare value: string
  selectionStart = 0
  selectionEnd = 0
  scrollHeight = 24
  style = { setProperty: () => {} }

  constructor(tag: string, ownerDocument: MiniDocument) {
    this.tagName = tag.toUpperCase()
    this.nodeName = this.tagName
    this.ownerDocument = ownerDocument
  }

  get firstChild() { return this.childNodes[0] ?? null }
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
  removeEventListener(type: string, listener: MiniListener) { listeners.get(this)?.get(type)?.delete(listener) }
  attachEvent(type: string, listener: MiniListener) { this.addEventListener(type.replace(/^on/, ''), listener) }
  detachEvent(type: string, listener: MiniListener) { this.removeEventListener(type.replace(/^on/, ''), listener) }
  focus() { this.ownerDocument.activeElement = this }
  blur() { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null }
  dispatchEvent(event: MiniEvent) {
    event.target = this
    const chain: Array<MiniNode | MiniDocument> = [this]
    let parent = this.parentNode
    while (parent) {
      chain.push(parent)
      parent = parent.parentNode
    }
    chain.push(this.ownerDocument)
    for (const current of chain) {
      event.currentTarget = current
      for (const listener of listeners.get(current)?.get(event.type) ?? []) listener.call(current, event)
      if (event.stopped) break
    }
    return !event.defaultPrevented
  }
}

// React 的受控 value 依赖原型描述符安装 value tracker，实例字段会挡住它。
Object.defineProperty(MiniNode.prototype, 'value', {
  configurable: true,
  get(this: MiniNode) { return this.getAttribute('value') ?? '' },
  set(this: MiniNode, value: string) { this.setAttribute('value', value) },
})

export class MiniDocument {
  nodeType = 9
  nodeName = '#document'
  documentElement: MiniNode
  body: MiniNode
  defaultView: unknown = globalThis
  activeElement: MiniNode | null = null
  oninput = null

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
  createElementNS(_namespace: string, tag: string) { return this.createElement(tag) }
  createTextNode(text: string) {
    const node = this.createElement('#text')
    node.nodeValue = text
    return node
  }
  addEventListener(type: string, listener: MiniListener) { trackListener(this, type, listener) }
  removeEventListener(type: string, listener: MiniListener) { listeners.get(this)?.get(type)?.delete(listener) }
}

export function miniEvent(type: string, init: Partial<MiniEvent> = {}): MiniEvent {
  return {
    type,
    target: null,
    currentTarget: null,
    bubbles: true,
    cancelable: true,
    defaultPrevented: false,
    button: 0,
    ctrlKey: false,
    metaKey: false,
    shiftKey: false,
    altKey: false,
    eventPhase: 2,
    timeStamp: 0,
    stopped: false,
    preventDefault() { this.defaultPrevented = true },
    stopPropagation() { this.stopped = true },
    ...init,
  }
}

export function findMiniNodes(node: MiniNode, predicate: (node: MiniNode) => boolean): MiniNode[] {
  return [...(predicate(node) ? [node] : []), ...node.childNodes.flatMap((child) => findMiniNodes(child, predicate))]
}

export async function mountMini(element: ReactNode) {
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
    act: globals.IS_REACT_ACT_ENVIRONMENT,
  }
  globals.window = globalThis
  globals.document = doc
  globals.HTMLIFrameElement = class {}
  globals.IS_REACT_ACT_ENVIRONMENT = true
  doc.defaultView = globals.window
  const { createRoot } = await import('react-dom/client')
  const root = createRoot(container as unknown as Element)
  const render = async (next: ReactNode) => {
    await act(async () => { root.render(next) })
  }
  const unmount = async () => {
    await act(async () => { root.unmount() })
    globals.window = previous.window
    globals.document = previous.document
    globals.HTMLIFrameElement = previous.HTMLIFrameElement
    globals.IS_REACT_ACT_ENVIRONMENT = previous.act
  }
  await render(element)
  return { container, render, unmount }
}
