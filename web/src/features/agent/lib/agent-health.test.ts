import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchAgentHealth } from '@/features/agent/api/agent'
import { isAgentAvailable } from '@/features/agent/lib/use-agent-availability'

function healthResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('fetchAgentHealth', () => {
  it('distinguishes a missing agent from an unhealthy one', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(healthResponse(404, { message: 'not found' })))
    await expect(fetchAgentHealth()).resolves.toEqual({ status: 'missing' })

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(healthResponse(503, {
      status: 'unhealthy',
      reason: 'Pi 未就绪',
      bash_enabled: false,
    })))
    await expect(fetchAgentHealth()).resolves.toMatchObject({ status: 'unhealthy', reason: 'Pi 未就绪' })
  })

  it('keeps network failures indistinguishable from a temporarily unavailable health response', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await expect(fetchAgentHealth()).resolves.toBeNull()
  })
})

describe('isAgentAvailable', () => {
  it('hides only an explicit 404 response', () => {
    expect(isAgentAvailable({ status: 'missing' })).toBe(false)
    expect(isAgentAvailable({ status: 'unhealthy' })).toBe(true)
    expect(isAgentAvailable(null)).toBe(true)
    expect(isAgentAvailable(undefined)).toBe(true)
  })
})
