import { useEffect, useRef } from 'react'
import {
  DRAW_SNAP_DURATION,
  resolveDragMove,
  resolveDragStart,
  resolveRelease,
  type DrawDragMode,
  type DrawRelease,
  type DrawSnap,
} from '@/components/ui/draw-gesture'

const FULL_HEIGHT = 'calc(var(--app-viewport-height, 100dvh) - env(safe-area-inset-top, 0px) - 12px - env(safe-area-inset-bottom, 0px) - 8px)'
const SCROLLABLE_INPUT = 'input, textarea, select, [contenteditable]'

type Gesture = {
  pointerId: number
  startX: number
  anchorY: number
  lastY: number
  lastTime: number
  velocity: number
  state: DrawSnap
  mode: DrawDragMode
  nestedScroll: HTMLElement | null
  mainScroll: HTMLElement | null
  startHeight: number
  mediumHeight: number
  fullHeight: number
  height: number
  offset: number
  horizontal: boolean
  nativeOnly: boolean
  scrollHandoff: boolean
  expandScrollBase: number | null
}

type Frame = { height: number; offset: number; scrollTop?: number }

function measuredHeight(element: HTMLElement, height: string, maxHeight: string) {
  const previousHeight = element.style.height
  const previousMaxHeight = element.style.maxHeight
  element.style.height = height
  element.style.maxHeight = maxHeight
  const result = element.offsetHeight
  element.style.height = previousHeight
  element.style.maxHeight = previousMaxHeight
  return result
}

function nearestScrollable(target: Element, boundary: HTMLElement) {
  let current: HTMLElement | null = target instanceof HTMLElement ? target : target.parentElement
  while (current && current !== boundary) {
    const style = getComputedStyle(current)
    if (/(auto|scroll)/.test(style.overflowY) && current.scrollHeight > current.clientHeight) return current
    current = current.parentElement
  }
  return null
}

