import { afterEach, describe, expect, it, vi } from 'vitest'

import { dismissBootSplash, waitBootSplashReady, type BootSplashTimers } from '@/lib/boot-splash'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

/** 用 fake timers 驱动的计时器,rAF 折算成 16ms,计时器触发时同步推进受控时钟 */
function createHarness() {
  let clock = 0
  const timers: BootSplashTimers = {
    setTimeout: (handler, ms) =>
      setTimeout(() => {
        clock += ms
        handler()
      }, ms) as unknown as number,
    clearTimeout: (id) => clearTimeout(id),
    requestAnimationFrame: (callback) =>
      setTimeout(() => {
        clock += 16
        callback()
      }, 16) as unknown as number,
    cancelAnimationFrame: (id) => clearTimeout(id),
  }
  return { timers, now: () => clock }
}

/** 订阅条件变化:记录 notify,由测试手动触发 */
function createSubscription() {
  const listeners: Array<() => void> = []
  const subscribe = (notify: () => void) => {
    listeners.push(notify)
    return () => {
      listeners.splice(listeners.indexOf(notify), 1)
    }
  }
  return { subscribe, notify: () => listeners.forEach((listener) => listener()) }
}

async function settle(promise: Promise<'ready' | 'timeout'>) {
  let settledWith: 'ready' | 'timeout' | undefined
  void promise.then((reason) => {
    settledWith = reason
  })
  return {
    get value() {
      return settledWith
    },
  }
}

describe('waitBootSplashReady', () => {
  it('条件立即满足时也要展示满最短时长才就绪', async () => {
    vi.useFakeTimers()
    const { timers, now } = createHarness()
    const { subscribe, notify } = createSubscription()
    const watcher = await settle(waitBootSplashReady({ isReady: () => true, subscribe, now, timers }))

    notify()
    await vi.advanceTimersByTimeAsync(299)
    expect(watcher.value).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(watcher.value).toBe('ready')
  })

  it('条件迟迟不满足时按兜底超时强制就绪', async () => {
    vi.useFakeTimers()
    const { timers, now } = createHarness()
    const { subscribe } = createSubscription()
    const watcher = await settle(waitBootSplashReady({ isReady: () => false, subscribe, now, timers }))

    await vi.advanceTimersByTimeAsync(9999)
    expect(watcher.value).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(watcher.value).toBe('timeout')
  })

  it('条件未满足时不会 dismiss,满足并通知后才就绪', async () => {
    vi.useFakeTimers()
    const { timers, now } = createHarness()
    const { subscribe, notify } = createSubscription()
    let ready = false
    const watcher = await settle(waitBootSplashReady({ isReady: () => ready, subscribe, now, timers }))

    notify()
    await vi.advanceTimersByTimeAsync(5000)
    expect(watcher.value).toBeUndefined()

    ready = true
    notify()
    await vi.advanceTimersByTimeAsync(300)
    expect(watcher.value).toBe('ready')
  })

  it('迟到时不再补最短展示时间', async () => {
    vi.useFakeTimers()
    const { timers, now } = createHarness()
    const { subscribe, notify } = createSubscription()
    const watcher = await settle(waitBootSplashReady({ isReady: () => true, subscribe, now, timers }))

    // 条件在启动很久之后才被通知
    await vi.advanceTimersByTimeAsync(3000)
    notify()
    await vi.advanceTimersByTimeAsync(32)
    expect(watcher.value).toBe('ready')
  })
})

describe('dismissBootSplash', () => {
  it('重复调用幂等:只移除一次且不报错', async () => {
    vi.useFakeTimers()
    let attached = true
    const remove = vi.fn(() => {
      attached = false
    })
    const splash = { style: {} as Record<string, string>, remove }
    vi.stubGlobal('window', {
      setTimeout: (handler: () => void, ms: number) => setTimeout(handler, ms) as unknown as number,
      clearTimeout: (id: number) => clearTimeout(id),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })
    vi.stubGlobal('document', {
      getElementById: (id: string) => (id === 'boot-splash' && attached ? splash : null),
    })

    dismissBootSplash()
    dismissBootSplash()
    await vi.advanceTimersByTimeAsync(400)
    dismissBootSplash()

    expect(remove).toHaveBeenCalledTimes(1)
    expect(splash.style.opacity).toBe('0')
  })
})
