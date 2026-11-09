import { act } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAgentPrefetch } from '@/app/use-agent-prefetch'

const useAgentAvailability = vi.hoisted(() => vi.fn())
const loadNamespaces = vi.hoisted(() => vi.fn())
const agentModuleLoaded = vi.hoisted(() => vi.fn())

vi.mock('@/features/agent/lib/use-agent-availability', () => ({ useAgentAvailability }))
vi.mock('@/locales/i18n', () => ({ default: { loadNamespaces } }))
vi.mock('@/routes/agent', () => {
  agentModuleLoaded()
  return {}
})

let pendingIdleCallback: IdleRequestCallback | undefined
const requestIdleCallback = vi.fn((callback: IdleRequestCallback) => {
  pendingIdleCallback = callback
  return 1
})
const cancelIdleCallback = vi.fn()
const windowStub = {
  requestIdleCallback,
  cancelIdleCallback,
  HTMLIFrameElement: class {},
  setTimeout: (handler: () => void, ms: number) => setTimeout(handler, ms),
  clearTimeout: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
}

beforeEach(() => {
  pendingIdleCallback = undefined
  requestIdleCallback.mockClear()
  cancelIdleCallback.mockClear()
  agentModuleLoaded.mockClear()
  useAgentAvailability.mockReturnValue(true)
  loadNamespaces.mockResolvedValue([])
  vi.stubGlobal('window', windowStub)
  vi.stubGlobal('navigator', { connection: { saveData: false } })
  vi.stubGlobal('HTMLIFrameElement', class {})
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
})

afterEach(() => {
  useAgentAvailability.mockReset()
  loadNamespaces.mockReset()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

function Probe() {
  useAgentPrefetch()
  return null
}

describe('useAgentPrefetch', () => {
  it('Agent 可用时，空闲回调加载路由和所需命名空间且只调度一次', async () => {
    const mounted = await mountPrefetch()
    try {
      expect(requestIdleCallback).toHaveBeenCalledOnce()
      expect(agentModuleLoaded).not.toHaveBeenCalled()
      expect(loadNamespaces).not.toHaveBeenCalled()

      const callback = pendingIdleCallback
      await act(async () => {
        callback?.({ didTimeout: false, timeRemaining: () => 50 })
        await Promise.resolve()
      })

      expect(agentModuleLoaded).toHaveBeenCalledOnce()
      expect(loadNamespaces).toHaveBeenCalledWith(['agent', 'chat', 'api-keys', 'components'])
      await mounted.rerender()
      expect(requestIdleCallback).toHaveBeenCalledOnce()
      expect(loadNamespaces).toHaveBeenCalledOnce()
    } finally {
      await mounted.unmount()
    }
  })

  it('Agent 不可用时不调度预取', async () => {
    useAgentAvailability.mockReturnValue(false)
    const mounted = await mountPrefetch()
    try {
      expect(requestIdleCallback).not.toHaveBeenCalled()
      expect(agentModuleLoaded).not.toHaveBeenCalled()
      expect(loadNamespaces).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('省流量模式下不调度预取', async () => {
    vi.stubGlobal('navigator', { connection: { saveData: true } })
    const mounted = await mountPrefetch()
    try {
      expect(requestIdleCallback).not.toHaveBeenCalled()
      expect(agentModuleLoaded).not.toHaveBeenCalled()
      expect(loadNamespaces).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('不支持 requestIdleCallback 时延迟两秒预取', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('window', { ...windowStub, requestIdleCallback: undefined })
    const mounted = await mountPrefetch()
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1999)
      })
      expect(loadNamespaces).not.toHaveBeenCalled()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })
      expect(loadNamespaces).toHaveBeenCalledWith(['agent', 'chat', 'api-keys', 'components'])
    } finally {
      await mounted.unmount()
    }
  })

  it('卸载时取消尚未执行的空闲回调', async () => {
    const mounted = await mountPrefetch()
    await mounted.unmount()
    expect(cancelIdleCallback).toHaveBeenCalledWith(1)
  })
})

async function mountPrefetch() {
  const documentStub = {
    nodeType: 9,
    nodeName: '#document',
    defaultView: globalThis.window,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }
  const container = {
    nodeType: 1,
    nodeName: 'DIV',
    tagName: 'DIV',
    ownerDocument: documentStub,
    namespaceURI: 'http://www.w3.org/1999/xhtml',
    textContent: '',
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }
  vi.stubGlobal('document', documentStub)
  const { createRoot } = await import('react-dom/client')
  const root = createRoot(container as unknown as Element)
  const render = async () => {
    await act(async () => {
      root.render(<Probe />)
    })
  }
  await render()
  return {
    rerender: render,
    async unmount() {
      await act(async () => {
        root.unmount()
      })
    },
  }
}