export function useDrawGesture(
  element: HTMLElement | null,
  enabled: boolean,
  canDismiss: boolean,
  requestClose: () => void,
) {
  const snapRef = useRef<DrawSnap>('medium')
  const canDismissRef = useRef(canDismiss)
  useEffect(() => {
    canDismissRef.current = canDismiss
  }, [canDismiss])

  useEffect(() => {
    if (!enabled || !element) return

    let gesture: Gesture | null = null
    let frame = 0
    let pendingFrame: Frame | null = null
    let snapTimer = 0

    const fullHeight = () => measuredHeight(element, FULL_HEIGHT, 'none')
    const apply = (next: Frame) => {
      element.style.height = `${next.height}px`
      element.style.maxHeight = 'none'
      element.style.setProperty('--draw-drag-y', `${next.offset}px`)
      if (next.scrollTop !== undefined && gesture?.mainScroll) gesture.mainScroll.scrollTop = next.scrollTop
    }
    const schedule = (next: Frame) => {
      pendingFrame = next
      if (!frame) frame = requestAnimationFrame(() => {
        frame = 0
        if (pendingFrame) apply(pendingFrame)
        pendingFrame = null
      })
    }
    const clearSnapTimer = () => {
      if (snapTimer) window.clearTimeout(snapTimer)
      snapTimer = 0
    }
    const settle = (release: DrawRelease) => {
      const active = gesture
      if (!active) return
      if (frame) cancelAnimationFrame(frame)
      frame = 0
      pendingFrame = null
      clearSnapTimer()

      if (release === 'dismiss') {
        // Keep the drag offset so the existing exit keyframes continue from here.
        requestClose()
        // 调用方可能拦截关闭（如提交中）：仍处于打开状态就回弹
        window.setTimeout(() => {
          if (!element.isConnected || element.getAttribute('data-state') !== 'open') return
          if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) element.classList.add('draw-gesture-snapping')
          element.style.setProperty('--draw-drag-y', '0px')
          window.setTimeout(() => element.classList.remove('draw-gesture-snapping'), DRAW_SNAP_DURATION)
        }, 60)
        gesture = null
        return
      }

      const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
      if (!reducedMotion) element.classList.add('draw-gesture-snapping')
      const target = release === 'snap-full' || (release === 'rebound' && active.state === 'full') ? 'full' : 'medium'
      snapRef.current = target
      element.style.setProperty('--draw-drag-y', '0px')
      element.style.height = target === 'full' ? FULL_HEIGHT : `${active.mediumHeight}px`
      element.style.maxHeight = 'none'

      const finish = () => {
        element.classList.remove('draw-gesture-snapping')
        if (snapRef.current === 'medium') {
          element.style.height = ''
          element.style.maxHeight = ''
          element.style.setProperty('--draw-drag-y', '0px')
        }
        snapTimer = 0
      }
      if (reducedMotion) finish()
      else snapTimer = window.setTimeout(finish, DRAW_SNAP_DURATION)
      gesture = null
    }
    const resize = () => {
      if (snapRef.current === 'full' && !gesture) {
        element.style.height = FULL_HEIGHT
        element.style.maxHeight = 'none'
      }
    }

    const touchStart = (event: TouchEvent) => {
      if (event.touches.length !== 1) return
      const touch = event.touches[0]
      const target = event.target
      if (!(target instanceof Element)) return
      const mainScroll = element.querySelector<HTMLElement>('[data-draw-scroll]')
      const fromHandle = !mainScroll || !mainScroll.contains(target)
      const activeElement = document.activeElement
      if (!fromHandle && activeElement instanceof Element && element.contains(activeElement) && activeElement.matches(SCROLLABLE_INPUT)) return

      clearSnapTimer()
      element.classList.remove('draw-gesture-snapping')
      const state = snapRef.current
      const startHeight = element.offsetHeight
      gesture = {
        pointerId: touch.identifier,
        startX: touch.clientX,
        anchorY: touch.clientY,
        lastY: touch.clientY,
        lastTime: performance.now(),
        velocity: 0,
        state,
        mode: 'native',
        nestedScroll: nearestScrollable(target, element),
        mainScroll,
        startHeight,
        mediumHeight: state === 'full' ? measuredHeight(element, 'auto', '') : startHeight,
        fullHeight: fullHeight(),
        height: startHeight,
        offset: 0,
        horizontal: false,
        nativeOnly: false,
        scrollHandoff: false,
        expandScrollBase: null,
      }
    }

    const touchMove = (event: TouchEvent) => {
      const active = gesture
      if (!active) return
      const touch = [...event.touches].find((item) => item.identifier === active.pointerId)
      if (!touch) return
      const totalDy = touch.clientY - active.anchorY
      if (active.mode === 'native' && Math.abs(touch.clientX - active.startX) > Math.abs(totalDy)) {
        active.horizontal = true
        return
      }
      if (active.horizontal || active.nativeOnly) return
      const now = performance.now()
      const elapsed = now - active.lastTime
      if (elapsed > 0) active.velocity = (touch.clientY - active.lastY) / elapsed
      active.lastY = touch.clientY
      active.lastTime = now

      const scrollTop = active.mainScroll?.scrollTop ?? 0
      if (active.mode === 'native') {
        if (!totalDy) return
        const nested = active.nestedScroll
        if (nested && nested !== active.mainScroll) {
          const canScrollNested = totalDy < 0
            ? nested.scrollTop + nested.clientHeight < nested.scrollHeight - 1
            : nested.scrollTop > 0
          if (canScrollNested) {
            active.nativeOnly = true
            return
          }
        }
        // After native scrolling reaches the top, re-anchor so earlier native
        // movement does not suddenly become sheet drag distance.
        if (totalDy > 0 && active.scrollHandoff && scrollTop <= 0) {
          active.anchorY = touch.clientY
          active.scrollHandoff = false
          active.velocity = 0
          return
        }
        if (totalDy > 0 && scrollTop > 0) active.scrollHandoff = true
        active.mode = resolveDragStart({
          state: active.state,
          dy: totalDy,
          scrollTop,
          scrollable: Boolean(active.mainScroll && active.mainScroll.scrollHeight > active.mainScroll.clientHeight + 1),
          fromHandle: !active.mainScroll || !active.mainScroll.contains(event.target as Element),
          canDismiss: canDismissRef.current,
        })
        if (active.mode === 'native') return
        if (active.state === 'medium') active.mediumHeight = active.startHeight
      }

      // React touch listeners are passive; this native listener must be able to
      // suppress Safari's rubber-band scrolling once the sheet takes over.
      event.preventDefault()
      const move = resolveDragMove({
        mode: active.mode,
        dy: totalDy,
        startHeight: active.startHeight,
        mediumHeight: active.mediumHeight,
        fullHeight: active.fullHeight,
      })
      active.height = move.height ?? active.startHeight
      active.offset = move.offset ?? 0
      let scrollTopAfter: number | undefined
      if (active.mode === 'expand' && active.height >= active.fullHeight && totalDy < 0) {
        if (active.expandScrollBase === null) active.expandScrollBase = active.mainScroll?.scrollTop ?? 0
        const overflow = -totalDy - (active.fullHeight - active.startHeight)
        scrollTopAfter = active.expandScrollBase + Math.max(0, overflow)
      } else {
        active.expandScrollBase = null
      }
      schedule({ height: active.height, offset: active.offset, scrollTop: scrollTopAfter })
    }

    const touchEnd = (event: TouchEvent) => {
      const active = gesture
      if (!active || ![...event.changedTouches].some((touch) => touch.identifier === active.pointerId)) return
      if (active.mode === 'native') {
        gesture = null
        return
      }
      const progress = active.mode === 'dismiss'
        ? active.offset / Math.max(1, active.startHeight)
        : active.mode === 'expand'
          ? (active.height - active.startHeight) / Math.max(1, active.fullHeight - active.startHeight)
          : (active.startHeight - active.height) / Math.max(1, active.startHeight - active.mediumHeight)
      settle(resolveRelease({ mode: active.mode, progress: Math.max(0, progress), velocity: active.velocity, canDismiss: canDismissRef.current }))
    }

    element.addEventListener('touchstart', touchStart, { passive: true })
    element.addEventListener('touchmove', touchMove, { passive: false })
    element.addEventListener('touchend', touchEnd)
    element.addEventListener('touchcancel', touchEnd)
    window.addEventListener('resize', resize)
    window.visualViewport?.addEventListener('resize', resize)
    return () => {
      element.removeEventListener('touchstart', touchStart)
      element.removeEventListener('touchmove', touchMove)
      element.removeEventListener('touchend', touchEnd)
      element.removeEventListener('touchcancel', touchEnd)
      window.removeEventListener('resize', resize)
      window.visualViewport?.removeEventListener('resize', resize)
      if (frame) cancelAnimationFrame(frame)
      clearSnapTimer()
      element.classList.remove('draw-gesture-snapping')
      element.style.height = ''
      element.style.maxHeight = ''
      element.style.removeProperty('--draw-drag-y')
      snapRef.current = 'medium'
    }
  }, [element, enabled, requestClose])
}
