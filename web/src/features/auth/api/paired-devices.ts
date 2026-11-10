import { apiFetch } from '@/lib/http'

export type DeviceItem = {
  id: string
  name: string
  platform: string
  created_at: string
  last_seen_at?: string | null
  last_ip?: string | null
  current: boolean
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
  return payload.data
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
