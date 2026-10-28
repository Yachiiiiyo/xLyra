import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Cloud, ExternalLink, Link2, RotateCcw, Unplug, UserRound } from 'lucide-react'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { ConfirmDialog } from '@/components/common/confirm-dialog'
import { ErrorState } from '@/components/common/error-state'
import { Notice } from '@/components/common/notice'
import { PageHeader } from '@/components/common/page-header'
import { StatusBadge } from '@/components/common/status-badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { InputOTP } from '@/components/ui/input-otp'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'
import {
  cancelCloudPairing,
  disconnectCloudBinding,
  fetchCloudBinding,
  cloudBindingQueryKeys,
  cloudConsoleURL,
  pairCloudBinding,
  type CloudBinding,
  type CloudBindingStatus,
} from '@/features/settings/api/cloud-binding'
import { fetchCloudDeveloper, cloudDeveloperQueryKeys, type CloudDeveloper } from '@/features/settings/api/cloud-developer'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

const cloudBindingPollMs = 2000

export function XlyraCloudSettingsPage() {
  const { t, i18n } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const [visible, setVisible] = useState(isPageVisible)
  const [disconnectOpen, setDisconnectOpen] = useState(false)
  const [dismissedPairingKey, setDismissedPairingKey] = useState<string | null>(null)
  const previousStatus = useRef<CloudBinding['status'] | undefined>(undefined)

  useEffect(() => {
    if (typeof document === 'undefined') return
    const sync = () => setVisible(isPageVisible())
    document.addEventListener('visibilitychange', sync)
    return () => document.removeEventListener('visibilitychange', sync)
  }, [])

  const bindingQuery = useQuery({
    queryKey: cloudBindingQueryKeys.all,
    queryFn: ({ signal }) => fetchCloudBinding(signal),
    refetchOnWindowFocus: false,
  })
  const binding = bindingQuery.data
  const refetchBinding = bindingQuery.refetch
  const developerQuery = useQuery({
    queryKey: cloudDeveloperQueryKeys.developer(),
    queryFn: ({ signal }) => fetchCloudDeveloper(true, signal),
    enabled: binding?.status === 'bound',
    refetchOnWindowFocus: false,
  })
  const refetchDeveloper = developerQuery.refetch
  const followBinding = visible && (binding?.status === 'pairing' || (binding?.status === 'bound' && !binding.last_sync_at))

  useEffect(() => {
    if (!followBinding) return
    const timer = setInterval(() => {
      void refetchBinding()
    }, cloudBindingPollMs)
    return () => clearInterval(timer)
  }, [followBinding, refetchBinding])

  useEffect(() => {
    if (previousStatus.current === 'pairing' && binding?.status === 'bound') {
      toast.success(t('xlyraCloud.toast.connected'))
    }
    previousStatus.current = binding?.status
  }, [binding?.status, t])

  const pairMutation = useMutation({
    mutationFn: () => pairCloudBinding(),
    onSuccess: (next) => {
      queryClient.setQueryData(cloudBindingQueryKeys.all, next)
    },
    onError: (error) => {
      toast.error(errorText(error))
    },
  })

  const cancelMutation = useMutation({
    mutationFn: cancelCloudPairing,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: cloudBindingQueryKeys.all })
    },
    onError: (error) => {
      toast.error(errorText(error))
    },
  })

  const disconnectMutation = useMutation({
    mutationFn: disconnectCloudBinding,
    onSuccess: async () => {
      setDisconnectOpen(false)
      toast.success(t('xlyraCloud.toast.disconnected'))
      await queryClient.invalidateQueries({ queryKey: cloudBindingQueryKeys.all })
    },
    onError: (error) => {
      toast.error(errorText(error))
    },
  })

  const pending = pairMutation.isPending || cancelMutation.isPending
  const manageURL = binding?.status === 'bound' ? cloudConsoleURL(binding.cloud_url) : null
  const pairingKey = pairingSessionKey(binding)
  const pairingOpen = pairingKey !== null && dismissedPairingKey !== pairingKey

  return (
    <div className="max-w-5xl space-y-7">
      <PageHeader
        eyebrow={t('xlyraCloud.eyebrow')}
        title="xLyra Cloud"
        description={t('xlyraCloud.description')}
        actions={(
          <>
            {manageURL ? (
              <Button variant="secondary" size="sm" asChild>
                <a href={manageURL} target="_blank" rel="noopener noreferrer" data-testid="cloud-manage-link">
                  <ExternalLink className="h-3.5 w-3.5" />
                  {t('xlyraCloud.bound.manage')}
                </a>
              </Button>
            ) : null}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                void refetchBinding()
                if (binding?.status === 'bound') void refetchDeveloper()
              }}
              disabled={bindingQuery.isFetching || developerQuery.isFetching}
            >
              <RotateCcw className={`h-3.5 w-3.5 ${bindingQuery.isFetching || developerQuery.isFetching ? 'animate-spin' : ''}`} />
              {t('xlyraCloud.refresh')}
            </Button>
          </>
        )}
      />

      {bindingQuery.isPending ? (
        <p data-testid="cloud-binding-loading" aria-busy="true" className="text-body text-label-2">
          {t('common:status.loading')}
        </p>
      ) : bindingQuery.isError || !binding ? (
        <ErrorState
          title={t('xlyraCloud.error.title')}
          description={errorText(bindingQuery.error)}
          action={(
            <Button variant="secondary" onClick={() => void refetchBinding()}>
              {t('xlyraCloud.error.retry')}
            </Button>
          )}
        />
      ) : (
        <CloudBody
          binding={binding}
          language={i18n.language}
          pending={pending}
          developer={binding.status === 'bound' ? developerQuery.data : undefined}
          developerLoading={binding.status === 'bound' && developerQuery.isPending}
          onPair={() => pairMutation.mutate()}
          onShowPairing={() => setDismissedPairingKey(null)}
          onDisconnect={() => setDisconnectOpen(true)}
        />
      )}

      <Dialog
        open={pairingOpen}
        onOpenChange={(next) => { if (!next) setDismissedPairingKey(pairingKey) }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('xlyraCloud.pairing.heading')}</DialogTitle>
            <DialogDescription>{t('xlyraCloud.pairing.instruction')}</DialogDescription>
          </DialogHeader>
          {binding?.status === 'pairing' ? (
            <PairingGuide
              binding={binding}
              pending={pending}
              onPair={() => pairMutation.mutate()}
              onDrop={() => {
                setDismissedPairingKey(pairingKey)
                cancelMutation.mutate()
              }}
            />
          ) : null}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={disconnectOpen}
        title={t('xlyraCloud.danger.confirmTitle')}
        description={t('xlyraCloud.danger.disconnectDescription')}
        confirmLabel={t('xlyraCloud.danger.confirm')}
        pending={disconnectMutation.isPending}
        destructive
        onCancel={() => setDisconnectOpen(false)}
        onConfirm={() => disconnectMutation.mutate()}
      />
    </div>
  )
}

