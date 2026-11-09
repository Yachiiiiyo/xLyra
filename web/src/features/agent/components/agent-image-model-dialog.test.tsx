import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentImageModelDialog } from './agent-image-model-dialog'
import { updateAgentImageModels, type AgentImageModelSelection } from '@/features/agent/api/agent'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'
import zhApiKeys from '@/locales/zh/api-keys.json'

const queryState = vi.hoisted(() => ({ imageModels: [] as Array<AgentImageModelSelection | string>, failed: false, retry: vi.fn(), empty: false }))
const mutation = vi.hoisted(() => ({ options: undefined as undefined | { mutationFn: () => Promise<unknown>; onSuccess?: () => Promise<void> | void } }))
const onOpenChange = vi.hoisted(() => vi.fn())
const layout = vi.hoisted(() => ({ mobile: false }))

vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children, open }: { children?: React.ReactNode; open?: boolean }) => open === false ? null : React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    const key = queryKey.at(-1)
    if (key === 'runtime') return { data: { image_models: queryState.imageModels.map((item) => typeof item === 'string' ? { model: item, site_ids: [] } : item) }, isLoading: false, isError: false, refetch: vi.fn() }
    if (key === 'sites') return queryState.failed
      ? { data: undefined, isLoading: false, isError: true, refetch: queryState.retry }
      : { data: { items: [
        { id: 'alpha', name: 'Alpha', enabled: true, site_type: 'openai' },
        { id: 'beta', name: 'Beta', enabled: true, site_type: 'openai' },
        { id: 'text-only', name: 'Text only', enabled: true, site_type: 'openai' },
        { id: 'disabled', name: 'Disabled', enabled: false, site_type: 'openai' },
      ] }, isLoading: false, isError: false, refetch: queryState.retry }
    if (queryKey.at(-2) === 'models' && key === 'all') {
      const modelsBySite = {
        alpha: [
          { id: 'alpha-shared', site_id: 'alpha', upstream_model_name: 'upstream-shared', display_name: 'Alpha Shared', canonical_model_id: 'canonical-shared', capabilities: { output_modalities: ['image'] }, status: 'active' },
        ],
        beta: [
          { id: 'beta-shared', site_id: 'beta', upstream_model_name: 'upstream-shared', display_name: 'Beta Shared', canonical_model_id: 'canonical-shared', capabilities: { output_modalities: ['image'] }, status: 'active' },
          { id: 'beta-other', site_id: 'beta', upstream_model_name: 'upstream-other', display_name: 'Other', canonical_model_id: 'canonical-other', capabilities: { supported_endpoint_types: ['openai-image'] }, status: 'active' },
        ],
        'text-only': [
          { id: 'text', site_id: 'text-only', upstream_model_name: 'gpt', display_name: 'GPT', canonical_model_id: '', capabilities: { supported_endpoint_types: ['openai'] }, status: 'active' },
        ],
        disabled: [],
      }
      return { data: queryState.empty ? {} : modelsBySite, isLoading: false, isError: false, refetch: vi.fn() }
    }
    return { data: { items: [
      { id: 'canonical-shared', model_key: 'shared', display_name: 'Shared', capabilities: {} },
      { id: 'canonical-other', model_key: 'other', display_name: 'Other', capabilities: {} },
    ] }, isLoading: false, isError: false, refetch: vi.fn() }
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  useMutation: (options: { mutationFn: () => Promise<unknown>; onSuccess?: () => Promise<void> | void }) => {
    mutation.options = options
    return { mutate: vi.fn(() => { void options.mutationFn() }), isPending: false }
  },
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  updateAgentImageModels: vi.fn(),
}))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => layout.mobile }))
vi.mock('@/features/appearance/api/appearance', () => ({ useHasAppBackground: () => false }))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()
beforeAll(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  await i18n.init({ lng: 'zh', defaultNS: 'agent', ns: ['agent', 'common', 'api-keys'], resources: { zh: { agent: zhAgent, common: zhCommon, 'api-keys': zhApiKeys } } })
})
beforeEach(() => {
  queryState.imageModels = []
  queryState.failed = false
  queryState.empty = false
  queryState.retry.mockReset()
  mutation.options = undefined
  onOpenChange.mockReset()
  layout.mobile = false
  vi.mocked(updateAgentImageModels).mockReset().mockResolvedValue(undefined)
})
afterEach(() => {
  vi.restoreAllMocks()
})

