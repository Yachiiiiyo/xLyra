import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CloudUpload, RotateCcw } from 'lucide-react'
import { StatusBadge } from '@/components/common/status-badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { TextArea } from '@/components/ui/textarea'
import {
  cloudDeveloperQueryKeys,
  latestCloudSubmission,
  listCloudSubmissions,
  submissionPending,
  submissionReason,
  type CloudSubmission,
} from '@/features/settings/api/cloud-developer'
import { jsPluginQueryKeys, type JSPluginListItem } from '@/features/settings/api/js-plugins'
import { installPluginMarketPlugin, syncPluginMarket } from '@/features/settings/api/plugin-market'
import { publishedFromReview, samePublishedVersion, showMarketSubmit, submissionStatusLabel } from '@/features/settings/components/js-plugins/market-submit-model'

const adoptedMarketVersions = new Set<string>()

function adoptPublishedMarketVersion(pluginId: string, version: string) {
  const key = `${pluginId}@${version}`
  if (adoptedMarketVersions.has(key)) return Promise.resolve()
  adoptedMarketVersions.add(key)
  return syncPluginMarket()
    .then(() => installPluginMarketPlugin(pluginId, version))
    .catch((error: unknown) => {
      adoptedMarketVersions.delete(key)
      throw error
    })
}

function badgeStatus(status: string) {
  switch (status) {
    case 'approved':
    case 'published':
      return 'success' as const
    case 'rejected':
    case 'check_failed':
      return 'error' as const
    case 'submitted':
    case 'checking':
    case 'in_review':
      return 'warning' as const
    default:
      return 'idle' as const
  }
}

export function MarketSubmitActions({
  plugin,
  busy,
  developerUnavailable,
  onOpen,
}: {
  plugin: JSPluginListItem
  busy?: boolean
  developerUnavailable?: boolean
  onOpen: (plugin: JSPluginListItem) => void
}) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const [fresh, setFresh] = useState<CloudSubmission | undefined>()
  const [refreshing, setRefreshing] = useState(false)
  const [syncError, setSyncError] = useState('')
  const canShow = showMarketSubmit(plugin) || (plugin.owned === true && plugin.submission != null && !samePublishedVersion(plugin))
  const visible = canShow && !samePublishedVersion(plugin, fresh ?? plugin.submission)
  const query = useQuery({
    queryKey: cloudDeveloperQueryKeys.submissions(plugin.id),
    queryFn: async ({ signal }) => latestCloudSubmission(await listCloudSubmissions(plugin.id, signal)),
    enabled: visible,
  })
  const submission = fresh ?? query.data ?? plugin.submission
  const show = canShow && !samePublishedVersion(plugin, submission)

  async function refreshStatus() {
    if (refreshing) return
    setRefreshing(true)
    const previous = submission?.status
    try {
      const next = latestCloudSubmission(await listCloudSubmissions(plugin.id))
      setFresh(next)
      const version = next?.version?.trim() || plugin.local_version?.trim() || ''
      if (publishedFromReview(previous, next?.status) && samePublishedVersion(plugin, next) && version) {
        try {
          await adoptPublishedMarketVersion(plugin.id, version)
        } catch {
          setSyncError(t('settings:jsPlugins.submit.syncFailed'))
          return
        }
        await queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.uploaded() })
      }
      setSyncError('')
    } finally {
      setRefreshing(false)
    }
  }

  if (!show && !syncError) return null
  const pending = submissionPending(submission?.status)
  const reason = submissionReason(submission)
  return (
    <>
      {submission ? (
        <span className="inline-flex items-center gap-1">
          <StatusBadge status={badgeStatus(submission.status)} title={reason || undefined}>
            {submissionStatusLabel(t, submission.status)}
          </StatusBadge>
          <Button
            type="button"
            size="icon-sm"
            variant="ghost"
            className="size-7"
            title={t('settings:jsPlugins.submit.refresh')}
            aria-label={t('settings:jsPlugins.submit.refresh')}
            disabled={refreshing}
            onClick={() => void refreshStatus()}
          >
            <RotateCcw className="h-4 w-4" />
          </Button>
        </span>
      ) : null}
      {show ? (
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="size-7"
          title={developerUnavailable ? t('settings:jsPlugins.submit.unavailable') : t('settings:jsPlugins.submit.action')}
          aria-label={developerUnavailable ? t('settings:jsPlugins.submit.unavailable') : t('settings:jsPlugins.submit.action')}
          disabled={busy || pending || developerUnavailable}
          onClick={() => { if (!developerUnavailable) onOpen(plugin) }}
        >
          <CloudUpload className="h-4 w-4" />
        </Button>
      ) : null}
      {syncError ? <p role="alert" className="text-body text-danger">{syncError}</p> : null}
    </>
  )
}

