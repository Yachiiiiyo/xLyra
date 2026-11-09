import { useTranslation } from 'react-i18next'
import { AlertTriangle, CheckCircle2, CircleSlash } from 'lucide-react'
import type { BackupInspectResult } from '@/features/settings/api/settings'

/** 恢复前预览：展示将恢复、将清空、无法恢复的内容 */
export function RestorePreview({ result }: { result: BackupInspectResult }) {
  const { t } = useTranslation('settings')
  const info = result.backup
  const sectionLabel = (id: string) => t(`backup.sections.items.${id}.label`, { defaultValue: id })
  const createdAt = formatDateTime(info.created_at)

  return (
    <div className="space-y-4">
      <div className="space-y-1 text-body text-label-2">
        <div>{t('backup.restorePreview.createdAt')}：{createdAt}</div>
        <div>{t('backup.restorePreview.formatVersion')}：v{info.format_version}</div>
        {info.legacy ? <div className="text-footnote text-warning">{t('backup.restorePreview.legacy')}</div> : null}
      </div>

      <section className="space-y-2">
        <div className="text-body font-medium text-label">{t('backup.restorePreview.restoreTitle')}</div>
        <ul className="divide-y divide-separator overflow-hidden rounded-card border border-separator">
          {info.sections.map((id) => (
            <li key={id} className="flex items-center gap-3 px-4 py-2.5 text-body text-label">
              <CheckCircle2 aria-hidden className="h-4 w-4 shrink-0 text-success" />
              <span className="min-w-0 flex-1">{sectionLabel(id)}</span>
            </li>
          ))}
        </ul>
      </section>

      {info.impact.length > 0 ? (
        <section className="space-y-2" role="alert">
          <div className="flex items-center gap-2 text-body font-medium text-danger">
            <AlertTriangle aria-hidden className="h-4 w-4 shrink-0" />
            {t('backup.restorePreview.clearTitle')}
          </div>
          <ul className="divide-y divide-separator overflow-hidden rounded-card border border-danger/35 bg-danger/10">
            {info.impact.map((item) => (
              <li key={item.id} className="flex items-center gap-3 px-4 py-2.5 text-body text-danger">
                <CircleSlash aria-hidden className="h-4 w-4 shrink-0" />
                <span className="min-w-0 flex-1">
                  {sectionLabel(item.id)}
                  {item.partial ? t('backup.restorePreview.partial') : null}
                </span>
              </li>
            ))}
          </ul>
          <p className="text-footnote text-label-2">{t('backup.restorePreview.clearHint')}</p>
        </section>
      ) : null}

      {info.unavailable.length > 0 ? (
        <section className="space-y-2" role="alert">
          <div className="flex items-center gap-2 text-body font-medium text-warning">
            <AlertTriangle aria-hidden className="h-4 w-4 shrink-0" />
            {t('backup.restorePreview.unavailableTitle')}
          </div>
          <ul className="divide-y divide-separator overflow-hidden rounded-card border border-warning/35 bg-warning/10">
            {info.unavailable.map((item) => (
              <li key={item.id} className="px-4 py-2.5 text-body">
                <span className="text-label">{sectionLabel(item.id)}</span>
                <span className="ml-2 text-footnote text-label-2">{item.reason}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  )
}

function formatDateTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}
