import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Cloud, ExternalLink, Link2, RotateCcw, Search, Unplug } from 'lucide-react'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { ConfirmDialog } from '@/components/common/confirm-dialog'
import { ErrorState } from '@/components/common/error-state'
import { Notice } from '@/components/common/notice'
import { QRCode } from '@/components/common/qr-code'
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
import { Input } from '@/components/ui/input'
import { InputOTP } from '@/components/ui/input-otp'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
import { cn } from '@/lib/utils'

const cloudBindingPollMs = 2000
const rowPreviewLimit = 6

type CloudKey = NonNullable<CloudDeveloper['keys']>[number]
type CloudPlugin = NonNullable<CloudDeveloper['plugins']>[number]

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
  const pairingKey = pairingSessionKey(binding)
  const pairingOpen = pairingKey !== null && dismissedPairingKey !== pairingKey

  return (
    <div className="max-w-5xl space-y-7">
      <PageHeader
        eyebrow={t('xlyraCloud.eyebrow')}
        title="xLyra Cloud"
        description={t('xlyraCloud.description')}
        actions={(
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
  const bound = binding.status === 'bound'
  const connected = statusText(binding.status, t)
  const synced = binding.last_sync_at ? formatRelativeTime(binding.last_sync_at, language) : ''
  const manageURL = bound ? cloudConsoleURL(binding.cloud_url) : null
  const subtitle = bound
    ? t('xlyraCloud.bound.connectedTo', { name: binding.user_display_name || '—' })
    : binding.status === 'revoked'
      ? t('xlyraCloud.revoked.description')
      : binding.status === 'pairing'
        ? t('xlyraCloud.pairing.instruction')
        : t('xlyraCloud.unbound.description')
  const meta = bound
    ? [
        t('xlyraCloud.bound.instanceName', { name: binding.instance_name || '—' }),
        `${t('xlyraCloud.info.connectedAt')} ${formatLocalTime(binding.bound_at, language) || '—'}`,
        `${t('xlyraCloud.info.lastSync')} ${synced || t('xlyraCloud.bound.neverSynced')}`,
      ].join(' · ')
    : ''

  return (
    <>
      <Card className="flex flex-col gap-4 p-5 lg:flex-row lg:items-center">
        <div className="flex min-w-0 flex-1 items-center gap-4">
          <div className="flex size-12 shrink-0 items-center justify-center rounded-xl bg-fill-3 text-label-2">
            <Cloud className="size-5" />
          </div>
          <div className="min-w-0">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <h3 className="truncate text-title-3 text-label">xLyra Cloud</h3>
              <StatusBadge status={statusTone(binding.status)} className="shrink-0">{connected}</StatusBadge>
            </div>
            <p className="mt-1 truncate text-body text-label-2">{subtitle}</p>
            {bound ? <p className="mt-0.5 truncate text-footnote text-label-3">{meta}</p> : null}
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          {bound && manageURL ? (
            <Button variant="secondary" size="sm" asChild>
              <a href={manageURL} target="_blank" rel="noopener noreferrer" data-testid="cloud-manage-link">
                {t('xlyraCloud.bound.manage')}
                <ExternalLink className="h-3.5 w-3.5" />
              </a>
            </Button>
          ) : null}
          {!bound ? (
            <StatusAction binding={binding} pending={pending} onPair={onPair} onShowPairing={onShowPairing} />
          ) : null}
        </div>
      </Card>

      {bound && binding.last_error ? (
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

      {binding.status === 'unbound' || binding.status === 'revoked' ? (
        <Notice items={[t('xlyraCloud.unbound.privacy')]} />
      ) : null}

      {bound ? <DeveloperIdentity developer={developer} loading={developerLoading} /> : null}

      {bound ? (
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
        {openURL && !expired ? (
          <div className="flex flex-col items-center gap-3">
            <QRCode
              value={openURL}
              size={160}
              data-testid="cloud-pairing-qr"
              aria-label={t('xlyraCloud.pairing.scanHint')}
            />
            <p className="text-center text-footnote text-label-2">{t('xlyraCloud.pairing.scanHint')}</p>
          </div>
        ) : null}
        <ol className="list-decimal space-y-1 pl-5 text-body text-label-2">
          <li>{t('xlyraCloud.pairing.stepCopyOrScan')}</li>
          <li>{t('xlyraCloud.pairing.stepApprove')}</li>
          <li>{t('xlyraCloud.pairing.stepReturn')}</li>
        </ol>
        <p className="text-footnote text-label-2">{t('xlyraCloud.pairing.dismissHint')}</p>
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
  const [keysOpen, setKeysOpen] = useState(false)
  const [pluginsOpen, setPluginsOpen] = useState(false)
  const [idTab, setIdTab] = useState('namespaces')
  const unavailable = developer?.unavailable === true
  const status = developer?.status || 'none'
  const keys = developer?.keys ?? []
  const namespaces = developer?.namespaces ?? []
  const plugins = developer?.plugins ?? []
  const active = !loading && !unavailable && status === 'active'

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
      {active ? (
        <div className="grid items-stretch gap-4 lg:grid-cols-2">
          <Card className="overflow-hidden p-0">
            <Tabs value={idTab} onValueChange={setIdTab} variant="segmented">
              <div className="flex h-14 items-center justify-between gap-3 px-4">
                <p className="truncate text-headline text-label">
                  {idTab === 'keys'
                    ? t('xlyraCloud.developer.keysTitle', { count: keys.length })
                    : t('xlyraCloud.developer.namespacesTitle', { count: namespaces.length })}
                </p>
                <TabsList className="h-8 w-fit shrink-0">
                  <TabsTrigger value="namespaces">{t('xlyraCloud.developer.tab.namespaces')}</TabsTrigger>
                  <TabsTrigger value="keys">{t('xlyraCloud.developer.tab.keys')}</TabsTrigger>
                </TabsList>
              </div>
              <TabsContent value="namespaces" className="mt-0">
                {namespaces.length === 0 ? (
                  <p className="px-4 py-6 text-center text-callout text-label-3">{t('xlyraCloud.developer.namespacesEmpty')}</p>
                ) : (
                  <ul className="divide-y divide-separator">
                    {namespaces.map((item) => (
                      <li key={item.name} className="flex min-w-0 items-center justify-between gap-2 px-4 py-3">
                        <span className="min-w-0 flex-1 truncate font-mono text-callout text-label" title={item.name}>{item.name}</span>
                        <StatusBadge status={itemStatusTone(item.status)} className="shrink-0">
                          {labeled(t, 'namespaceStatus', item.status)}
                        </StatusBadge>
                      </li>
                    ))}
                  </ul>
                )}
              </TabsContent>
              <TabsContent value="keys" className="mt-0">
                {keys.length === 0 ? (
                  <p className="px-4 py-6 text-center text-callout text-label-3">{t('xlyraCloud.developer.keysEmpty')}</p>
                ) : (
                  <>
                    <ul className="divide-y divide-separator">
                      {keys.slice(0, rowPreviewLimit).map((item) => (
                        <li key={item.fingerprint} className="flex min-w-0 items-center justify-between gap-2 px-4 py-3">
                          <div className="min-w-0">
                            {item.label ? <p className="truncate text-callout text-label">{item.label}</p> : null}
                            <p className="truncate font-mono text-footnote text-label" title={item.fingerprint}>{item.fingerprint}</p>
                          </div>
                          <StatusBadge status={itemStatusTone(item.status)} className="shrink-0">
                            {labeled(t, 'keyStatus', item.status)}
                          </StatusBadge>
                        </li>
                      ))}
                    </ul>
                    {keys.length > rowPreviewLimit ? (
                      <div className="border-t border-separator">
                        <Button variant="ghost" className="w-full" onClick={() => setKeysOpen(true)}>
                          {t('xlyraCloud.developer.viewAll', { count: keys.length })}
                        </Button>
                      </div>
                    ) : null}
                  </>
                )}
              </TabsContent>
            </Tabs>
          </Card>
          <Card className="flex flex-col overflow-hidden p-0">
            <div className="flex h-14 items-center px-4">
              <p className="text-headline text-label">{t('xlyraCloud.developer.pluginsTitle', { count: plugins.length })}</p>
            </div>
            {plugins.length === 0 ? (
              <p className="px-4 py-6 text-center text-callout text-label-3">{t('xlyraCloud.developer.pluginsEmpty')}</p>
            ) : (
              <>
                <ul className="divide-y divide-separator">
                  {plugins.slice(0, rowPreviewLimit).map((item) => (
                    <PluginRow key={item.id} item={item} className="px-4 py-3" />
                  ))}
                </ul>
                {plugins.length > rowPreviewLimit ? (
                  <div className="mt-auto border-t border-separator">
                    <Button variant="ghost" className="w-full" onClick={() => setPluginsOpen(true)}>
                      {t('xlyraCloud.developer.viewAll', { count: plugins.length })}
                    </Button>
                  </div>
                ) : null}
              </>
            )}
          </Card>
        </div>
      ) : null}

      <KeysDialog open={keysOpen} keys={keys} onOpenChange={setKeysOpen} />
      <PluginsDialog open={pluginsOpen} plugins={plugins} onOpenChange={setPluginsOpen} />
    </div>
  )
}

function PluginRow({ item, className }: { item: CloudPlugin; className?: string }) {
  const { t } = useTranslation('settings')
  return (
    <li className={cn('flex min-w-0 flex-wrap items-center justify-between gap-2', className)}>
      <span className="min-w-0 flex-1 truncate font-mono text-callout text-label" title={item.id}>{item.id}</span>
      <span className="flex shrink-0 flex-wrap items-center gap-2">
        <span className="font-mono text-footnote text-label-2">{item.latest_version || t('xlyraCloud.developer.pluginNoVersion')}</span>
        {item.latest_status ? (
          <StatusBadge status={itemStatusTone(item.latest_status)}>
            {labeled(t, 'pluginStatus', item.latest_status)}
          </StatusBadge>
        ) : null}
      </span>
    </li>
  )
}

function KeysDialog({ open, keys, onOpenChange }: { open: boolean; keys: CloudKey[]; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation(['settings', 'common'])
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t('xlyraCloud.developer.keysDialogTitle')}</DialogTitle>
        </DialogHeader>
        <DialogBody className="max-h-[60vh]">
          <ul className="space-y-3">
            {keys.map((item) => (
              <li key={item.fingerprint} className="space-y-1">
                <p className="break-all font-mono text-footnote text-label">{item.fingerprint}</p>
                <p className="text-footnote text-label-3">{[item.label, labeled(t, 'keyStatus', item.status)].filter(Boolean).join(' · ')}</p>
              </li>
            ))}
          </ul>
        </DialogBody>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>{t('common:actions.close')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PluginsDialog({ open, plugins, onOpenChange }: { open: boolean; plugins: CloudPlugin[]; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation(['settings', 'common'])
  const [query, setQuery] = useState('')
  const keyword = query.trim().toLowerCase()
  const filtered = keyword ? plugins.filter((item) => item.id.toLowerCase().includes(keyword)) : plugins
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) setQuery(''); onOpenChange(next) }}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{t('xlyraCloud.developer.pluginsDialogTitle')}</DialogTitle>
        </DialogHeader>
        <div className="px-6 pt-5">
          <label className="relative block">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-label-3" />
            <Input
              type="search"
              className="pl-9"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t('xlyraCloud.developer.pluginSearch')}
            />
          </label>
        </div>
        <DialogBody className="max-h-[60vh]">
          {filtered.length === 0 ? (
            <p className="text-body text-label-2">{t('xlyraCloud.developer.pluginSearchEmpty')}</p>
          ) : (
            <ul className="space-y-3">
              {filtered.map((item) => (
                <PluginRow key={item.id} item={item} />
              ))}
            </ul>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="secondary" onClick={() => onOpenChange(false)}>{t('common:actions.close')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
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

function itemStatusTone(status: string) {
  if (status === 'active' || status === 'approved' || status === 'published' || status === 'verified') return 'success' as const
  if (status === 'pending' || status === 'in_review' || status === 'checking' || status === 'submitted') return 'syncing' as const
  if (status === 'revoked' || status === 'rejected' || status === 'failed' || status === 'check_failed' || status === 'taken_down') return 'error' as const
  if (status === 'suspended' || status === 'disabled') return 'warning' as const
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
