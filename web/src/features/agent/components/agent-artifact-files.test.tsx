import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentArtifactFiles } from '@/features/agent/components/agent-artifact-files'
import type { AgentArtifactFile, AgentArtifacts } from '@/features/agent/api/agent'
import zhAgent from '@/locales/zh/agent.json'

const downloadSessionFileMock = vi.hoisted(() => vi.fn())
vi.mock('@/features/agent/api/agent', () => ({
  downloadSessionFile: downloadSessionFileMock,
  fetchSessionFile: vi.fn(),
}))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

function file(overrides: Partial<AgentArtifactFile>): AgentArtifactFile {
  return { path: 'a.txt', size: 10, mtime: new Date(Date.now() - 3_600_000).toISOString(), kind: 'text', agent_generated: false, ...overrides }
}

const artifacts: AgentArtifacts = {
  truncated: false,
  files: [
    file({ path: 'user/notes.md', size: 512 }),
    file({ path: 'images/chart.png', size: 2048, kind: 'image', agent_generated: true }),
    file({ path: 'out.bin', size: 1024 * 1024, kind: 'binary' }),
  ],
}

function render(node: React.ReactNode) {
  return renderToStaticMarkup(<I18nextProvider i18n={i18n}>{node}</I18nextProvider>)
}

describe('AgentArtifactFiles 渲染', () => {
  it('平铺列出文件:Agent 产生的在前,带大小', () => {
    const html = render(<AgentArtifactFiles sessionId="session-1" artifacts={artifacts} onPreview={() => {}} />)
    expect(html.indexOf('images/chart.png')).toBeLessThan(html.indexOf('user/notes.md'))
    expect(html).toContain('out.bin')
    expect(html).toContain('2 KB')
    expect(html).toContain('1 MB')
    expect(html).toContain(zhAgent.files.preview)
    expect(html).toContain(zhAgent.files.download)
  })

  it('空列表显示占位,truncated 时底部提示', () => {
    const empty = render(<AgentArtifactFiles sessionId="session-1" artifacts={{ files: [], truncated: false }} onPreview={() => {}} />)
    expect(empty).toContain(zhAgent.files.empty)
    const truncated = render(<AgentArtifactFiles sessionId="session-1" artifacts={{ files: artifacts.files, truncated: true }} onPreview={() => {}} />)
    expect(truncated).toContain(zhAgent.files.truncated)
  })
})

describe('AgentArtifactFiles 按钮回调', () => {
  it('「预览」回调路径,行内「下载」调用对应接口', async () => {
    const { createRoot } = await import('react-dom/client')
    const doc = new MiniDocument()
    const container = doc.createElement('div')
    doc.body.appendChild(container)
    const globals = globalThis as unknown as { window: unknown; document: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean }
    const previous = { window: globals.window, document: globals.document }
    globals.window = {
      document: doc,
      HTMLIFrameElement: class {},
      addEventListener() {},
      removeEventListener() {},
    }
    doc.defaultView = globals.window
    globals.document = doc
    globals.IS_REACT_ACT_ENVIRONMENT = true
    const onPreview = vi.fn()
    const root = createRoot(container as unknown as Element)
    try {
      await act(async () => {
        root.render(
          <I18nextProvider i18n={i18n}>
            <AgentArtifactFiles sessionId="session-1" artifacts={artifacts} onPreview={onPreview} />
          </I18nextProvider>,
        )
      })
      const chartRow = findAll(container, (node) => node.tagName === 'LI' && (node.textContent?.includes('images/chart.png') ?? false))[0]
      expect(chartRow).toBeTruthy()
      const previewButton = findAll(chartRow!, (node) => node.tagName === 'BUTTON' && node.textContent === zhAgent.files.preview)[0]
      expect(previewButton).toBeTruthy()
      await act(async () => { previewButton!.dispatchEvent(inputEvent('click')) })
      expect(onPreview).toHaveBeenCalledWith('images/chart.png')
      const rowDownload = findAll(chartRow!, (node) => node.tagName === 'BUTTON' && node.textContent === zhAgent.files.download)[0]
      expect(rowDownload).toBeTruthy()
      await act(async () => { rowDownload!.dispatchEvent(inputEvent('click')) })
      expect(downloadSessionFileMock).toHaveBeenCalledWith('session-1', 'images/chart.png')
    } finally {
      await act(async () => { root.unmount() })
      globals.window = previous.window
      globals.document = previous.document
    }
  })
})

describe('AgentArtifactFiles 展开', () => {
  it('同时只展开一个文件,展开的文件行吸附在顶部', async () => {
    const { createRoot } = await import('react-dom/client')
    const { QueryClient, QueryClientProvider } = await import('@tanstack/react-query')
    const doc = new MiniDocument()
    const container = doc.createElement('div')
    doc.body.appendChild(container)
    const globals = globalThis as unknown as { window: unknown; document: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean }
    const previous = { window: globals.window, document: globals.document }
    globals.window = { document: doc, HTMLIFrameElement: class {}, addEventListener() {}, removeEventListener() {} }
    doc.defaultView = globals.window
    globals.document = doc
    globals.IS_REACT_ACT_ENVIRONMENT = true
    const binaries: AgentArtifacts = { truncated: false, files: [file({ path: 'a.bin', kind: 'binary' }), file({ path: 'b.bin', kind: 'binary' })] }
    const root = createRoot(container as unknown as Element)
    try {
      await act(async () => {
        root.render(
          <QueryClientProvider client={new QueryClient()}>
            <I18nextProvider i18n={i18n}>
              <AgentArtifactFiles sessionId="session-1" artifacts={binaries} onPreview={() => {}} />
            </I18nextProvider>
          </QueryClientProvider>,
        )
      })
      const toggle = (name: string) => findAll(container, (node) => node.tagName === 'BUTTON' && node.getAttribute('aria-expanded') !== null && (node.textContent?.includes(name) ?? false))[0]!
      const expanded = () => findAll(container, (node) => node.getAttribute('aria-expanded') === 'true').map((node) => node.textContent)
      await act(async () => { toggle('a.bin').dispatchEvent(inputEvent('click')) })
      expect(expanded()).toHaveLength(1)
      expect(expanded()[0]).toContain('a.bin')
      await act(async () => { toggle('b.bin').dispatchEvent(inputEvent('click')) })
      expect(expanded()).toHaveLength(1)
      expect(expanded()[0]).toContain('b.bin')
      await act(async () => { toggle('b.bin').dispatchEvent(inputEvent('click')) })
      expect(expanded()).toHaveLength(0)
    } finally {
      await act(async () => { root.unmount() })
      globals.window = previous.window
      globals.document = previous.document
    }
  })
})

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
  preventDefault(): void
  stopPropagation(): void
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

describe('AgentArtifactFiles 行样式', () => {
  it('下载按钮不带图标,行内文字统一 text-xs', () => {
    const html = render(<AgentArtifactFiles sessionId="session-1" artifacts={artifacts} onPreview={() => {}} />)
    expect(html).not.toMatch(/<button[^>]*>\s*<svg[^>]*>(?:(?!<\/button>).)*?<\/svg>\s*下载/)
    expect(html).not.toContain('text-[11px]')
    expect(html).not.toContain('text-[12px]')
  })
})
