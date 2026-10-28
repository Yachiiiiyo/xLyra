import type { JSPluginVersion } from '@/features/settings/api/js-plugins'
import { apiFetch } from '@/lib/http'

export type PluginMarketSettings = {
  enabled: boolean
  source_url: string
  proxy_id: string
  auto_update: boolean
  index_sync_hours: number
}

export type PluginMarketDeveloper = {
  namespace: string
  display_name: string
}

export type PluginMarketCompat = {
  xlyra: string
  api_version: number
  host_api: number
}

export type PluginMarketRevocation = {
  revoked: boolean
  reason?: string
  level?: string
}

export type PluginMarketVersion = {
  version: string
  published_at: string
  changelog: string
  size: number
  compat: PluginMarketCompat
  hosts: string[]
  permissions: string[]
  compatible: boolean
  incompatible_reason?: string
  revocation: PluginMarketRevocation
}

export type PluginMarketItem = {
  id: string
  name: string
  summary: string
  kind: string
  developer: PluginMarketDeveloper
  homepage?: string
  icon_url?: string
  latest_compatible_version?: string
  has_compatible_version: boolean
  incompatible_reason?: string
  local_status: 'not_installed' | 'installed' | 'enabled' | string
  local_version?: string
  update_available: boolean
  update_version?: string
  update_policy?: 'off' | 'notify' | 'auto' | string
  paused_reason?: string
  auto_block_reason?: string
  auto_block_detail?: string
  signer_revoked?: boolean
  auto_rolled_back?: boolean
  has_revoked_version: boolean
}

export type PluginMarketPlugin = PluginMarketItem & {
  versions?: PluginMarketVersion[]
}

export type PluginMarketSnapshot = {
  index_expired: boolean
  revocations_expired: boolean
  index_synced_at?: string | null
  last_error: string
  source_url: string
  root_keys_configured: boolean
  enabled: boolean
}

export type PluginMarketCatalog = {
  plugins: PluginMarketItem[]
  snapshot: PluginMarketSnapshot
}

export type PluginMarketSyncStatus = {
  error?: string
  source_url?: string
}

export const pluginMarketQueryKeys = {
  all: ['settings', 'plugin-market'] as const,
  settings: () => [...pluginMarketQueryKeys.all, 'settings'] as const,
  list: () => [...pluginMarketQueryKeys.all, 'list'] as const,
  detail: (id: string) => [...pluginMarketQueryKeys.all, 'detail', id] as const,
}

export async function fetchPluginMarketSettings(signal?: AbortSignal) {
  return apiFetch<PluginMarketSettings>('/api/v1/settings/plugin-market', { signal })
}

export async function updatePluginMarketSettings(body: PluginMarketSettings) {
  return apiFetch<PluginMarketSettings>('/api/v1/settings/plugin-market', { method: 'PUT', body })
}

export async function fetchPluginMarket(signal?: AbortSignal) {
  return apiFetch<PluginMarketCatalog>('/api/v1/plugin-market', { signal })
}

export async function fetchPluginMarketPlugin(id: string, signal?: AbortSignal) {
  return apiFetch<PluginMarketPlugin>(`/api/v1/plugin-market/${encodeURIComponent(id)}`, { signal })
}

export async function syncPluginMarket() {
  return apiFetch<PluginMarketSyncStatus>('/api/v1/plugin-market/sync', { method: 'POST' })
}

export async function installPluginMarketPlugin(id: string, version?: string) {
  return apiFetch<JSPluginVersion>(`/api/v1/plugin-market/${encodeURIComponent(id)}/install`, {
    method: 'POST',
    body: version ? { version } : {},
  })
}