function CloudBody({
  binding,
  language,
  pending,
  developer,
  developerLoading,
  onPair,
  onShowPairing,
  onDisconnect,
}: {
  binding: CloudBinding
  language: string
  pending: boolean
  developer?: CloudDeveloper
  developerLoading: boolean
  onPair: () => void
  onShowPairing: () => void
  onDisconnect: () => void
}) {
  const { t } = useTranslation('settings')
  const synced = binding.last_sync_at ? formatRelativeTime(binding.last_sync_at, language) : ''

  return (
    <>
      <div className="grid gap-4 md:grid-cols-2">
        <Card className="p-5">
          <div className="flex items-center gap-2 text-body text-label-2">
            <Cloud className="h-4 w-4" />
            {t('xlyraCloud.statusLabel')}
          </div>
          <div className="mt-3 flex min-h-9 flex-wrap items-center justify-between gap-3">
            <span className="flex min-w-0 items-center gap-2 text-title-3 text-label">
              {statusText(binding.status, t)}
              <StatusBadge status={statusTone(binding.status)}>{statusText(binding.status, t)}</StatusBadge>
            </span>
            <StatusAction binding={binding} pending={pending} onPair={onPair} onShowPairing={onShowPairing} />
          </div>
        </Card>
        <Card className="p-5">
          <div className="flex items-center gap-2 text-body text-label-2">
            <UserRound className="h-4 w-4" />
            {t('xlyraCloud.account.title')}
          </div>
          <div className="mt-3 flex h-9 items-center justify-between gap-3">
            <span className="min-w-0 truncate text-title-3 text-label">
              {binding.status === 'bound' ? (binding.user_display_name || '—') : '—'}
            </span>
          </div>
          {binding.status === 'bound' ? (
            <p className="mt-1 text-footnote text-label-2">{t('xlyraCloud.bound.instanceName', { name: binding.instance_name })}</p>
          ) : null}
        </Card>
      </div>

      {binding.status === 'bound' ? (
        <section className="space-y-5 border-t border-separator pt-6">
          <div>
            <h4 className="text-headline text-label">{t('xlyraCloud.info.title')}</h4>
            <p className="mt-1 text-body text-label-2">
              {t('xlyraCloud.bound.description', { name: binding.user_display_name || '—' })}
            </p>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Card className="p-5">
              <p className="text-body text-label-2">{t('xlyraCloud.info.connectedAt')}</p>
              <p className="mt-2 text-title-3 text-label">{formatLocalTime(binding.bound_at, language) || '—'}</p>
            </Card>
            <Card className="p-5">
              <p className="text-body text-label-2">{t('xlyraCloud.info.lastSync')}</p>
              <p className="mt-2 text-title-3 text-label">{synced || t('xlyraCloud.bound.neverSynced')}</p>
            </Card>
          </div>
          {binding.last_error ? (
            <Notice
              tone="warning"
              items={[
                binding.last_error_at
                  ? t('xlyraCloud.bound.syncError', { error: binding.last_error, time: formatLocalTime(binding.last_error_at, language) })
                  : t('xlyraCloud.bound.syncErrorNoTime', { error: binding.last_error }),
                t('xlyraCloud.bound.syncErrorHint'),
              ]}
            />
          ) : null}
          <DeveloperIdentity developer={developer} loading={developerLoading} />
        </section>
      ) : null}

      {binding.status === 'unbound' || binding.status === 'revoked' ? (
        <Notice
          items={binding.status === 'revoked'
            ? [t('xlyraCloud.revoked.description'), t('xlyraCloud.unbound.privacy')]
            : [t('xlyraCloud.unbound.description'), t('xlyraCloud.unbound.privacy')]}
        />
      ) : null}

      {binding.status === 'bound' ? (
        <section className="border-t border-separator pt-6">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h4 className="text-headline text-label">{t('xlyraCloud.danger.title')}</h4>
              <p className="mt-1 text-body text-label-2">{t('xlyraCloud.danger.disconnectDescription')}</p>
            </div>
            <Button variant="destructive-soft" onClick={onDisconnect}>
              <Unplug className="h-4 w-4" />
              {t('xlyraCloud.danger.disconnectTitle')}
            </Button>
          </div>
        </section>
      ) : null}
    </>
  )
}

