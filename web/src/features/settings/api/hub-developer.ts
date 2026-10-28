import { apiFetch } from '@/lib/http'

export type HubDeveloperStatus = 'none' | 'pending' | 'active' | 'suspended' | 'rejected' | string

export type HubDeveloper = {
  status: HubDeveloperStatus
  unavailable?: boolean
  display_name?: string
  namespaces?: Array<{ name: string; status: string }>
  keys?: Array<{ fingerprint: string; label?: string; status: string }>
  plugins?: Array<{ id: string; latest_version?: string; latest_status?: string }>
}

export type HubSubmission = {
  id: string
  plugin_id?: string
  version?: string
  status: string
  check_summary?: string
  review_note?: string
  submitted_at?: string
}

export const hubDeveloperQueryKeys = {
  developer: () => ['settings', 'hub-binding', 'developer'] as const,
  submissions: (pluginId: string) => ['settings', 'hub-binding', 'submissions', pluginId] as const,
}

const emptyDeveloper: HubDeveloper = { status: 'none', namespaces: [], keys: [], plugins: [] }

export async function fetchHubDeveloper(refresh = false, signal?: AbortSignal) {
  const path = refresh ? '/api/v1/hub-binding/developer?refresh=1' : '/api/v1/hub-binding/developer'
  try {
    return await apiFetch<HubDeveloper>(path, { signal })
  } catch {
    return emptyDeveloper
  }
}

export async function listHubSubmissions(pluginId: string, signal?: AbortSignal) {
  const result = await apiFetch<{ items: HubSubmission[] }>(
    `/api/v1/hub-binding/submissions?pluginId=${encodeURIComponent(pluginId)}`,
    { signal },
  )
  return result.items ?? []
}

export async function submitHubPlugin(body: { pluginId: string; version: string; changelog: string }) {
  return apiFetch<HubSubmission>('/api/v1/hub-binding/submissions', { method: 'POST', body })
}

export function latestHubSubmission(items: HubSubmission[] | undefined) {
  if (!items || items.length === 0) return undefined
  return items.reduce((best, item) => ((item.submitted_at ?? '') > (best.submitted_at ?? '') ? item : best))
}

export function submissionPending(status: string | undefined) {
  return status === 'submitted' || status === 'checking' || status === 'in_review'
}

export function submissionReason(item: HubSubmission | undefined) {
  const note = item?.review_note?.trim() || item?.check_summary?.trim() || ''
  return note
}
