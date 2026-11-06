import { drawStarfield } from '@/lib/draw-starfield'

/**
 * 全局启动遮罩：接管 HTML 里静态的 #boot-splash（星空背景 + 呼吸 logo），
 * 覆盖应用启动阶段（i18n / 首次鉴权 / 首屏路由就绪），之后淡出并移除。
 */

/** 从导航起点算起至少展示的时长 */
const MIN_DISPLAY_MS = 300
/** 兜底：超过这个时长强制收起，避免卡死遮罩 */
const MAX_WAIT_MS = 10_000
const FADE_OUT_MS = 400
const RESIZE_DEBOUNCE_MS = 120

let resizeTimer = 0
let onResize: (() => void) | null = null
let dismissing = false

export function startBootSplash() {
  const splash = document.getElementById('boot-splash')
  const canvas = splash?.querySelector('canvas')
  if (!splash || !canvas) return

  const draw = () => drawStarfield(canvas)
  draw()
  onResize = () => {
    window.clearTimeout(resizeTimer)
    resizeTimer = window.setTimeout(draw, RESIZE_DEBOUNCE_MS)
  }
  window.addEventListener('resize', onResize)
}

export function dismissBootSplash() {
  if (onResize) {
    window.clearTimeout(resizeTimer)
    window.removeEventListener('resize', onResize)
    onResize = null
  }
  if (dismissing) return
  const splash = document.getElementById('boot-splash')
  if (!splash) return
  dismissing = true
  splash.style.opacity = '0'
  window.setTimeout(() => splash.remove(), FADE_OUT_MS)
}

/** 可注入的计时器，便于单测用 fake timers 驱动 */
export type BootSplashTimers = {
  setTimeout: (handler: () => void, ms: number) => number
  clearTimeout: (id: number) => void
  requestAnimationFrame: (callback: () => void) => number
  cancelAnimationFrame: (id: number) => void
}

const browserTimers: BootSplashTimers = {
  setTimeout: (handler, ms) => window.setTimeout(handler, ms),
  clearTimeout: (id) => window.clearTimeout(id),
  requestAnimationFrame: (callback) => window.requestAnimationFrame(callback),
  cancelAnimationFrame: (id) => window.cancelAnimationFrame(id),
}

export type WaitBootSplashReadyOptions = {
  /** 是否满足全部就绪条件 */
  isReady: () => boolean
  /** 订阅条件变化，收到通知时重新检查 isReady；返回取消订阅函数 */
  subscribe: (notify: () => void) => () => void
  now?: () => number
  timers?: BootSplashTimers
  minDisplayMs?: number
  maxWaitMs?: number
}

/**
 * 等待启动就绪：条件满足后再等两帧（保证首屏画过一次），并保证从导航起点
 * （now() 的起点）算起至少展示 minDisplayMs；超过 maxWaitMs 强制返回 'timeout'。
 */
export function waitBootSplashReady(options: WaitBootSplashReadyOptions): Promise<'ready' | 'timeout'> {
  const {
    isReady,
    subscribe,
    now = () => performance.now(),
    timers = browserTimers,
    minDisplayMs = MIN_DISPLAY_MS,
    maxWaitMs = MAX_WAIT_MS,
  } = options
  const startedAt = now()

  return new Promise((resolve) => {
    let settled = false
    let timeoutId = 0
    let delayId = 0
    let frameId = 0
    let unsubscribe: (() => void) | null = null

    const finish = (reason: 'ready' | 'timeout') => {
      if (settled) return
      settled = true
      timers.clearTimeout(timeoutId)
      timers.clearTimeout(delayId)
      timers.cancelAnimationFrame(frameId)
      unsubscribe?.()
      unsubscribe = null
      resolve(reason)
    }

    const settleAfterMinDisplay = () => {
      const remaining = minDisplayMs - (now() - startedAt)
      if (remaining > 0) {
        delayId = timers.setTimeout(() => finish('ready'), remaining)
      } else {
        finish('ready')
      }
    }

    const notify = () => {
      if (settled || !isReady()) return
      unsubscribe?.()
      unsubscribe = null
      // 条件满足后再等两帧
      frameId = timers.requestAnimationFrame(() => {
        frameId = timers.requestAnimationFrame(settleAfterMinDisplay)
      })
    }

    timeoutId = timers.setTimeout(() => finish('timeout'), maxWaitMs)
    unsubscribe = subscribe(notify)
    notify()
  })
}
