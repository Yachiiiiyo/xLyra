import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentQuestionCard, AgentQuestionSummary } from '@/features/agent/components/agent-question-card'
import { QUESTION_OTHER, type AgentQuestionAnswer } from '@/features/agent/api/agent'
import type { AgentQuestion } from '@/features/agent/lib/agent-events'
import zhAgent from '@/locales/zh/agent.json'

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

const singleQuestion: AgentQuestion = {
  id: 'q1',
  toolCallId: 'call-1',
  status: 'pending',
  items: [
    { header: '风格', question: '选哪种风格？', multiSelect: false, options: [{ label: '简约' }, { label: '复古', description: '怀旧感' }] },
  ],
}

const multiQuestion: AgentQuestion = {
  id: 'q2',
  toolCallId: 'call-2',
  status: 'pending',
  items: [
    { header: '模块', question: '需要哪些模块？', multiSelect: true, options: [{ label: 'A' }, { label: 'B' }] },
  ],
}

const wizardQuestion: AgentQuestion = {
  id: 'q3',
  toolCallId: 'call-3',
  status: 'pending',
  items: [singleQuestion.items[0]!, multiQuestion.items[0]!],
}

// ---- 最小 DOM 夹具（沿用 agent-artifact-files.test.tsx 的手法） ----
type MiniListener = (event: MiniEvent) => void
const miniListeners = new WeakMap<object, Map<string, Set<MiniListener>>>()
function trackListener(target: object, type: string, listener: MiniListener) {
  const byType = miniListeners.get(target) ?? new Map<string, Set<MiniListener>>()
  const set = byType.get(type) ?? new Set()
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
  metaKey?: boolean
  shiftKey?: boolean
  altKey?: boolean
  key?: string
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
  declare value: string
  selectionStart = 0
  selectionEnd = 0
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
  removeEventListener(type: string, listener: MiniListener) { miniListeners.get(this)?.get(type)?.delete(listener) }
  attachEvent(type: string, listener: MiniListener) { this.addEventListener(type.replace(/^on/, ''), listener) }
  detachEvent(type: string, listener: MiniListener) { this.removeEventListener(type.replace(/^on/, ''), listener) }
  focus() {
    this.ownerDocument.activeElement = this
    this.dispatchEvent(keyEvent('focusin', ''))
  }
  blur() { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null }
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
  activeElement: MiniNode | null = null
  oninput = null
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
// React 的受控 value 依赖原型上的属性描述符来安装 value tracker；实例字段会挡住它
Object.defineProperty(MiniNode.prototype, 'value', {
  configurable: true,
  get(this: MiniNode) { return this.getAttribute('value') ?? '' },
  set(this: MiniNode, value: string) { this.setAttribute('value', value) },
})

function keyEvent(type: string, key: string, init?: Partial<MiniEvent>): MiniEvent {
  return {
    type, target: null, currentTarget: null, bubbles: true, cancelable: true, defaultPrevented: false,
    button: 0, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, key,
    eventPhase: 2, timeStamp: 0, stopped: false,
    preventDefault() { this.defaultPrevented = true }, stopPropagation() { this.stopped = true },
    ...init,
  }
}
function clickEvent(): MiniEvent {
  return {
    type: 'click', target: null, currentTarget: null, bubbles: true, cancelable: true, defaultPrevented: false,
    button: 0, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false,
    eventPhase: 2, timeStamp: 0, stopped: false,
    preventDefault() { this.defaultPrevented = true }, stopPropagation() { this.stopped = true },
  }
}
function findAll(node: MiniNode, predicate: (node: MiniNode) => boolean): MiniNode[] {
  return [...(predicate(node) ? [node] : []), ...node.childNodes.flatMap((child) => findAll(child, predicate))]
}

/** 挂载卡片到 Mini DOM，返回查询辅助 */
async function mountCard(question: AgentQuestion, onSubmit: (answers: AgentQuestionAnswer[]) => void, onSkip: () => void) {
  const { createRoot } = await import('react-dom/client')
  const doc = new MiniDocument()
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as { window: unknown; document: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean }
  const previous = { window: globals.window, document: globals.document }
  globals.window = { document: doc, HTMLIFrameElement: class {}, addEventListener() {}, removeEventListener() {} }
  doc.defaultView = globals.window
  globals.document = doc
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const root = createRoot(container as unknown as Element)
  const cleanup = async () => {
    await act(async () => { root.unmount() })
    globals.window = previous.window
    globals.document = previous.document
  }
  await act(async () => {
    root.render(<I18nextProvider i18n={i18n}><AgentQuestionCard question={question} onSubmit={onSubmit} onSkip={onSkip} /></I18nextProvider>)
  })
  return { container, cleanup }
}

const optionRow = (container: MiniNode, label: string) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && (node.textContent?.includes(label) ?? false))[0]
const submitButton = (container: MiniNode) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && (node.textContent === zhAgent.question.submit))[0]
const nextButton = (container: MiniNode) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && (node.textContent === zhAgent.question.next))[0]
const backButton = (container: MiniNode) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && (node.textContent === zhAgent.question.back))[0]
const skipButton = (container: MiniNode) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && (node.textContent === zhAgent.question.skip))[0]
const editButtons = (container: MiniNode) =>
  findAll(container, (node) => node.tagName === 'BUTTON' && node.textContent === zhAgent.question.edit)
