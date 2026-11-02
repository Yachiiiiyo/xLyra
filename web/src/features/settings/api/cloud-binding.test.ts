import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  cancelCloudPairing,
  disconnectCloudBinding,
  fetchCloudBinding,
  cloudConsoleURL,
  pairCloudBinding,
  parseCloudBinding,
} from '@/features/settings/api/cloud-binding'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('parseCloudBinding', () => {
  it('fills missing fields and rejects an unknown status', () => {
    expect(parseCloudBinding({})).toEqual({
      status: 'unbound',
      instance_name: '',
      user_display_name: '',
      cloud_url: '',
      bound_at: null,
      last_sync_at: null,
      last_error: '',
      last_error_at: null,
      pairing: null,
      max_poll_seconds: 0,
    })
    expect(parseCloudBinding({
      status: 'nope',
      instance_name: 12,
      pairing: { expires_at: '2026-01-01T00:00:00Z' },
      max_poll_seconds: '30',
    }).status).toBe('unbound')
    expect(parseCloudBinding({ status: 'pairing', pairing: {} }).pairing).toBeNull()
  })

  it('keeps a pairing code and nullable timestamps', () => {
    expect(parseCloudBinding({
      status: 'pairing',
      instance_name: 'edge-1',
      cloud_url: 'https://xlyra.yachiyo.im',
      bound_at: null,
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2026-10-26T10:00:00Z' },
      max_poll_seconds: 120,
    })).toMatchObject({
      status: 'pairing',
      instance_name: 'edge-1',
      cloud_url: 'https://xlyra.yachiyo.im',
      bound_at: null,
      pairing: { user_code: 'GYD4-LMGT', expires_at: '2026-10-26T10:00:00Z' },
      max_poll_seconds: 120,
    })
  })
})

describe('cloud binding requests', () => {
  it('loads and tolerates a partial payload', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'bound' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await fetchCloudBinding()

    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/cloud-binding')
    expect(fetchMock.mock.calls[0][1].method ?? 'GET').toBe('GET')
    expect(result.status).toBe('bound')
    expect(result.instance_name).toBe('')
    expect(result.pairing).toBeNull()
  })

  it('posts a pair request without a name by default', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'pairing' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    const result = await pairCloudBinding()

    const [, request] = fetchMock.mock.calls[0]
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/cloud-binding/pair')
    expect(request.method).toBe('POST')
    expect(JSON.parse(request.body)).toEqual({})
    expect(result.status).toBe('pairing')
  })

  it('posts an optional instance name', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'pairing' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await pairCloudBinding('  edge-1  ')

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ name: 'edge-1' })
  })

  it('cancels pairing and disconnects with DELETE', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await cancelCloudPairing()
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/v1/cloud-binding/pair')
    expect(fetchMock.mock.calls[0][1].method).toBe('DELETE')

    await disconnectCloudBinding()
    expect(String(fetchMock.mock.calls[1][0])).toBe('/api/v1/cloud-binding')
    expect(fetchMock.mock.calls[1][1].method).toBe('DELETE')
  })
})

describe('cloudConsoleURL', () => {
  it('allows https and local http, and rejects other targets', () => {
    expect(cloudConsoleURL('https://xlyra.yachiyo.im')).toBe('https://xlyra.yachiyo.im/console/')
    expect(cloudConsoleURL('https://xlyra.yachiyo.im/', 'GYD4-LMGT')).toBe('https://xlyra.yachiyo.im/console/#connect=GYD4-LMGT')
    expect(cloudConsoleURL('http://localhost:8787', 'GYD4-LMGT')).toBe('http://localhost:8787/console/#connect=GYD4-LMGT')
    expect(cloudConsoleURL('http://127.0.0.1:8787')).toBe('http://127.0.0.1:8787/console/')
    expect(cloudConsoleURL('http://evil.example', 'GYD4-LMGT')).toBeNull()
    expect(cloudConsoleURL('javascript:alert(1)', 'GYD4-LMGT')).toBeNull()
    expect(cloudConsoleURL('http://localhost.evil.com')).toBeNull()
    expect(cloudConsoleURL('https://user:pass@xlyra.yachiyo.im')).toBeNull()
    expect(cloudConsoleURL('')).toBeNull()
  })
})