function StatusAction({
  binding,
  pending,
  onPair,
  onShowPairing,
}: {
  binding: CloudBinding
  pending: boolean
  onPair: () => void
  onShowPairing: () => void
}) {
  const { t } = useTranslation('settings')
  if (binding.status === 'unbound') {
    return (
      <Button size="sm" onClick={onPair} loading={pending} disabled={pending}>
        <Link2 className="h-3.5 w-3.5" />
        {t('xlyraCloud.unbound.connect')}
      </Button>
    )
  }
  if (binding.status === 'revoked') {
    return (
      <Button size="sm" onClick={onPair} loading={pending} disabled={pending}>
        <Link2 className="h-3.5 w-3.5" />
        {t('xlyraCloud.revoked.reconnect')}
      </Button>
    )
  }
  if (binding.status === 'pairing') {
    return (
      <Button size="sm" variant="secondary" onClick={onShowPairing}>
        {t('xlyraCloud.pairing.viewCode')}
      </Button>
    )
  }
  return null
}

function PairingGuide({
  binding,
  pending,
  onPair,
  onDrop,
}: {
  binding: CloudBinding
  pending: boolean
  onPair: () => void
  onDrop: () => void
}) {
  const { t } = useTranslation('settings')
  const [now, setNow] = useState(() => Date.now())
  const expiresAt = binding.pairing?.expires_at
  const userCode = binding.pairing?.user_code

  useEffect(() => {
    if (!userCode || !expiresAt) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [expiresAt, userCode])

  const openURL = userCode ? cloudConsoleURL(binding.cloud_url, userCode) : null
  const remaining = userCode && expiresAt ? remainingSeconds(expiresAt, now) : null
  const expired = remaining !== null && remaining <= 0
  const canCopyOpen = Boolean(userCode && openURL && !expired)

  return (
    <>
      <DialogBody className="space-y-4">
        {userCode ? (
          <InputOTP readOnly value={userCode} aria-label={t('xlyraCloud.pairing.heading')} testId="cloud-pairing-code" />
        ) : (
          <p className="text-body text-label">{t('xlyraCloud.pairing.codeMissing')}</p>
        )}
        {userCode && remaining !== null && !expired ? (
          <p className="text-center text-footnote tabular-nums text-label-2">{t('xlyraCloud.pairing.countdown', { time: formatCountdown(remaining) })}</p>
        ) : null}
        {expired ? <p className="text-center text-footnote tabular-nums text-label-2">{t('xlyraCloud.pairing.expired')}</p> : null}
      </DialogBody>
      <DialogFooter
        cancel={(
          <Button variant="secondary" onClick={onDrop} loading={pending} disabled={pending}>
            {t('xlyraCloud.pairing.cancelPairing')}
          </Button>
        )}
        confirm={canCopyOpen && openURL && userCode ? (
          <Button
            data-testid="cloud-copy-open"
            disabled={pending}
            onClick={() => void copyAndOpen(userCode, openURL, t('xlyraCloud.pairing.copied'), t('xlyraCloud.pairing.copyFailed'))}
          >
            {t('xlyraCloud.pairing.copyOpen')}
          </Button>
        ) : (
          <Button onClick={onPair} loading={pending} disabled={pending}>
            <RotateCcw className="h-4 w-4" />
            {t('xlyraCloud.pairing.regenerate')}
          </Button>
        )}
      />
    </>
  )
}

async function copyAndOpen(code: string, url: string, copied: string, failed: string) {
  const ok = await copyToClipboard(code, copied, failed)
  if (!ok) return
  window.open(url, '_blank', 'noopener,noreferrer')
}

function pairingSessionKey(binding: CloudBinding | undefined) {
  if (binding?.status !== 'pairing') return null
  return `${binding.pairing?.user_code ?? ''}|${binding.pairing?.expires_at ?? ''}`
}

function statusText(status: CloudBindingStatus, t: (key: string) => string) {
  if (status === 'pairing') return t('xlyraCloud.pairing.title')
  if (status === 'bound') return t('xlyraCloud.bound.status')
  if (status === 'revoked') return t('xlyraCloud.revoked.title')
  return t('xlyraCloud.unbound.title')
}

function DeveloperIdentity({ developer, loading }: { developer?: CloudDeveloper; loading: boolean }) {
  const { t } = useTranslation('settings')
  const unavailable = developer?.unavailable === true
  const status = developer?.status || 'none'
  const keys = developer?.keys ?? []
  const namespaces = developer?.namespaces ?? []
  const plugins = developer?.plugins ?? []
  return (
    <div className="space-y-4 border-t border-separator pt-5" data-testid="cloud-developer">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-headline text-label">{t('xlyraCloud.developer.title')}</h4>
        <StatusBadge status={unavailable ? 'warning' : developerTone(status)}>
          {unavailable
            ? t('xlyraCloud.developer.status.unavailable')
            : t(`xlyraCloud.developer.status.${knownDeveloperStatus(status)}`, { defaultValue: status })}
        </StatusBadge>
      </div>
      {loading ? <p className="text-body text-label-2">{t('xlyraCloud.developer.loading')}</p> : null}
      {!loading && unavailable ? (
        <p className="text-body text-label-2">{t('xlyraCloud.developer.unavailable')}</p>
      ) : null}
      {!loading && !unavailable && status !== 'active' ? (
        <p className="text-body text-label-2">{t(`xlyraCloud.developer.${status === 'pending' || status === 'rejected' || status === 'suspended' ? status : 'none'}`)}</p>
      ) : null}
      {!loading && !unavailable && status === 'active' ? (
        <div className="grid gap-4 lg:grid-cols-3">
          <Card className="space-y-3 p-5">
            <p className="text-body text-label-2">{t('xlyraCloud.developer.namespaces')}</p>
            {namespaces.length === 0 ? <p className="text-callout text-label-3">{t('xlyraCloud.developer.namespacesEmpty')}</p> : null}
            <ul className="space-y-2">
              {namespaces.map((item) => (
                <li key={item.name} className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-mono text-callout text-label">{item.name}</span>
                  <span className="text-footnote text-label-3">{labeled(t, 'namespaceStatus', item.status)}</span>
                </li>
              ))}
            </ul>
          </Card>
          <Card className="space-y-3 p-5">
            <div className="flex items-center justify-between gap-3">
              <p className="text-body text-label-2">{t('xlyraCloud.developer.keys')}</p>
              <span className="text-title-3 text-label">{t('xlyraCloud.developer.keysCount', { count: keys.length })}</span>
            </div>
            {keys.length === 0 ? <p className="text-callout text-label-3">{t('xlyraCloud.developer.keysEmpty')}</p> : null}
            <ul className="space-y-2">
              {keys.map((item) => (
                <li key={item.fingerprint} className="space-y-1">
                  <p className="break-all font-mono text-footnote text-label">{item.fingerprint}</p>
                  <p className="text-footnote text-label-3">{[item.label, labeled(t, 'keyStatus', item.status)].filter(Boolean).join(' · ')}</p>
                </li>
              ))}
            </ul>
          </Card>
          <Card className="space-y-3 p-5">
            <p className="text-body text-label-2">{t('xlyraCloud.developer.plugins')}</p>
            {plugins.length === 0 ? <p className="text-callout text-label-3">{t('xlyraCloud.developer.pluginsEmpty')}</p> : null}
            <ul className="space-y-3">
              {plugins.map((item) => (
                <li key={item.id} className="space-y-1">
                  <p className="break-all font-mono text-callout text-label">{item.id}</p>
                  <p className="text-footnote text-label-3">
                    {item.latest_version ? t('xlyraCloud.developer.pluginVersion', { version: item.latest_version }) : t('xlyraCloud.developer.pluginNoVersion')}
                    {item.latest_status ? ` · ${labeled(t, 'pluginStatus', item.latest_status)}` : ''}
                  </p>
                </li>
              ))}
            </ul>
          </Card>
        </div>
      ) : null}
    </div>
  )
}

function knownDeveloperStatus(status: string) {
  if (status === 'pending' || status === 'active' || status === 'suspended' || status === 'rejected') return status
  return 'none'
}

function developerTone(status: string) {
  if (status === 'active') return 'success' as const
  if (status === 'pending') return 'syncing' as const
  if (status === 'suspended' || status === 'rejected') return 'warning' as const
  return 'idle' as const
}

function labeled(t: (key: string, options?: { defaultValue?: string }) => string, group: 'namespaceStatus' | 'keyStatus' | 'pluginStatus', status: string) {
  return t(`xlyraCloud.developer.${group}.${status}`, { defaultValue: status })
}

function statusTone(status: CloudBindingStatus) {
  if (status === 'pairing') return 'syncing'
  if (status === 'bound') return 'success'
  if (status === 'revoked') return 'warning'
  return 'idle'
}

function isPageVisible() {
  if (typeof document === 'undefined') return true
  return document.visibilityState !== 'hidden'
}

function remainingSeconds(expiresAt: string, now: number) {
  const expires = new Date(expiresAt).getTime()
  if (!Number.isFinite(expires)) return 0
  return Math.ceil((expires - now) / 1000)
}

function formatCountdown(totalSeconds: number) {
  const seconds = Math.max(0, totalSeconds)
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return `${String(minutes).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
}

function formatLocalTime(value: string | null, language: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const locale = language.startsWith('ja') || language.startsWith('jp') ? 'ja' : language.startsWith('en') ? 'en' : 'zh-CN'
  return date.toLocaleString(locale)
}

function errorText(error: unknown) {
  if (error instanceof APIError) return error.message || error.code || ''
  if (error instanceof Error) return error.message
  return ''
}
