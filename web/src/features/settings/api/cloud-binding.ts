import { apiFetch } from '@/lib/http'

export type CloudBindingStatus = 'unbound' | 'pairing' | 'bound' | 'revoked'

export type CloudBindingPairing = {
  user_code: string
  expires_at: string
}

export type CloudBinding = {
  status: CloudBindingStatus
  instance_name: string
  user_display_name: string
  cloud_url: string
  bound_at: string | null
  last_sync_at: string | null
  last_error: string
  last_error_at: string | null
  pairing: CloudBindingPairing | null
  max_poll_seconds: number
}

const cloudBindingStatuses = new Set<CloudBindingStatus>(['unbound', 'pairing', 'bound', 'revoked'])

export const cloudBindingQueryKeys = {
  all: ['settings', 'cloud-binding'] as const,
}

export function parseCloudBinding(raw: unknown): CloudBinding {
  const value = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
  const status = typeof value.status === 'string' && cloudBindingStatuses.has(value.status as CloudBindingStatus)
    ? value.status as CloudBindingStatus
    : 'unbound'
  return {
    status,
    instance_name: stringField(value.instance_name),
    user_display_name: stringField(value.user_display_name),
    cloud_url: stringField(value.cloud_url),
    bound_at: nullableString(value.bound_at),
    last_sync_at: nullableString(value.last_sync_at),
    last_error: stringField(value.last_error),
    last_error_at: nullableString(value.last_error_at),
    pairing: parsePairing(value.pairing),
    max_poll_seconds: typeof value.max_poll_seconds === 'number' && Number.isFinite(value.max_poll_seconds)
      ? value.max_poll_seconds
      : 0,
  }
}

export async function fetchCloudBinding(signal?: AbortSignal) {
  return parseCloudBinding(await apiFetch<unknown>('/api/v1/cloud-binding', { signal }))
}

export async function pairCloudBinding(name?: string) {
  const trimmed = name?.trim()
  return parseCloudBinding(await apiFetch<unknown>('/api/v1/cloud-binding/pair', {
    method: 'POST',
    body: trimmed ? { name: trimmed } : {},
  }))
}

export async function cancelCloudPairing() {
  await apiFetch<void>('/api/v1/cloud-binding/pair', { method: 'DELETE' })
}

export async function disconnectCloudBinding() {
  await apiFetch<void>('/api/v1/cloud-binding', { method: 'DELETE' })
}

/** 只允许 https，以及本地开发的 http://localhost / 127.0.0.1。 */
export function cloudConsoleURL(cloudURL: string, userCode?: string) {
  let parsed: URL
  try {
    parsed = new URL(cloudURL.trim())
  } catch {
    return null
  }
  const localHTTP = parsed.protocol === 'http:' && (parsed.hostname === 'localhost' || parsed.hostname === '127.0.0.1')
  if (parsed.protocol !== 'https:' && !localHTTP) return null
  if (parsed.username || parsed.password) return null
  const base = `${parsed.origin}${parsed.pathname}`.replace(/\/+$/, '')
  let consoleURL: URL
  try {
    consoleURL = new URL(`${base}/console/`)
  } catch {
    return null
  }
  if (consoleURL.origin !== parsed.origin || consoleURL.protocol !== parsed.protocol) return null
  if (userCode) consoleURL.hash = `connect=${encodeURIComponent(userCode)}`
  return consoleURL.toString()
}

function parsePairing(raw: unknown): CloudBindingPairing | null {
  if (!raw || typeof raw !== 'object') return null
  const value = raw as Record<string, unknown>
  const userCode = stringField(value.user_code)
  if (!userCode) return null
  return {
    user_code: userCode,
    expires_at: stringField(value.expires_at),
  }
}

function stringField(value: unknown) {
  return typeof value === 'string' ? value : ''
}

function nullableString(value: unknown) {
  if (typeof value !== 'string') return null
  return value
}
