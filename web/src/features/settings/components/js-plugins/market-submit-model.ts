import type { TFunction } from 'i18next'
import type { JSPluginListItem } from '@/features/settings/api/js-plugins'
import { submissionPending } from '@/features/settings/api/cloud-developer'
import { APIError } from '@/lib/http'

export function showMarketSubmit(plugin: JSPluginListItem) {
  if (samePublishedVersion(plugin)) return false
  return plugin.owned === true && plugin.market_ahead === true
}

export function samePublishedVersion(plugin: JSPluginListItem, submission = plugin.submission) {
  if (!submission || submission.status !== 'published') return false
  const remote = submission.version?.trim() ?? ''
  const local = plugin.local_version?.trim() || plugin.enabled_version?.trim() || ''
  if (!remote || !local) return false
  return remote === local
}

/** 只有提交后的待审状态被刷成已发布时才拉市场。已经是已发布、或第一次看到已发布，都不算。 */
export function publishedFromReview(previous: string | undefined, next: string | undefined) {
  if (next !== 'published' || !previous || previous === 'published') return false
  return submissionPending(previous) || previous === 'approved'
}

export function submissionStatusLabel(t: TFunction, status: string) {
  const key = `settings:jsPlugins.submit.status.${status}`
  const label = t(key)
  return label === key ? status : label
}

export function submitErrorText(t: TFunction, error: unknown) {
  if (error instanceof APIError && error.code === 'cloud_unavailable') {
    return t('settings:jsPlugins.submit.unavailable')
  }
  if (error instanceof APIError && error.code) {
    const key = `settings:jsPlugins.submit.errors.${error.code}`
    const label = t(key)
    if (label !== key) return label
    if (error.message) return error.message
  }
  return t('settings:jsPlugins.submit.failed')
}
