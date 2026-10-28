import { apiFetch } from '@/lib/http'

export type CloudDeveloperStatus = 'none' | 'pending' | 'active' | 'suspended' | 'rejected' | string

export type CloudDeveloper = {
  status: CloudDeveloperStatus
  unavailable?: boolean
  display_name?: string
  namespaces?: Array<{ name: string; status: string }>
  keys?: Array<{ fingerprint: string; label?: string; status: string }>
  plugins?: Array<{ id: string; latest_version?: string; latest_status?: string }>
}

export type CloudSubmission = {
  id: string
  plugin_id?: string
  version?: string
  status: string
  check_summary?: string
  review_note?: string
  submitted_at?: string
}

export const cloudDeveloperQueryKeys = {
  developer: () => ['settings', 'cloud-binding', 'developer'] as const,
  submissions: (pluginId: string) => ['settings', 'cloud-binding', 'submissions', pluginId] as const,
}

const emptyDeveloper: CloudDeveloper = { status: 'none', namespaces: [], keys: [], plugins: [] }

export async function fetchCloudDeveloper(refresh = false, signal?: AbortSignal) {
  const path = refresh ? '/api/v1/cloud-binding/developer?refresh=1' : '/api/v1/cloud-binding/developer'
  try {
    return await apiFetch<CloudDeveloper>(path, { signal })
  } catch {
    return emptyDeveloper
  }
}

export async function listCloudSubmissions(pluginId: string, signal?: AbortSignal) {
  const result = await apiFetch<{ items: CloudSubmission[] }>(
    `/api/v1/cloud-binding/submissions?pluginId=${encodeURIComponent(pluginId)}`,
    { signal },
  )
  return result.items ?? []
}

export async function submitCloudPlugin(body: { pluginId: string; version: string; changelog: string }) {
  return apiFetch<CloudSubmission>('/api/v1/cloud-binding/submissions', { method: 'POST', body })
}

export function latestCloudSubmission(items: CloudSubmission[] | undefined) {
  if (!items || items.length === 0) return undefined
  return items.reduce((best, item) => ((item.submitted_at ?? '') > (best.submitted_at ?? '') ? item : best))
}

export function submissionPending(status: string | undefined) {
  return status === 'submitted' || status === 'checking' || status === 'in_review'
}

export function submissionReason(item: CloudSubmission | undefined) {
  const note = item?.review_note?.trim() || item?.check_summary?.trim() || ''
  return note
}