const textareaOf = (container: MiniNode) => findAll(container, (node) => node.tagName === 'TEXTAREA')[0]
const cardRoot = (container: MiniNode) =>
  findAll(container, (node) => node.getAttribute('role') === 'group' && node.getAttribute('aria-label') === zhAgent.question.ariaCard)[0]

async function changeText(node: MiniNode, value: string) {
  const setter = Object.getOwnPropertyDescriptor(MiniNode.prototype, 'value')!.set!
  await act(async () => {
    node.focus()
    setter.call(node, value)
    node.dispatchEvent(keyEvent('input', ''))
    node.dispatchEvent(keyEvent('keyup', ''))
  })
}

beforeEach(() => { vi.restoreAllMocks() })

describe('AgentQuestionCard 渲染', () => {
  it('渲染 chip、问题、选项与「其他」，选项带 radio/checkbox 语义', () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AgentQuestionCard question={singleQuestion} onSubmit={() => {}} onSkip={() => {}} />
      </I18nextProvider>,
    )
    expect(html).toContain('风格')
    expect(html).toContain('选哪种风格？')
    expect(html).toContain('简约')
    expect(html).toContain('复古')
    expect(html).toContain(zhAgent.question.otherLabel)
    expect(html).toContain('role="radio"')
    expect(html).toContain('aria-checked="false"')
    expect(html).toContain(zhAgent.question.submit)
    expect(html).toContain(zhAgent.question.skip)
    expect(html).not.toContain(zhAgent.question.next)
    expect(html).not.toContain(zhAgent.question.confirmTitle)
  })

  it('多题初始只渲染第一题并显示进度，不显示确认步骤', () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AgentQuestionCard question={wizardQuestion} onSubmit={() => {}} onSkip={() => {}} />
      </I18nextProvider>,
    )
    expect(html).toContain('第 1 / 2 题')
    expect(html).toContain('选哪种风格？')
    expect(html).not.toContain('需要哪些模块？')
    expect(html).toContain(zhAgent.question.next)
    expect(html).not.toContain(zhAgent.question.submit)
    expect(html).not.toContain(zhAgent.question.confirmTitle)
  })

  it('多选题选项带 checkbox 语义，移动端选项触控高度 ≥ 44px', () => {
    const html = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AgentQuestionCard question={multiQuestion} onSubmit={() => {}} onSkip={() => {}} />
      </I18nextProvider>,
    )
    expect(html).toContain('role="checkbox"')
    expect(html).toContain('min-h-11')
  })

  it('已回答摘要：左竖线引用块、每题一行、其他写「其他：文本」', () => {
    const answered: AgentQuestion = {
      ...singleQuestion,
      status: 'answered',
      answers: [{ selected: [QUESTION_OTHER], other: '加个暗色模式' }],
    }
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentQuestionSummary question={answered} /></I18nextProvider>)
    expect(html).toContain('border-l-2')
    expect(html).toContain('风格')
    expect(html).toContain('选哪种风格？')
    expect(html).toContain(`${zhAgent.question.otherLabel}：加个暗色模式`)
  })

  it('跳过/取消/超时自动跳过的摘要文案', () => {
    const skipped: AgentQuestion = { ...singleQuestion, status: 'skipped' }
    const timedOut: AgentQuestion = { ...singleQuestion, status: 'skipped', auto: true }
    const cancelled: AgentQuestion = { ...singleQuestion, status: 'cancelled' }
    for (const [question, label] of [[skipped, zhAgent.question.skippedLabel], [timedOut, zhAgent.question.timedOutSkipped], [cancelled, zhAgent.question.cancelledLabel]] as const) {
      const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentQuestionSummary question={question} /></I18nextProvider>)
      expect(html).toContain(label)
    }
  })
})

