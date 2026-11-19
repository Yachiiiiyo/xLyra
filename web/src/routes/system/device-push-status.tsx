import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { formatAbsoluteTime } from '@/features/agent/lib/relative-time'
import { devicesQueryKeys, sendDevicePushTest, type DeviceItem, type DevicePush, type DevicePushPrefs } from '@/features/auth/api/paired-devices'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

const prefKeys = ['approval', 'question', 'done', 'error'] as const satisfies ReadonlyArray<keyof DevicePushPrefs>

export function DeviceCloudPushNotice() {
  const { t } = useTranslation('settings')
  return (
    <p className="rounded-card border border-separator bg-fill-2 px-3 py-2 text-footnote text-label-2">
      {t('devices.push.cloudRequired')}
      {' '}
      <Link to="/system/xlyra-cloud" className="text-tint underline">
        {t('devices.push.cloudLink')}
      </Link>
    </p>
  )
}

export function DevicePushBadge({ device, cloudBound }: { device: DeviceItem, cloudBound?: boolean }) {
  const { t } = useTranslation('settings')
  const push = device.push
  if (push?.registered !== true) return <Badge variant="neutral">{t('devices.push.unregistered')}</Badge>
  if (cloudBound === false) return <Badge variant="warning">{t('devices.push.cloudUnbound')}</Badge>
  const variant = push.state === 'invalid' || push.state === 'error' ? 'warning' : 'success'
  const label = push.state === 'invalid'
    ? t('devices.push.invalid')
    : push.state === 'error'
      ? t('devices.push.error')
      : t('devices.push.ok')
  return <Badge variant={variant}>{label}</Badge>
}

export function DevicePushDetail({ device, cloudBound }: { device: DeviceItem, cloudBound?: boolean }) {
  const { t, i18n } = useTranslation('settings')
  const push = device.push
  if (push?.registered !== true) {
    return <p className="text-footnote text-label-2">{t('devices.push.unregisteredHint')}</p>
  }
  const parts = [
    registeredMeta(push, i18n.language, t),
    closedPrefsSummary(push.prefs, t),
  ].filter(Boolean)
  return (
    <>
      {cloudBound === false ? <p className="text-footnote text-label-2">{t('devices.push.cloudUnboundHint')}</p> : null}
      {push.state === 'invalid' ? <p className="text-footnote text-label-2">{t('devices.push.invalidHint')}</p> : null}
      {parts.length > 0 ? <p className="truncate text-footnote text-label-2">{parts.join(' · ')}</p> : null}
    </>
  )
}

export function DevicePushTestButton({ device, cloudBound }: { device: DeviceItem, cloudBound?: boolean }) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const [sending, setSending] = useState(false)
  if (device.push?.registered !== true) return null
  const blocked = cloudBound === false

  return (
    <Button
      variant="secondary"
      size="sm"
      loading={sending}
      disabled={sending || blocked}
      title={blocked ? t('devices.push.test.cloudNotBound') : undefined}
      onClick={() => { void sendTest() }}
    >
      {t('devices.push.test.send')}
    </Button>
  )

  async function sendTest() {
    setSending(true)
    try {
      const payload = await sendDevicePushTest(device.id)
      toast.success(t(`devices.push.test.result.${payload.result}`))
    } catch (error) {
      if (error instanceof APIError && error.status === 409 && error.code === 'cloud_not_bound') {
        toast.error(t('devices.push.test.cloudNotBound'))
      } else if (error instanceof APIError && error.status === 409 && error.code === 'push_not_registered') {
        toast.error(error.message)
      } else {
        toast.error(error instanceof Error ? error.message : t('devices.push.test.failed'))
      }
    } finally {
      setSending(false)
      void queryClient.invalidateQueries({ queryKey: devicesQueryKeys.list() })
    }
  }
}

function registeredMeta(push: DevicePush, locale: string, t: (key: string, options?: Record<string, unknown>) => string) {
  const parts: string[] = []
  const registeredAt = formatAbsoluteTime(push.registered_at ?? undefined, locale)
  if (registeredAt) parts.push(t('devices.push.registeredAt', { time: registeredAt }))
  return parts.join(' · ')
}

function closedPrefsSummary(prefs: DevicePushPrefs, t: (key: string, options?: Record<string, unknown>) => string) {
  const closed = prefKeys.filter((key) => !prefs[key])
  if (closed.length === 0) return ''
  const names = closed.map((key) => t(`devices.push.kinds.${key}`)).join(t('devices.push.listSeparator'))
  return t('devices.push.prefsOff', { names })
}
