import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Copy, LoaderCircle, QrCode, Smartphone } from 'lucide-react'
import { PageHeader } from '@/components/common/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/common/confirm-dialog'
import { EmptyState } from '@/components/common/empty-state'
import { QRCode } from '@/components/common/qr-code'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  createDevicePairing,
  devicesQueryKeys,
  listDevices,
  revokeAllDevices,
  revokeDevice,
  type DeviceItem,
  type DevicePairing,
} from '@/features/auth/api/paired-devices'

export function DevicesSettingsPage() {
  const { t, i18n } = useTranslation('settings')
  const { t: tc } = useTranslation('common')
  const queryClient = useQueryClient()
  const [pairingOpen, setPairingOpen] = useState(false)
  const [pairing, setPairing] = useState<DevicePairing | null>(null)
  // 生成配对码时已有的设备 id；之后多出来的那台就是刚扫码连上的。
  const [knownIds, setKnownIds] = useState<Set<string> | null>(null)
  const origin = window.location?.origin ?? ''
  const remaining = useCountdownSeconds(pairing?.expires_at ?? null)
  const expired = pairing !== null && remaining <= 0
  const findPaired = (list: DeviceItem[] | undefined) => (knownIds ? list?.find((item) => !knownIds.has(item.id)) : undefined)
  const devicesQuery = useQuery({
    queryKey: devicesQueryKeys.list(),
    queryFn: listDevices,
    // 配对码有效期内每 2 秒看一次设备列表，有新设备就算连上了，不用等配对码过期。
    // 扫码时人在看手机，网页标签多半在后台，也要继续查。
    refetchIntervalInBackground: true,
    refetchInterval: (query) => (pairing && !expired && !findPaired(query.state.data) ? 2000 : false),
  })
  const [revokeTarget, setRevokeTarget] = useState<DeviceItem | null>(null)
  const [revokeAllOpen, setRevokeAllOpen] = useState(false)

  const pairingMutation = useMutation({
    mutationFn: createDevicePairing,
    onSuccess: async (result) => {
      const current = await queryClient.fetchQuery({ queryKey: devicesQueryKeys.list(), queryFn: listDevices, staleTime: 0 })
      setKnownIds(new Set(current.map((item) => item.id)))
      setPairing(result)
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t('devices.pairing.createFailed')),
  })

  const revokeMutation = useMutation({
    mutationFn: (deviceId: string) => revokeDevice(deviceId),
    onSuccess: async () => {
      setRevokeTarget(null)
      await queryClient.invalidateQueries({ queryKey: devicesQueryKeys.list() })
      toast.success(t('devices.list.revoked'))
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t('devices.list.revokeFailed')),
  })

  const revokeAllMutation = useMutation({
    mutationFn: revokeAllDevices,
    onSuccess: async () => {
      setRevokeAllOpen(false)
      await queryClient.invalidateQueries({ queryKey: devicesQueryKeys.list() })
      toast.success(t('devices.list.allRevoked'))
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t('devices.list.revokeAllFailed')),
  })

  const devices = devicesQuery.data ?? []

  // 检测到新设备连上后自动关闭弹窗、刷新列表并提示。
  const connectedRef = useRef(false)
  useEffect(() => {
    if (!pairingOpen || !knownIds) return
    return queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== 'updated') return
      const list = queryClient.getQueryData<DeviceItem[]>(devicesQueryKeys.list())
      const paired = knownIds ? list?.find((item) => !knownIds.has(item.id)) : undefined
      if (!paired || connectedRef.current) return
      connectedRef.current = true
      setPairingOpen(false)
      setPairing(null)
      setKnownIds(null)
      void queryClient.invalidateQueries({ queryKey: devicesQueryKeys.list() })
      toast.success(t('devices.pairing.connected', { name: paired.name }))
    })
  }, [pairingOpen, knownIds, queryClient, t])

  function closePairingDialog() {
    setPairingOpen(false)
    setPairing(null)
    setKnownIds(null)
  }

  async function copyText(text: string, label: string) {
    await copyToClipboard(text, label, null)
  }

  return (
    <div className="max-w-5xl space-y-7">
      <PageHeader eyebrow={tc('nav.system')} title={t('devices.title')} description={t('devices.description')} />

      <section className="space-y-4">
        <h4 className="font-semibold text-label">{t('devices.pairing.title')}</h4>
        <p className="text-label-2 text-body">{t('devices.pairing.hint')}</p>
        <Button onClick={() => { connectedRef.current = false; setPairingOpen(true) }}>
          <Smartphone className="h-4 w-4" />
          {t('devices.pairing.generate')}
        </Button>
      </section>

      <section className="space-y-4 border-t border-separator pt-6">
        <div className="flex items-center justify-between gap-3">
          <h4 className="font-semibold text-label">{t('devices.list.title')}</h4>
          <Button
            variant="secondary"
            disabled={devices.length === 0 || revokeAllMutation.isPending}
            onClick={() => setRevokeAllOpen(true)}
          >
            {t('devices.list.revokeAll')}
          </Button>
        </div>
        {devicesQuery.isLoading ? (
          <p className="text-label-2 text-body">{t('devices.list.loading')}</p>
        ) : devicesQuery.isError ? (
          <p className="text-label-2 text-body">{t('devices.list.loadFailed')}</p>
        ) : devices.length === 0 ? (
          <div className="rounded-card border border-separator">
            <EmptyState icon={Smartphone} title={t('devices.list.empty')} description={t('devices.list.emptyDescription')} />
          </div>
        ) : (
          <ul className="divide-y divide-separator overflow-hidden rounded-card border border-separator">
            {devices.map((device) => (
              <li key={device.id} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3 py-3 sm:px-4">
                <div className="min-w-0 flex-1 space-y-0.5">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-body font-medium text-label">{device.name}</span>
                    {device.current ? <Badge variant="success">{t('devices.list.current')}</Badge> : null}
                  </div>
                  <p className="truncate text-footnote text-label-2">
                    {device.platform} · {device.last_ip ?? '-'} · {formatRelativeTime(device.last_seen_at ?? undefined, i18n.language) || '-'}
                  </p>
                </div>
                <Button variant="secondary" size="sm" disabled={revokeMutation.isPending} onClick={() => setRevokeTarget(device)}>
                  {t('devices.list.revoke')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <Dialog
        open={pairingOpen}
        onOpenChange={(nextOpen) => {
          if (!nextOpen && !pairingMutation.isPending) closePairingDialog()
        }}
      >
        <DialogContent presentation="center" className="w-[min(92vw,520px)] overflow-hidden">
          {pairing ? (
            <>
              <DialogHeader className="border-b-0 pb-2">
                <DialogTitle>{t('devices.pairing.title')}</DialogTitle>
              </DialogHeader>
              <DialogBody className="pt-0 text-body text-label-2">
                <div className="flex flex-col items-center gap-4">
                  <div className={cn('relative shrink-0', expired && 'opacity-40 grayscale')}>
                    <QRCode value={`xlyra://pair?u=${encodeURIComponent(origin)}&c=${pairing.code}`} size={160} />
                    {expired ? (
                      <span className="absolute inset-x-0 bottom-2 text-center text-footnote text-label">{t('devices.pairing.expired')}</span>
                    ) : null}
                  </div>
                  <p className="text-footnote text-label-2">{t('devices.pairing.scanHint')}</p>
                  <div className="w-full space-y-2">
                    <div className="flex items-center gap-2">
                      <span className="w-20 shrink-0 text-footnote text-label-2">{t('devices.pairing.serverAddress')}</span>
                      <span className="min-w-0 flex-1 truncate font-mono text-body text-label">{origin}</span>
                      <Button variant="secondary" size="sm" onClick={() => void copyText(origin, t('devices.pairing.addressCopied'))}>
                        <Copy className="h-3.5 w-3.5" />
                        {t('devices.pairing.copy')}
                      </Button>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="w-20 shrink-0 text-footnote text-label-2">{t('devices.pairing.codeLabel')}</span>
                      <span className="min-w-0 flex-1 truncate font-mono text-title-3 tracking-widest text-label">{pairing.code}</span>
                      <Button variant="secondary" size="sm" onClick={() => void copyText(pairing.code, t('devices.pairing.copied'))}>
                        <Copy className="h-3.5 w-3.5" />
                        {t('devices.pairing.copy')}
                      </Button>
                    </div>
                  </div>
                  <p className="text-body text-label-2">
                    {expired ? t('devices.pairing.expired') : t('devices.pairing.expiresIn', { time: formatCountdown(remaining) })}
                  </p>
                </div>
              </DialogBody>
              <DialogFooter className="border-t-0 pt-2">
                <Button
                  variant="secondary"
                  disabled={!expired || pairingMutation.isPending}
                  onClick={() => pairingMutation.mutate()}
                >
                  {pairingMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : <QrCode className="h-4 w-4" />}
                  {t('devices.pairing.regenerate')}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader className="border-b-0 pb-2">
                <DialogTitle>{t('devices.pairing.confirmTitle')}</DialogTitle>
              </DialogHeader>
              <DialogBody className="space-y-3 pt-0 text-body text-label-2">
                <DialogDescription className="mt-0">{t('devices.pairing.confirmDescription')}</DialogDescription>
                <p className="break-all rounded-card border border-separator bg-fill-2 px-3 py-2 text-center font-mono text-label">
                  {origin}
                </p>
              </DialogBody>
              <DialogFooter
                className="border-t-0 pt-2"
                cancel={<Button variant="secondary" onClick={closePairingDialog}>{tc('actions.cancel')}</Button>}
                confirm={(
                  <Button onClick={() => pairingMutation.mutate()} disabled={pairingMutation.isPending}>
                    {pairingMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : null}
                    {t('devices.pairing.confirmContinue')}
                  </Button>
                )}
              />
            </>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={revokeTarget !== null}
        title={t('devices.list.revokeDialog.title')}
        description={t('devices.list.revokeDialog.description')}
        confirmLabel={t('devices.list.revokeDialog.confirm')}
        pending={revokeMutation.isPending}
        destructive
        onCancel={() => setRevokeTarget(null)}
        onConfirm={() => revokeTarget && revokeMutation.mutate(revokeTarget.id)}
      />

      <ConfirmDialog
        open={revokeAllOpen}
        title={t('devices.list.revokeAllDialog.title')}
        description={t('devices.list.revokeAllDialog.description')}
        confirmLabel={t('devices.list.revokeAllDialog.confirm')}
        pending={revokeAllMutation.isPending}
        destructive
        onCancel={() => setRevokeAllOpen(false)}
        onConfirm={() => revokeAllMutation.mutate()}
      />
    </div>
  )
}

function useCountdownSeconds(expiresAt: string | null) {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!expiresAt) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [expiresAt])

  if (!expiresAt) return 0
  const expiry = new Date(expiresAt).getTime()
  if (!Number.isFinite(expiry)) return 0
  return Math.max(0, Math.floor((expiry - now) / 1000))
}

function formatCountdown(seconds: number) {
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return `${minutes}:${String(rest).padStart(2, '0')}`
}