async function mountDialog(path = '/') {
  const doc = new MiniDocument()
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as { window: unknown; document: unknown; HTMLIFrameElement?: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean }
  const previous = { window: globals.window, document: globals.document, HTMLIFrameElement: globals.HTMLIFrameElement }
  class FrameElement {}
  globals.window = globalThis
  globals.document = doc
  globals.HTMLIFrameElement = FrameElement
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const { createRoot } = await import('react-dom/client')
  const root = createRoot(container as unknown as Element)
  try {
    await act(async () => {
      root.render(<I18nextProvider i18n={i18n}><MemoryRouter initialEntries={[path]}><AgentImageModelDialog open onOpenChange={onOpenChange} /></MemoryRouter></I18nextProvider>)
    })
    await flushUpdates()
  } catch (error) {
    globals.window = previous.window
    globals.document = previous.document
    globals.HTMLIFrameElement = previous.HTMLIFrameElement
    throw error
  }
  return {
    container,
    async unmount() {
      await act(async () => { root.unmount() })
      globals.window = previous.window
      globals.document = previous.document
      globals.HTMLIFrameElement = previous.HTMLIFrameElement
    },
  }
}

async function flushUpdates() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

async function click(node: MiniNode | null) {
  expect(node).not.toBeNull()
  const eventType = node!.getAttribute('role') === 'tab' ? 'mousedown' : 'click'
  await act(async () => { node!.dispatchEvent(inputEvent(node!, eventType)) })
  await flushUpdates()
}

async function toggle(node: MiniNode | null) {
  await click(node)
}

function findButton(node: MiniNode, label: string): MiniNode | null {
  const buttons = findAll(node, (item) => item.tagName === 'BUTTON')
  return buttons.find((item) => item.textContent.trim() === label) ?? null
}

function findAll(node: MiniNode, predicate: (node: MiniNode) => boolean): MiniNode[] {
  return [...(predicate(node) ? [node] : []), ...node.childNodes.flatMap((child) => findAll(child, predicate))]
}

function checkboxes(container: MiniNode) {
  return findAll(container, (node) => node.getAttribute('role') === 'checkbox')
}

function selectedOrder(container: MiniNode) {
  return findAll(container, (node) => node.tagName === 'OL')[0]?.childNodes.map((item) => item.childNodes[0]?.childNodes[1]?.textContent) ?? []
}

function siteChips(container: MiniNode) {
  return findAll(container, (node) => node.tagName === 'UL')[0]?.childNodes.map((item) => item.childNodes[0]?.textContent) ?? []
}

