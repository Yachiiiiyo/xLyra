export const DRAW_TOP_GAP = 12
export const DRAW_SNAP_PROGRESS = 0.25
export const DRAW_SNAP_VELOCITY = 0.5
export const DRAW_DISMISS_PROGRESS = 0.3
export const DRAW_DISMISS_VELOCITY = 0.8
export const DRAW_SNAP_DURATION = 280

export type DrawSnap = 'medium' | 'full'
export type DrawDragMode = 'native' | 'expand' | 'shrink' | 'dismiss'
export type DrawRelease = 'snap-medium' | 'snap-full' | 'dismiss' | 'rebound'

type DragStartInput = {
  state: DrawSnap
  dy: number
  scrollTop: number
  scrollable: boolean
  fromHandle: boolean
  canDismiss: boolean
}

export function resolveDragStart({ state, dy, scrollTop, scrollable, fromHandle, canDismiss }: DragStartInput): DrawDragMode {
  if (dy < 0) {
    if (state === 'medium' && scrollable) return 'expand'
    return 'native'
  }
  if (dy === 0) return 'native'
  if (state === 'full') return fromHandle || scrollTop <= 0 ? 'shrink' : 'native'
  return fromHandle || scrollTop <= 0 ? (canDismiss ? 'dismiss' : 'native') : 'native'
}

type DragMoveInput = {
  mode: Exclude<DrawDragMode, 'native'>
  dy: number
  startHeight: number
  mediumHeight: number
  fullHeight: number
}

export function resolveDragMove({ mode, dy, startHeight, mediumHeight, fullHeight }: DragMoveInput) {
  if (mode === 'dismiss') return { offset: Math.max(0, dy) }
  const height = startHeight - dy
  return { height: Math.min(fullHeight, Math.max(mediumHeight, height)) }
}

type ReleaseInput = {
  mode: Exclude<DrawDragMode, 'native'>
  progress: number
  velocity: number
  canDismiss: boolean
}

export function resolveRelease({ mode, progress, velocity, canDismiss }: ReleaseInput): DrawRelease {
  if (mode === 'dismiss') {
    return canDismiss && (progress > DRAW_DISMISS_PROGRESS || velocity > DRAW_DISMISS_VELOCITY) ? 'dismiss' : 'rebound'
  }
  const pastThreshold = mode === 'expand'
    ? progress > DRAW_SNAP_PROGRESS || velocity < -DRAW_SNAP_VELOCITY
    : progress > DRAW_SNAP_PROGRESS || velocity > DRAW_SNAP_VELOCITY
  if (!pastThreshold) return 'rebound'
  return mode === 'expand' ? 'snap-full' : 'snap-medium'
}
