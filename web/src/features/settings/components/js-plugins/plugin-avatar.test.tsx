import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { act, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { jsPluginQueryKeys, type JSPluginListItem } from '@/features/settings/api/js-plugins'
import { JSPluginsSettingsWorkspace } from '@/features/settings/components/js-plugins/js-plugins-settings-workspace'
import zh from '@/locales/zh/settings.json'
import { PluginAvatar } from './plugin-avatar'

const layout = vi.hoisted(() => ({ mobile: false }))

vi.mock('@/hooks/use-media-query', () => ({
  useMobileLayout: () => layout.mobile,
  useMobileDevice: () => false,
  detectMobileDevice: () => false,
}))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { settings: zh } },
    interpolation: { escapeValue: false },
  })
})

const iconUrl = '/api/v1/plugin-icons/acme.balance?v=0123456789abcdef'

function render(node: ReactNode) {
  return renderToStaticMarkup(node)
}

describe('PluginAvatar', () => {
  it('renders the icon url unchanged', () => {
    const markup = render(<PluginAvatar iconUrl={iconUrl} name="balance" />)
    expect(markup).toContain(`src="${iconUrl}"`)
    expect(markup).toContain('alt=""')
    expect(markup).toContain('loading="lazy"')
    expect(markup).toContain('decoding="async"')
    expect(markup).toContain('draggable="false"')
    expect(markup).toContain('referrerPolicy="no-referrer"')
    expect(markup).toContain('object-cover')
    expect(markup).toContain('bg-elevated-1')
    expect(markup).toContain('rounded-xl')
    expect(markup).toContain('ring-white/10')
    expect(markup).not.toContain('>B</span>')
  })

  it('renders the first letter when there is no icon', () => {
    const markup = render(<PluginAvatar name="balance" />)
    expect(markup).toContain('>B</span>')
    expect(markup).toContain('rounded-xl')
    expect(markup).toContain('bg-elevated-1')
    expect(markup).toContain('ring-1')
    expect(markup).toContain('ring-inset')
    expect(markup).toContain('ring-white/10')
    expect(markup).toContain('text-label')
    expect(markup).not.toContain('rounded-full')
    expect(markup).not.toContain('bg-fill-3')
    const empty = render(<PluginAvatar iconUrl="" name="balance" />)
    expect(empty).toContain('>B</span>')
    expect(empty).toContain('rounded-xl')
    expect(empty).not.toContain('<img')
    expect(empty).not.toContain('rounded-full')
  })

  it('renders ? when the name is empty', () => {
    expect(render(<PluginAvatar name="" />)).toContain('>?</span>')
    expect(render(<PluginAvatar name="   " />)).toContain('>?</span>')
  })

  it('uses 32, 40, and 48 pixel boxes', () => {
    expect(render(<PluginAvatar name="A" size="sm" />)).toContain('h-8 w-8')
    expect(render(<PluginAvatar name="A" />)).toContain('h-10 w-10')
    expect(render(<PluginAvatar iconUrl={iconUrl} name="A" size="lg" />)).toContain('h-12 w-12')
  })

  it('falls back after an error and tries again when the url changes', async () => {
    const mounted = await mount(<PluginAvatar iconUrl={iconUrl} name="balance" size="sm" />)
    try {
      const img = findTag(mounted.container, 'IMG')
      expect(img?.getAttribute('src')).toBe(iconUrl)
      expect(img?.getAttribute('alt')).toBe('')
      if (!img) return
      await act(async () => {
        img.dispatchEvent(errorEvent(img))
      })
      expect(findTag(mounted.container, 'IMG')).toBeNull()
      expect(mounted.container.textContent).toBe('B')

      const next = '/api/v1/plugin-icons/acme.balance?v=ffffffffffffffff'
      await mounted.render(<PluginAvatar iconUrl={next} name="balance" size="sm" />)
      const retried = findTag(mounted.container, 'IMG')
      expect(retried?.getAttribute('src')).toBe(next)
      expect(mounted.container.textContent).toBe('')
    } finally {
      await mounted.unmount()
    }
  })
})

function uploadedPlugin(fields: Partial<JSPluginListItem> & Pick<JSPluginListItem, 'id' | 'name'>): JSPluginListItem {
  return {
    source: 'upload',
    enabled_version: '',
    version_count: 1,
    kind: 'quota_probe',
    ...fields,
  }
}

const uploaded = [
  uploadedPlugin({
    id: 'acme.balance',
    name: 'Balance',
    icon_url: iconUrl,
  }),
  uploadedPlugin({
    id: 'acme.plain',
    name: '石英',
    icon_url: '',
  }),
]

function renderInstalled() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(jsPluginQueryKeys.uploaded(), uploaded)
  client.setQueryData(jsPluginQueryKeys.trustedKeys(), [])
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <MemoryRouter>
          <JSPluginsSettingsWorkspace />
        </MemoryRouter>
      </I18nextProvider>
    </QueryClientProvider>,
  )
}