describe('AgentImageModelDialog 多选与顺序', () => {
  it('按已启用站点分组生图候选并隐藏没有生图模型的站点', async () => {
    const mounted = await mountDialog()
    try {
      expect(mounted.container.textContent).toContain('Alpha')
      expect(mounted.container.textContent).toContain('Beta')
      expect(mounted.container.textContent).not.toContain('Text only')
      expect(mounted.container.textContent).not.toContain('Disabled')
      expect(mounted.container.textContent).toContain('Alpha Shared')
      expect(mounted.container.textContent).not.toContain('Beta Shared')
      await click(findButton(mounted.container, 'Beta0/2'))
      expect(mounted.container.textContent).toContain('Beta Shared')
      expect(mounted.container.textContent).toContain('Other')
    } finally { await mounted.unmount() }
  })

  it('跨站点同名模型只占一项但勾选状态与站点计数各自独立', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      expect(mounted.container.textContent).toContain('Alpha1/1')
      expect(mounted.container.textContent).toContain('Beta0/2')
      expect(selectedOrder(mounted.container)).toEqual(['Beta Shared'])
      expect(siteChips(mounted.container)).toEqual(['Alpha'])

      await click(findButton(mounted.container, 'Beta0/2'))
      expect(checkboxes(mounted.container)[0]?.getAttribute('aria-checked')).toBe('false')
      await toggle(checkboxes(mounted.container)[0])
      expect(mounted.container.textContent).toContain('Alpha1/1')
      expect(mounted.container.textContent).toContain('Beta1/2')
      expect(selectedOrder(mounted.container)).toEqual(['Beta Shared'])
      expect(siteChips(mounted.container)).toEqual(['Alpha', 'Beta'])

      await click(findButton(mounted.container, 'Alpha1/1'))
      await toggle(checkboxes(mounted.container)[0])
      await click(findButton(mounted.container, 'Beta1/2'))
      expect(mounted.container.textContent).toContain('Alpha0/1')
      expect(mounted.container.textContent).toContain('Beta1/2')
      expect(checkboxes(mounted.container)[0]?.getAttribute('aria-checked')).toBe('true')
      expect(siteChips(mounted.container)).toEqual(['Beta'])
    } finally { await mounted.unmount() }
  })

  it('勾选按模型顺序追加且只移除被取消站点的模型', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      await click(findButton(mounted.container, 'Beta0/2'))
      await toggle(checkboxes(mounted.container)[1])
      expect(selectedOrder(mounted.container)).toEqual(['Beta Shared', 'Other'])
      await click(findButton(mounted.container, 'Alpha1/1'))
      await toggle(checkboxes(mounted.container)[0])
      expect(selectedOrder(mounted.container)).toEqual(['Other'])
    } finally { await mounted.unmount() }
  })

  it('站点 chip 只移除对应站点，最后一个站点移除时整条模型消失', async () => {
    queryState.imageModels = [{ model: 'shared', site_ids: ['alpha', 'beta'] }]
    const mounted = await mountDialog()
    try {
      expect(siteChips(mounted.container)).toEqual(['Alpha', 'Beta'])
      const removeSiteButtons = findAll(mounted.container, (node) => node.getAttribute('aria-label') === zhAgent.settings.scope.imageRemoveSite)
      expect(removeSiteButtons).toHaveLength(2)
      await click(removeSiteButtons[0])
      expect(siteChips(mounted.container)).toEqual(['Beta'])
      expect(selectedOrder(mounted.container)).toEqual(['Beta Shared'])
      await click(findAll(mounted.container, (node) => node.getAttribute('aria-label') === zhAgent.settings.scope.imageRemoveSite)[0])
      expect(selectedOrder(mounted.container)).toEqual([])
    } finally { await mounted.unmount() }
  })

  it('移除模型按钮删除整条模型及其全部站点', async () => {
    queryState.imageModels = [{ model: 'shared', site_ids: ['alpha', 'beta'] }]
    const mounted = await mountDialog()
    try {
      await click(findAll(mounted.container, (node) => node.getAttribute('aria-label') === zhAgent.settings.scope.imageRemove)[0])
      expect(selectedOrder(mounted.container)).toEqual([])
      expect(mounted.container.textContent).toContain('Alpha0/1')
      expect(mounted.container.textContent).toContain('Beta0/2')
    } finally { await mounted.unmount() }
  })

  it('上移和下移按钮真实改变有序列表', async () => {
    queryState.imageModels = [
      { model: 'other', site_ids: ['beta'] },
      { model: 'shared', site_ids: ['alpha'] },
    ]
    const mounted = await mountDialog()
    try {
      expect(selectedOrder(mounted.container)).toEqual(['Other', 'Beta Shared'])
      await click(findAll(mounted.container, (node) => node.getAttribute('aria-label') === zhAgent.settings.scope.imageMoveDown)[0])
      expect(selectedOrder(mounted.container)).toEqual(['Beta Shared', 'Other'])
      await click(findAll(mounted.container, (node) => node.getAttribute('aria-label') === zhAgent.settings.scope.imageMoveUp)[1])
      expect(selectedOrder(mounted.container)).toEqual(['Other', 'Beta Shared'])
    } finally { await mounted.unmount() }
  })

  it('保存请求体恰好包含对象数组并按站点勾选顺序合并同名模型', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      await click(findButton(mounted.container, 'Beta0/2'))
      await toggle(checkboxes(mounted.container)[0])
      await toggle(checkboxes(mounted.container)[1])
      await click(findButton(mounted.container, zhAgent.settings.save))
      expect(updateAgentImageModels).toHaveBeenCalledExactlyOnceWith([
        { model: 'shared', site_ids: ['alpha', 'beta'] },
        { model: 'other', site_ids: ['beta'] },
      ])
      expect(mutation.options).toBeDefined()
    } finally { await mounted.unmount() }
  })

  it('清除后保存发送空 image_models 数组', async () => {
    queryState.imageModels = [
      { model: 'shared', site_ids: ['alpha'] },
      { model: 'other', site_ids: ['beta'] },
    ]
    const mounted = await mountDialog()
    try {
      await click(findButton(mounted.container, zhAgent.settings.scope.imageClear))
      expect(selectedOrder(mounted.container)).toEqual([])
      await click(findButton(mounted.container, zhAgent.settings.save))
      expect(updateAgentImageModels).toHaveBeenCalledExactlyOnceWith([])
    } finally { await mounted.unmount() }
  })

  it.each([
    ['旧字符串元素', ['shared'] as Array<AgentImageModelSelection | string>],
    ['不限站点的空 site_ids', [{ model: 'shared', site_ids: [] }] as Array<AgentImageModelSelection | string>],
  ])('%s 已有配置打开时展开为所有提供站点已选', async (_label, imageModels) => {
    queryState.imageModels = imageModels
    const mounted = await mountDialog()
    try {
      expect(checkboxes(mounted.container)[0]?.getAttribute('aria-checked')).toBe('true')
      expect(mounted.container.textContent).toContain('Alpha1/1')
      expect(mounted.container.textContent).toContain('Beta1/2')
      expect(siteChips(mounted.container)).toEqual(['Alpha', 'Beta'])
      await click(findButton(mounted.container, 'Beta1/2'))
      expect(checkboxes(mounted.container)[0]?.getAttribute('aria-checked')).toBe('true')
    } finally { await mounted.unmount() }
  })

  it('没有候选时显示生图空态', async () => {
    queryState.empty = true
    const mounted = await mountDialog()
    try {
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.imageEmpty)
      expect(checkboxes(mounted.container)).toHaveLength(0)
    } finally { await mounted.unmount() }
  })

  it('拉取失败显示加载失败且重试会重新拉取站点', async () => {
    queryState.failed = true
    const mounted = await mountDialog()
    try {
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.imageLoadFailed)
      await click(findButton(mounted.container, zhAgent.settings.scope.imageRetry))
      expect(queryState.retry).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })

  it('改了又改回原样不算未保存,取消直接关闭', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.unsavedChanges)
      await toggle(checkboxes(mounted.container)[0])
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.unsavedChanges)
      await click(findButton(mounted.container, zhAgent.settings.cancel))
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.discardTitle)
      expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
    } finally { await mounted.unmount() }
  })

  it('保存成功后草稿被清空,再次打开不再提示未保存的更改', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.unsavedChanges)
      await act(async () => { await mutation.options?.onSuccess?.() })
      expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
      expect(mounted.container.textContent).not.toContain(zhAgent.settings.scope.unsavedChanges)
    } finally { await mounted.unmount() }
  })

  it('有未保存修改时取消要求确认且放弃后不发送保存请求', async () => {
    const mounted = await mountDialog()
    try {
      await toggle(checkboxes(mounted.container)[0])
      await click(findButton(mounted.container, zhAgent.settings.cancel))
      expect(mounted.container.textContent).toContain(zhAgent.settings.scope.discardTitle)
      expect(onOpenChange).not.toHaveBeenCalled()
      await click(findButton(mounted.container, zhAgent.settings.scope.discardConfirm))
      expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false)
      expect(updateAgentImageModels).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
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
  checked = false
  disabled = false
  type = ''
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
  get className() { return this.attributes.class ?? '' }
  set className(value: string) { this.attributes.class = value }
  setAttribute(name: string, value: string) { this.attributes[name] = String(value) }
  getAttribute(name: string) { return this.attributes[name] ?? null }
  removeAttribute(name: string) { delete this.attributes[name] }
  hasAttribute(name: string) { return Object.prototype.hasOwnProperty.call(this.attributes, name) }
  setAttributeNS(_namespace: string, name: string, value: string) { this.setAttribute(name, value) }
  focus() {}
  scrollIntoView() {}
  querySelector(selector: string) { return this.querySelectorAll(selector)[0] ?? null }
  querySelectorAll(selector: string) {
    if (selector === '[role="tab"]') return findAll(this, (node) => node.getAttribute('role') === 'tab')
    if (selector === '[role="tab"][data-state="active"]') return findAll(this, (node) => node.getAttribute('role') === 'tab' && node.getAttribute('data-state') === 'active')
    return []
  }
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
  defaultView = globalThis
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
function inputEvent(target: MiniNode, type: string): MiniEvent {
  return {
    type, target, currentTarget: target, bubbles: true, cancelable: true, defaultPrevented: false,
    button: 0, ctrlKey: false, eventPhase: 2, timeStamp: 0, stopped: false,
    preventDefault() { this.defaultPrevented = true }, stopPropagation() { this.stopped = true },
  }
}
