import { act } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { Composer } from './composer'
import { findMiniNodes, miniEvent, mountMini } from '@/test/mini-dom'

const placeholder = '请输入消息'

function staticComposer(props: Partial<Parameters<typeof Composer>[0]> = {}) {
  return renderToStaticMarkup(
    <Composer
      value=""
      onChange={() => {}}
      onSubmit={() => {}}
      canSubmit={false}
      placeholder={placeholder}
      mascot={false}
      {...props}
    />,
  )
}

function textareaOf(container: Parameters<typeof findMiniNodes>[0]) {
  return findMiniNodes(container, (node) => node.tagName === 'TEXTAREA')[0]!
}

describe('Composer ghost text', () => {
  it('仅在输入框为空且预测非空时显示灰字和 Tab chip，并隐藏原 placeholder', () => {
    const html = staticComposer({ ghostText: '帮我继续整理', ghostAcceptLabel: '接受输入预测', onAcceptGhost: () => {} })
    expect(html).toContain('帮我继续整理')
    expect(html).toContain('text-label-3')
    expect(html).toContain('aria-label="接受输入预测"')
    expect(html).toContain('placeholder=""')

    const typedHtml = staticComposer({ value: '已有输入', ghostText: '帮我继续整理' })
    const emptyGhostHtml = staticComposer({ ghostText: '' })
    for (const hiddenHtml of [typedHtml, emptyGhostHtml]) {
      expect(hiddenHtml).not.toContain('aria-label="接受输入预测"')
      expect(hiddenHtml).toContain(`placeholder="${placeholder}"`)
    }
  })

  it('无修饰键 Tab 接受预测并阻止默认行为', async () => {
    const onAcceptGhost = vi.fn()
    const mounted = await mountMini(<Composer value="" onChange={() => {}} onSubmit={() => {}} canSubmit={false} placeholder={placeholder} mascot={false} ghostText="下一条" ghostAcceptLabel="接受输入预测" onAcceptGhost={onAcceptGhost} />)
    try {
      const event = miniEvent('keydown', { key: 'Tab' })
      await act(async () => { textareaOf(mounted.container).dispatchEvent(event) })
      expect(onAcceptGhost).toHaveBeenCalledOnce()
      expect(event.defaultPrevented).toBe(true)
    } finally { await mounted.unmount() }
  })

  it('输入法组合态或带修饰键的 Tab 不接受预测', async () => {
    const onAcceptGhost = vi.fn()
    const mounted = await mountMini(<Composer value="" onChange={() => {}} onSubmit={() => {}} canSubmit={false} placeholder={placeholder} mascot={false} ghostText="下一条" ghostAcceptLabel="接受输入预测" onAcceptGhost={onAcceptGhost} />)
    try {
      for (const event of [
        miniEvent('keydown', { key: 'Tab', isComposing: true }),
        miniEvent('keydown', { key: 'Tab', shiftKey: true }),
        miniEvent('keydown', { key: 'Tab', ctrlKey: true }),
      ]) {
        await act(async () => { textareaOf(mounted.container).dispatchEvent(event) })
        expect(event.defaultPrevented).toBe(false)
      }
      expect(onAcceptGhost).not.toHaveBeenCalled()
    } finally { await mounted.unmount() }
  })

  it('点击 Tab chip 和按右方向键都能接受预测', async () => {
    const onAcceptGhost = vi.fn()
    const mounted = await mountMini(<Composer value="" onChange={() => {}} onSubmit={() => {}} canSubmit={false} placeholder={placeholder} mascot={false} ghostText="下一条" ghostAcceptLabel="接受输入预测" onAcceptGhost={onAcceptGhost} />)
    try {
      const chip = findMiniNodes(mounted.container, (node) => node.getAttribute('aria-label') === '接受输入预测')[0]!
      await act(async () => { chip.dispatchEvent(miniEvent('click')) })
      expect(onAcceptGhost).toHaveBeenCalledOnce()
      await act(async () => { textareaOf(mounted.container).dispatchEvent(miniEvent('keydown', { key: 'ArrowRight' })) })
      expect(onAcceptGhost).toHaveBeenCalledTimes(2)
    } finally { await mounted.unmount() }
  })

  it('Enter 保持提交逻辑，不会把预测当成输入接受或提交', async () => {
    const onAcceptGhost = vi.fn()
    const onSubmit = vi.fn()
    const props = { value: '', onChange: () => {}, onSubmit, placeholder, mascot: false, ghostText: '下一条', onAcceptGhost }
    const mounted = await mountMini(<Composer {...props} canSubmit={false} />)
    try {
      await act(async () => { textareaOf(mounted.container).dispatchEvent(miniEvent('keydown', { key: 'Enter' })) })
      expect(onAcceptGhost).not.toHaveBeenCalled()
      expect(onSubmit).not.toHaveBeenCalled()
      await mounted.render(<Composer {...props} canSubmit />)
      await act(async () => { textareaOf(mounted.container).dispatchEvent(miniEvent('keydown', { key: 'Enter' })) })
      expect(onAcceptGhost).not.toHaveBeenCalled()
      expect(onSubmit).toHaveBeenCalledOnce()
    } finally { await mounted.unmount() }
  })
})