describe('installed plugin list', () => {
  it('shows an image for a plugin with an icon and a letter without one', () => {
    layout.mobile = false
    const markup = renderInstalled()
    expect(markup).toContain('grid-cols-2')
    expect(markup.match(/<img /g)).toHaveLength(1)
    expect(markup).toContain(`src="${iconUrl}"`)
    expect(markup).toContain('>石</span>')
    expect(markup).toContain('Balance')
    expect(markup).toContain('acme.plain')
  })

  it('shows the same avatars on the phone cards', () => {
    layout.mobile = true
    const markup = renderInstalled()
    expect(markup).toContain('space-y-3')
    expect(markup).not.toContain('grid-cols-2')
    expect(markup.match(/<img /g)).toHaveLength(1)
    expect(markup).toContain(`src="${iconUrl}"`)
    expect(markup).toContain('h-10 w-10')
    expect(markup).toContain('>石</span>')
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

function untrackListener(target: object, type: string, listener: MiniListener) {
  miniListeners.get(target)?.get(type)?.delete(listener)
}

function emitListeners(target: object, event: MiniEvent) {
  for (const listener of miniListeners.get(target)?.get(event.type) ?? []) listener(event)
}

interface MiniEvent {
  type: string
  target: MiniNode | null
  currentTarget: MiniNode | null
  bubbles: boolean
  cancelable: boolean
  defaultPrevented: boolean
  eventPhase: number
  timeStamp: number
  preventDefault: () => void
  stopPropagation: () => void
}

class MiniNode {
  nodeType = 1
  nodeName: string
  tagName: string
  namespaceURI: string | null = 'http://www.w3.org/1999/xhtml'
  ownerDocument: MiniDocument
  parentNode: MiniNode | null = null
  childNodes: MiniNode[] = []
  nodeValue: string | null = null
  attributes: Record<string, string> = {}
  style: { setProperty: (name: string, value: string) => void } = { setProperty: () => {} }

  constructor(tag: string, ownerDocument: MiniDocument) {
    this.tagName = tag.toUpperCase()
    this.nodeName = this.tagName
    this.ownerDocument = ownerDocument
  }

  get firstChild(): MiniNode | null {
    return this.childNodes[0] ?? null
  }

  get lastChild(): MiniNode | null {
    return this.childNodes[this.childNodes.length - 1] ?? null
  }

  get nextSibling(): MiniNode | null {
    if (!this.parentNode) return null
    const index = this.parentNode.childNodes.indexOf(this)
    return this.parentNode.childNodes[index + 1] ?? null
  }

  get textContent(): string {
    if (this.nodeType === 3) return this.nodeValue ?? ''
    return this.childNodes.map((child) => child.textContent).join('')
  }

  set textContent(value: string) {
    this.childNodes = []
    if (value) this.appendChild(this.ownerDocument.createTextNode(value))
  }

  get className() {
    return this.attributes.class ?? ''
  }

  set className(value: string) {
    this.attributes.class = String(value)
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

  setAttributeNS(_namespace: string, name: string, value: string) {
    this.setAttribute(name, value)
  }

  appendChild<T extends MiniNode>(child: T): T {
    this.insertBefore(child, null)
    return child
  }

  insertBefore<T extends MiniNode>(child: T, before: MiniNode | null): T {
    if (child.parentNode) child.parentNode.removeChild(child)
    child.parentNode = this
    if (!before) {
      this.childNodes.push(child)
      return child
    }
    const index = this.childNodes.indexOf(before)
    if (index < 0) throw new Error('insertBefore target is not a child')
    this.childNodes.splice(index, 0, child)
    return child
  }

  removeChild<T extends MiniNode>(child: T): T {
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
    untrackListener(this, type, listener)
  }

  dispatchEvent(event: MiniEvent) {
    event.target = this
    event.currentTarget = this
    emitListeners(this, event)
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
    untrackListener(this, type, listener)
  }
}

function findTag(node: MiniNode, tag: string): MiniNode | null {
  if (node.tagName === tag) return node
  for (const child of node.childNodes) {
    const found = findTag(child, tag)
    if (found) return found
  }
  return null
}

function errorEvent(target: MiniNode): MiniEvent {
  return {
    type: 'error',
    target,
    currentTarget: target,
    bubbles: false,
    cancelable: false,
    defaultPrevented: false,
    eventPhase: 2,
    timeStamp: 0,
    preventDefault: () => {},
    stopPropagation: () => {},
  }
}

async function mount(node: ReactNode) {
  const doc = new MiniDocument()
  const container = doc.createElement('div')
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
      root.render(node)
    })
    return {
      container,
      async render(next: ReactNode) {
        await act(async () => {
          root.render(next)
        })
      },
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
