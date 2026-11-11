import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'
import type { DeviceItem, DevicePush, DevicePushPrefs } from '@/features/auth/api/paired-devices'

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

export function DevicePushStatus({ device }: { device: DeviceItem }) {
  const { t, i18n } = useTranslation('settings')
  const push = device.push
  const registered = push?.registered === true

  if (!registered || !push) {
    return (
      <div className="space-y-1 pt-1">
        <Badge variant="neutral">{t('devices.push.unregistered')}</Badge>
        <p className="text-footnote text-label-2">{t('devices.push.unregisteredHint')}</p>
      </div>
    )
  }

  const variant = push.state === 'invalid' || push.state === 'error' ? 'warning' : 'success'
  const label = push.state === 'invalid'
    ? t('devices.push.invalid')
    : push.state === 'error'
      ? t('devices.push.error')
      : t('devices.push.ok')
  const meta = registeredMeta(push, i18n.language, t)
  const offSummary = closedPrefsSummary(push.prefs, t)

  return (
    <div className="space-y-1 pt-1">
      <Badge variant={variant}>{label}</Badge>
      {meta ? <p className="text-footnote text-label-2">{meta}</p> : null}
      {push.state === 'invalid' ? <p className="text-footnote text-label-2">{t('devices.push.invalidHint')}</p> : null}
      {offSummary ? <p className="text-footnote text-label-2">{offSummary}</p> : null}
    </div>
  )
}

function registeredMeta(push: DevicePush, locale: string, t: (key: string, options?: Record<string, unknown>) => string) {
  const parts: string[] = []
  if (push.environment === 'sandbox') parts.push(t('devices.push.env.sandbox'))
  if (push.environment === 'production') parts.push(t('devices.push.env.production'))
  const registeredAt = formatRelativeTime(push.registered_at ?? undefined, locale)
  if (registeredAt) parts.push(t('devices.push.registeredAt', { time: registeredAt }))
  return parts.join(' · ')
}

function closedPrefsSummary(prefs: DevicePushPrefs, t: (key: string, options?: Record<string, unknown>) => string) {
  const closed = prefKeys.filter((key) => !prefs[key])
  if (closed.length === 0) return ''
  const names = closed.map((key) => t(`devices.push.kinds.${key}`)).join(t('devices.push.listSeparator'))
  return t('devices.push.prefsOff', { names })
}
