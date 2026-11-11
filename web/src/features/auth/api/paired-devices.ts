import { apiFetch } from '@/lib/http'

export type DevicePushEnvironment = '' | 'sandbox' | 'production'
export type DevicePushState = '' | 'ok' | 'invalid' | 'error'

export type DevicePushPrefs = {
  approval: boolean
  question: boolean
  done: boolean
  error: boolean
}

export type DevicePush = {
  registered: boolean
  environment: DevicePushEnvironment
  state: DevicePushState
  prefs: DevicePushPrefs
  registered_at: string | null
}

export type DeviceItem = {
  id: string
  name: string
  platform: string
  created_at: string
  last_seen_at?: string | null
  last_ip?: string | null
  current: boolean
  push?: DevicePush
}

const pushEnvironments = new Set<DevicePushEnvironment>(['', 'sandbox', 'production'])
const pushStates = new Set<DevicePushState>(['', 'ok', 'invalid', 'error'])
const pushPrefKeys = ['approval', 'question', 'done', 'error'] as const

export function unregisteredDevicePush(): DevicePush {
  return {
    registered: false,
    environment: '',
    state: '',
    prefs: { approval: false, question: false, done: false, error: false },
    registered_at: null,
  }
}

/** 旧后端没有 push、或字段格式不对时视为未登记。未知 state 当作空串。 */
export function parseDevicePush(raw: unknown): DevicePush {
  if (!raw || typeof raw !== 'object') return unregisteredDevicePush()
  const value = raw as Record<string, unknown>
  if (typeof value.registered !== 'boolean') return unregisteredDevicePush()
  if (typeof value.environment !== 'string' || !pushEnvironments.has(value.environment as DevicePushEnvironment)) {
    return unregisteredDevicePush()
  }
  if (typeof value.state !== 'string') return unregisteredDevicePush()
  const state = pushStates.has(value.state as DevicePushState) ? value.state as DevicePushState : ''
  const prefs = parsePushPrefs(value.prefs)
  if (!prefs) return unregisteredDevicePush()
  if (value.registered_at !== null && typeof value.registered_at !== 'string') return unregisteredDevicePush()
  return {
    registered: value.registered,
    environment: value.environment as DevicePushEnvironment,
    state,
    prefs,
    registered_at: value.registered_at,
  }
}

export function parseDeviceItem(raw: unknown): DeviceItem {
  const value = raw && typeof raw === 'object' ? raw as Partial<DeviceItem> & { push?: unknown } : {}
  return {
    id: typeof value.id === 'string' ? value.id : '',
    name: typeof value.name === 'string' ? value.name : '',
    platform: typeof value.platform === 'string' ? value.platform : '',
    created_at: typeof value.created_at === 'string' ? value.created_at : '',
    last_seen_at: value.last_seen_at,
    last_ip: value.last_ip,
    current: value.current === true,
    push: parseDevicePush(value.push),
  }
}

export function isIOSPlatform(platform: string) {
  return platform.trim().toLowerCase().startsWith('ios')
}

function parsePushPrefs(raw: unknown): DevicePushPrefs | null {
  if (!raw || typeof raw !== 'object') return null
  const value = raw as Record<string, unknown>
  const prefs = {} as DevicePushPrefs
  for (const key of pushPrefKeys) {
    if (typeof value[key] !== 'boolean') return null
    prefs[key] = value[key]
  }
  return prefs
}

export type DevicePairing = {
  pairing_id: string
  code: string
  expires_at: string
}

export const devicesQueryKeys = {
  all: ['devices'] as const,
  list: () => [...devicesQueryKeys.all, 'list'] as const,
}

export async function listDevices() {
  const payload = await apiFetch<{ data: DeviceItem[] }>('/api/v1/devices')
  if (!Array.isArray(payload.data)) return payload.data
  return payload.data.map(parseDeviceItem)
}

export async function createDevicePairing() {
  const payload = await apiFetch<{ data: DevicePairing }>('/api/v1/devices/pairings', { method: 'POST' })
  return payload.data
}

export async function revokeDevice(deviceId: string) {
  return apiFetch<void>(`/api/v1/devices/${deviceId}`, { method: 'DELETE' })
}

export async function revokeAllDevices() {
  return apiFetch<void>('/api/v1/devices/revoke-all', { method: 'POST' })
}