export function MarketSubmitDialog({
  plugin,
  hosts,
  permissions,
  signer,
  summaryLoading,
  submitting,
  error,
  onClose,
  onSubmit,
}: {
  plugin: JSPluginListItem | null
  hosts: string[]
  permissions: string[]
  signer: string
  summaryLoading?: boolean
  submitting?: boolean
  error?: string
  onClose: () => void
  onSubmit: (changelog: string) => void
}) {
  const { t } = useTranslation(['settings', 'common'])
  const [changelog, setChangelog] = useState('')
  const open = plugin != null
  const length = [...changelog.trim()].length
  const market = plugin?.market_version?.trim() ? plugin.market_version : t('settings:jsPlugins.submit.marketEmpty')
  const list = (items: string[]) => (items.length > 0 ? items.join(', ') : t('settings:jsPlugins.submit.empty'))
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent size="sm" swipeDismiss={false}>
        <DialogHeader>
          <DialogTitle>{t('settings:jsPlugins.submit.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('settings:jsPlugins.submit.dialogDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-body">
            <dt className="text-label-2">{t('settings:jsPlugins.submit.localVersion')}</dt>
            <dd className="font-mono">{plugin?.local_version || '—'}</dd>
            <dt className="text-label-2">{t('settings:jsPlugins.submit.marketVersion')}</dt>
            <dd className="font-mono">{market}</dd>
          </dl>
          <div className="space-y-1 text-body">
            <p className="text-subhead text-label">{t('settings:jsPlugins.submit.summary')}</p>
            {summaryLoading ? (
              <p className="text-label-2">{t('settings:jsPlugins.loading')}</p>
            ) : (
              <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
                <dt className="text-label-2">{t('settings:jsPlugins.columns.id')}</dt>
                <dd className="truncate font-mono">{plugin?.id}</dd>
                <dt className="text-label-2">{t('settings:jsPlugins.columns.version')}</dt>
                <dd className="font-mono">{plugin?.local_version || '—'}</dd>
                <dt className="text-label-2">{t('settings:jsPlugins.submit.hosts')}</dt>
                <dd>{list(hosts)}</dd>
                <dt className="text-label-2">{t('settings:jsPlugins.submit.permissions')}</dt>
                <dd>{list(permissions)}</dd>
                <dt className="text-label-2">{t('settings:jsPlugins.submit.signer')}</dt>
                <dd className="truncate font-mono">{signer || t('settings:jsPlugins.submit.empty')}</dd>
              </dl>
            )}
          </div>
          <FormField label={t('settings:jsPlugins.submit.changelog')} htmlFor="market-submit-changelog" required>
            <TextArea
              id="market-submit-changelog"
              value={changelog}
              maxLength={2000}
              placeholder={t('settings:jsPlugins.submit.changelogPlaceholder')}
              onChange={(event) => setChangelog(event.target.value)}
            />
          </FormField>
          {error ? <p className="text-body text-danger">{error}</p> : null}
          {submitting ? <p className="text-body text-label-2">{t('settings:jsPlugins.submit.submitting')}</p> : null}
        </DialogBody>
        <DialogFooter>
          <Button type="button" variant="secondary" onClick={onClose}>
            {t('common:actions.cancel')}
          </Button>
          <Button type="button" disabled={submitting || length < 1 || length > 2000} onClick={() => onSubmit(changelog.trim())}>
            {submitting ? t('settings:jsPlugins.submit.submitting') : t('settings:jsPlugins.submit.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