describe('AgentQuestionCard 交互', () => {
  it('单选：点击选项提交，请求体恰含选中标签', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(singleQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenCalledWith([{ selected: ['简约'] }])
    } finally { await cleanup() }
  })

  it('多选：可勾选多项，请求体按选择顺序', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(multiQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, 'B')!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, 'A')!.dispatchEvent(clickEvent()) })
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenCalledWith([{ selected: ['B', 'A'] }])
    } finally { await cleanup() }
  })

  it('选「其他」出现文本框；未填文本点下一步给校验提示并停留', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, zhAgent.question.otherLabel)!.dispatchEvent(clickEvent()) })
      expect(textareaOf(container)).toBeTruthy()
      expect(textareaOf(container)!.getAttribute('placeholder')).toBe(zhAgent.question.otherPlaceholder)
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).not.toHaveBeenCalled()
      expect(findAll(container, (node) => node.getAttribute('role') === 'alert')[0]?.textContent).toBe(zhAgent.question.otherRequired)
      expect(container.textContent).toContain('选哪种风格？')
      expect(container.textContent).not.toContain('需要哪些模块？')
    } finally { await cleanup() }
  })

  it('未选择点下一步提示「每个问题至少选择一项」并停留', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, onSubmit, () => {})
    try {
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).not.toHaveBeenCalled()
      expect(findAll(container, (node) => node.getAttribute('role') === 'alert')[0]?.textContent).toBe(zhAgent.question.required)
      expect(container.textContent).toContain('选哪种风格？')
      expect(container.textContent).not.toContain('需要哪些模块？')
    } finally { await cleanup() }
  })

  it('下一题后返回上一步，第一题的选择保留', async () => {
    const { container, cleanup } = await mountCard(wizardQuestion, () => {}, () => {})
    try {
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain('需要哪些模块？')
      expect(container.textContent).not.toContain('选哪种风格？')
      await act(async () => { backButton(container)!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain('选哪种风格？')
      expect(optionRow(container, '简约')!.getAttribute('aria-checked')).toBe('true')
      expect(container.textContent).not.toContain('需要哪些模块？')
    } finally { await cleanup() }
  })

  it('确认步骤列出所有回答与其他文本，最终提交请求体恰好符合契约', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, zhAgent.question.otherLabel)!.dispatchEvent(clickEvent()) })
      await changeText(textareaOf(container)!, ' 加点暗色 ')
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, 'B')!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, 'A')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain(zhAgent.question.confirmTitle)
      expect(container.textContent).toContain('选哪种风格？')
      expect(container.textContent).toContain('需要哪些模块？')
      expect(container.textContent).toContain(`${zhAgent.question.otherLabel}：加点暗色`)
      expect(container.textContent).toContain('B、A')
      expect(nextButton(container)).toBeUndefined()
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenCalledExactlyOnceWith([
        { selected: [QUESTION_OTHER], other: '加点暗色' },
        { selected: ['B', 'A'] },
      ])
    } finally { await cleanup() }
  })

  it('确认步骤点修改回到对应题，保留原选择并可继续修改', async () => {
    const { container, cleanup } = await mountCard(wizardQuestion, () => {}, () => {})
    try {
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, 'A')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(editButtons(container)).toHaveLength(2)
      await act(async () => { editButtons(container)[1]!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain('需要哪些模块？')
      expect(container.textContent).not.toContain('选哪种风格？')
      expect(optionRow(container, 'A')!.getAttribute('aria-checked')).toBe('true')
      await act(async () => { optionRow(container, 'B')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain(zhAgent.question.confirmTitle)
      expect(container.textContent).toContain('A、B')
    } finally { await cleanup() }
  })

  it('跳过在题目步骤都可用,确认步骤不再提供跳过', async () => {
    const onSkip = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, () => {}, onSkip)
    try {
      await act(async () => { skipButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { skipButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { optionRow(container, 'A')!.dispatchEvent(clickEvent()) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      expect(container.textContent).toContain(zhAgent.question.confirmTitle)
      expect(skipButton(container)).toBeUndefined()
      expect(onSkip).toHaveBeenCalledTimes(2)
    } finally { await cleanup() }
  })

  it('提交失败保留已选：父组件不换状态时卡片原样可再次提交', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(singleQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, '复古')!.dispatchEvent(clickEvent()) })
      // 第一次提交失败（父组件不乐观更新，卡片仍是 pending）
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenCalledTimes(1)
      // 选择保留，可再次提交
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenNthCalledWith(2, [{ selected: ['复古'] }])
    } finally { await cleanup() }
  })

  it('数字键只作用于当前题', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, onSubmit, () => {})
    try {
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', '2')) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', '2')) })
      await act(async () => { nextButton(container)!.dispatchEvent(clickEvent()) })
      await act(async () => { submitButton(container)!.dispatchEvent(clickEvent()) })
      expect(onSubmit).toHaveBeenCalledWith([{ selected: ['复古'] }, { selected: ['B'] }])
    } finally { await cleanup() }
  })

  it('单题卡片上按 Enter 直接提交已选的答案', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(singleQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', 'Enter')) })
      expect(onSubmit).toHaveBeenCalledExactlyOnceWith([{ selected: ['简约'] }])
    } finally { await cleanup() }
  })

  it('多题 Enter 依次进入下一题、确认步骤并提交', async () => {
    const onSubmit = vi.fn()
    const { container, cleanup } = await mountCard(wizardQuestion, onSubmit, () => {})
    try {
      await act(async () => { optionRow(container, '简约')!.dispatchEvent(clickEvent()) })
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', 'Enter')) })
      expect(container.textContent).toContain('需要哪些模块？')
      await act(async () => { optionRow(container, 'A')!.dispatchEvent(clickEvent()) })
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', 'Enter')) })
      expect(container.textContent).toContain(zhAgent.question.confirmTitle)
      await act(async () => { cardRoot(container)!.dispatchEvent(keyEvent('keydown', 'Enter')) })
      expect(onSubmit).toHaveBeenCalledExactlyOnceWith([{ selected: ['简约'] }, { selected: ['A'] }])
    } finally { await cleanup() }
  })

  it('文本框内 Shift+Enter 停留，Ctrl+Enter 进入下一题', async () => {
    const { container, cleanup } = await mountCard(wizardQuestion, () => {}, () => {})
    try {
      await act(async () => { optionRow(container, zhAgent.question.otherLabel)!.dispatchEvent(clickEvent()) })
      await changeText(textareaOf(container)!, '补充说明')
      const shiftEnter = keyEvent('keydown', 'Enter', { shiftKey: true })
      await act(async () => { textareaOf(container)!.dispatchEvent(shiftEnter) })
      expect(shiftEnter.defaultPrevented).toBe(false)
      expect(container.textContent).toContain('选哪种风格？')
      await act(async () => { textareaOf(container)!.dispatchEvent(keyEvent('keydown', 'Enter', { ctrlKey: true })) })
      expect(container.textContent).toContain('需要哪些模块？')
    } finally { await cleanup() }
  })
})
