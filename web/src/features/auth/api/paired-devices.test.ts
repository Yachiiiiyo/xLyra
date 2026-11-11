import { afterEach, describe, expect, it, vi } from 'vitest'

import { listDevices, parseDevicePush, unregisteredDevicePush, type DeviceItem } from '@/features/auth/api/paired-devices'

const apiFetch = vi.hoisted(() => vi.fn())

vi.mock('@/lib/http', () => ({ apiFetch }))

afterEach(() => {
  apiFetch.mockReset()
})

function push(overrides: Record<string, unknown> = {}) {
  return {
    registered: true,
    environment: 'sandbox',
    state: 'ok',
    prefs: { approval: true, question: true, done: true, error: true },
    registered_at: '2026-01-02T00:00:00Z',
    ...overrides,
  }
}

describe('parseDevicePush', () => {
  it('保留合法的推送状态', () => {
    expect(parseDevicePush(push())).toEqual(push())
  })

  it('没有 push 或字段格式不对时视为未登记', () => {
    const fallback = unregisteredDevicePush()
    expect(parseDevicePush(undefined)).toEqual(fallback)
    expect(parseDevicePush(null)).toEqual(fallback)
    expect(parseDevicePush('nope')).toEqual(fallback)
    expect(parseDevicePush(push({ registered: 'yes' }))).toEqual(fallback)
    expect(parseDevicePush(push({ environment: 'staging' }))).toEqual(fallback)
    expect(parseDevicePush(push({ state: 1 }))).toEqual(fallback)
    expect(parseDevicePush(push({ prefs: { approval: true } }))).toEqual(fallback)
    expect(parseDevicePush(push({ prefs: null }))).toEqual(fallback)
    expect(parseDevicePush(push({ registered_at: 12 }))).toEqual(fallback)
  })

  it('未知 state 当作空串，其它字段保留', () => {
    expect(parseDevicePush(push({ state: 'stale' }))).toEqual(push({ state: '' }))
  })
})

describe('listDevices', () => {
  it('解析每台设备的 push，旧数据补成未登记', async () => {
    const legacy = {
      id: '11111111-1111-1111-1111-111111111111',
      name: '小夜的 iPhone',
      platform: 'iOS',
      created_at: '2025-01-01T00:00:00Z',
      current: false,
    }
    apiFetch.mockResolvedValue({ data: [legacy, { ...legacy, id: '222', push: push() }] })
    const devices = await listDevices()
    expect(devices[0]).toEqual({
      ...legacy,
      last_seen_at: undefined,
      last_ip: undefined,
      push: unregisteredDevicePush(),
    } satisfies DeviceItem)
    expect(devices[1]?.push).toEqual(push())
  })
})
