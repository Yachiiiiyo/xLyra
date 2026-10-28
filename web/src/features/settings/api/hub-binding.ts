import { apiFetch } from '@/lib/http'

export type HubBindingStatus = 'unbound' | 'pairing' | 'bound' | 'revoked'

export type HubBindingPairing = {
  user_code: string
  expires_at: string
}

export type HubBinding = {
  status: HubBindingStatus
  instance_name: string
  user_display_name: string
  hub_url: string
  bound_at: string | null
  last_sync_at: string | null
  last_error: string
  last_error_at: string | null
  pairing: HubBindingPairing | null
  max_poll_seconds: number
}

const hubBindingStatuses = new Set<HubBindingStatus>(['unbound', 'pairing', 'bound', 'revoked'])

export const hubBindingQueryKeys = {
  all: ['settings', 'hub-binding'] as const,
}

export function parseHubBinding(raw: unknown): HubBinding {
  const value = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
  const status = typeof value.status === 'string' && hubBindingStatuses.has(value.status as HubBindingStatus)
    ? value.status as HubBindingStatus
    : 'unbound'
  return {
    status,
    instance_name: stringField(value.instance_name),
    user_display_name: stringField(value.user_display_name),
    hub_url: stringField(value.hub_url),
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

export async function fetchHubBinding(signal?: AbortSignal) {
  return parseHubBinding(await apiFetch<unknown>('/api/v1/hub-binding', { signal }))
}

export async function pairHubBinding(name?: string) {
  const trimmed = name?.trim()
  return parseHubBinding(await apiFetch<unknown>('/api/v1/hub-binding/pair', {
    method: 'POST',
    body: trimmed ? { name: trimmed } : {},
  }))
}

export async function cancelHubPairing() {
  await apiFetch<void>('/api/v1/hub-binding/pair', { method: 'DELETE' })
}

export async function disconnectHubBinding() {
  await apiFetch<void>('/api/v1/hub-binding', { method: 'DELETE' })
}

/** 只允许 https，以及本地开发的 http://localhost / 127.0.0.1。 */
export function hubConsoleURL(hubURL: string, userCode?: string) {
  let parsed: URL
  try {
    parsed = new URL(hubURL.trim())
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

function parsePairing(raw: unknown): HubBindingPairing | null {
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
