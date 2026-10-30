import { describe, expect, it } from 'vitest'
import { resolveDragMove, resolveDragStart, resolveRelease } from './draw-gesture'

const start = (overrides: Partial<Parameters<typeof resolveDragStart>[0]> = {}) => resolveDragStart({
  state: 'medium',
  dy: -10,
  scrollTop: 0,
  scrollable: true,
  fromHandle: false,
  canDismiss: true,
  ...overrides,
})

const release = (overrides: Partial<Parameters<typeof resolveRelease>[0]> = {}) => resolveRelease({
  mode: 'expand',
  progress: 0,
  velocity: 0,
  canDismiss: true,
  ...overrides,
})

describe('draw gesture', () => {
  it('does not expand a non-scrollable body', () => {
    expect(start({ scrollable: false })).toBe('native')
  })

  it('expands a scrollable medium sheet and snaps by progress or velocity', () => {
    expect(start()).toBe('expand')
    expect(release({ progress: 0.26 })).toBe('snap-full')
    expect(release({ progress: 0.1, velocity: -0.51 })).toBe('snap-full')
    expect(release({ progress: 0.25, velocity: -0.5 })).toBe('rebound')
  })

  it('clamps expansion to the full height', () => {
    expect(resolveDragMove({ mode: 'expand', dy: -500, startHeight: 400, mediumHeight: 400, fullHeight: 700 })).toEqual({ height: 700 })
  })

  it('leaves downward movement native until full-sheet content reaches the top', () => {
    expect(start({ state: 'full', dy: 10, scrollTop: 1 })).toBe('native')
    expect(start({ state: 'full', dy: 10, scrollTop: 0 })).toBe('shrink')
    expect(release({ mode: 'shrink', progress: 0.26 })).toBe('snap-medium')
    expect(release({ mode: 'shrink', progress: 0.1, velocity: 0.51 })).toBe('snap-medium')
    expect(release({ mode: 'shrink', progress: 0.25, velocity: 0.5 })).toBe('rebound')
  })

  it('dismisses a medium sheet by distance or downward velocity', () => {
    expect(start({ dy: 10 })).toBe('dismiss')
    expect(release({ mode: 'dismiss', progress: 0.31 })).toBe('dismiss')
    expect(release({ mode: 'dismiss', progress: 0.1, velocity: 0.81 })).toBe('dismiss')
    expect(release({ mode: 'dismiss', progress: 0.3, velocity: 0.8 })).toBe('rebound')
  })

  it('never dismisses when dismissal is disabled', () => {
    expect(start({ dy: 10, canDismiss: false })).toBe('native')
    expect(release({ mode: 'dismiss', progress: 1, velocity: 2, canDismiss: false })).toBe('rebound')
  })

  it('treats handle gestures independently of scroll position', () => {
    expect(start({ state: 'full', dy: 10, scrollTop: 200, fromHandle: true })).toBe('shrink')
    expect(start({ state: 'medium', dy: 10, scrollTop: 200, fromHandle: true })).toBe('dismiss')
    expect(start({ dy: -10, scrollTop: 200, scrollable: false, fromHandle: true })).toBe('native')
    expect(start({ dy: -10, scrollTop: 200, scrollable: true, fromHandle: true })).toBe('expand')
  })
})
