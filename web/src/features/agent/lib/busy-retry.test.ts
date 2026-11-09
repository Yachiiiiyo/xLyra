import { describe, expect, it } from 'vitest'
import { APIError } from '@/lib/http'
import { BUSY_RETRY_MAX, BUSY_RETRY_WINDOW_MS, shouldRetryBusySend } from '@/features/agent/lib/busy-retry'

const busy = new APIError('会话已有进行中的 run', 409, 'agent_busy')

describe('shouldRetryBusySend', () => {
  it('只对繁忙错误(409/agent_busy)且刚收到终态的情况重试', () => {
    expect(shouldRetryBusySend(busy, 100, 0)).toBe(true)
    expect(shouldRetryBusySend(new APIError('busy', 400, 'agent_busy'), 100, 0)).toBe(true)
    expect(shouldRetryBusySend(new APIError('server error', 500), 100, 0)).toBe(false)
    expect(shouldRetryBusySend(new Error('network'), 100, 0)).toBe(false)
  })

  it('没有终态记录或超出重试窗口时不重试', () => {
    expect(shouldRetryBusySend(busy, null, 0)).toBe(false)
    expect(shouldRetryBusySend(busy, BUSY_RETRY_WINDOW_MS, 0)).toBe(false)
    expect(shouldRetryBusySend(busy, BUSY_RETRY_WINDOW_MS - 1, 0)).toBe(true)
    expect(shouldRetryBusySend(busy, -1, 0)).toBe(false)
  })

  it('超过最大重试次数后不再重试', () => {
    expect(shouldRetryBusySend(busy, 100, BUSY_RETRY_MAX - 1)).toBe(true)
    expect(shouldRetryBusySend(busy, 100, BUSY_RETRY_MAX)).toBe(false)
  })
})
