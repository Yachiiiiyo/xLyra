import * as React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DrawContent, DrawFooter } from './draw'
import { DialogFooter } from './dialog'
import { SheetFooter } from './sheet'
import { DrawFooterRegistrationContext, OverlayModeContext } from './overlay-mode'
import { useDrawGesture } from './use-draw-gesture'

vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof React>()
  // Server rendering keeps this test DOM-free; run only the registration effect.
  return { ...actual, useEffect: (effect: React.EffectCallback) => { effect() } }
})
vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children }: { children?: React.ReactNode }) => React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => false }))
vi.mock('./use-draw-gesture', () => ({ useDrawGesture: vi.fn() }))

afterEach(() => {
  vi.mocked(useDrawGesture).mockClear()
})

function canDismiss() {
  return vi.mocked(useDrawGesture).mock.lastCall?.[2]
}

describe('DrawContent swipe dismissal registration', () => {
  it.each([
    ['DrawFooter', DrawFooter],
    ['DialogFooter', DialogFooter],
    ['SheetFooter', SheetFooter],
  ])('registers save actions for %s', (_name, Footer) => {
    const register = vi.fn()
    renderToStaticMarkup(
      <OverlayModeContext.Provider value="draw">
        <DrawFooterRegistrationContext.Provider value={register}>
          <Footer confirm={<button>保存</button>} />
        </DrawFooterRegistrationContext.Provider>
      </OverlayModeContext.Provider>,
    )
    expect(register).toHaveBeenCalledWith(true)
  })

  it('uses a light translucent corner button instead of nested dark glass', () => {
    const html = renderToStaticMarkup(<DrawContent />)
    expect(html).toContain('size-10')
    expect(html).toContain('border-white/10')
    expect(html).toContain('bg-white/10')
    expect(html).toContain('backdrop-blur-md')
    expect(html).not.toContain('lg-regular')
  })

  it('allows dismissal without save actions', () => {
    renderToStaticMarkup(<DrawContent />)
    expect(canDismiss()).toBe(true)
  })

  it('disables dismissal for a back action', () => {
    renderToStaticMarkup(<DrawContent onBack={() => {}} />)
    expect(canDismiss()).toBe(false)
  })

  it('prefers the explicit override', () => {
    renderToStaticMarkup(<DrawContent swipeDismiss><DrawFooter confirm={<button>保存</button>} /></DrawContent>)
    expect(canDismiss()).toBe(true)
  })
})
