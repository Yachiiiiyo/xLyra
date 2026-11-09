import { useId, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Checkbox } from '@/components/ui/checkbox'
import type { BackupSectionInfo } from '@/features/settings/api/settings'
import {
  deselectSection,
  formatSectionSize,
  groupSections,
  lockedSections,
  missingRecommends,
  requiredByMap,
  selectSection,
} from '@/features/settings/lib/backup-sections'
import { cn } from '@/lib/utils'

type BackupSectionPickerProps = {
  /** 后端目录 */
  sections: BackupSectionInfo[]
  /** 当前选择 */
  value: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
}

export function BackupSectionPicker({ sections, value, onChange, disabled = false }: BackupSectionPickerProps) {
  const { t, i18n } = useTranslation('settings')
  const groups = useMemo(() => groupSections(sections), [sections])
  const locked = useMemo(() => lockedSections(sections, value), [sections, value])
  const requiredBy = useMemo(() => requiredByMap(sections, value), [sections, value])
  const recommendsMap = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const item of missingRecommends(sections, value)) map.set(item.id, item.recommends)
    return map
  }, [sections, value])
  const selected = useMemo(() => new Set(value), [value])

  function sectionLabel(id: string) {
    return t(`backup.sections.items.${id}.label`, { defaultValue: id })
  }

  return (
    <div className="space-y-3">
      {groups.map((group) => (
        <div key={group.group} className="space-y-2">
          <div className="text-footnote font-medium text-label-2">{t(`backup.sections.groups.${group.group}`, { defaultValue: group.group })}</div>
          <div className="divide-y divide-separator overflow-hidden rounded-card border border-separator">
            {group.items.map((info) => (
              <BackupSectionItem
                key={info.id}
                info={info}
                checked={selected.has(info.id)}
                locked={locked.has(info.id)}
                requiredBy={requiredBy.get(info.id)?.map(sectionLabel) ?? []}
                missingRecommends={recommendsMap.get(info.id)?.map(sectionLabel) ?? []}
                disabled={disabled}
                label={sectionLabel(info.id)}
                description={t(`backup.sections.items.${info.id}.description`, { defaultValue: '' })}
                sizeText={sectionSizeText(info, i18n.language ?? 'en', t)}
                onToggle={(next) => {
                  onChange(next ? selectSection(sections, value, info.id) : deselectSection(sections, value, info.id))
                }}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

function sectionSizeText(info: BackupSectionInfo, locale: string, t: (key: string, options?: Record<string, unknown>) => string) {
  if (info.size_bytes !== undefined && info.size_bytes !== null) {
    return t('backup.sections.sizeBytes', { size: formatSectionSize(info, locale) })
  }
  if (info.rows !== undefined && info.rows !== null) {
    return t('backup.sections.sizeRows', { count: formatSectionSize(info, locale) })
  }
  return ''
}

function BackupSectionItem({
  info,
  checked,
  locked,
  requiredBy,
  missingRecommends,
  disabled,
  label,
  description,
  sizeText,
  onToggle,
}: {
  info: BackupSectionInfo
  checked: boolean
  locked: boolean
  requiredBy: string[]
  missingRecommends: string[]
  disabled: boolean
  label: string
  description: string
  sizeText: string
  onToggle: (next: boolean) => void
}) {
  const { t } = useTranslation('settings')
  const labelId = useId()
  const checkboxId = `${labelId}-${info.id}`
  const itemDisabled = disabled || !info.available || locked
  const hints: string[] = []
  if (!info.available) hints.push(t('backup.sections.unavailable', { reason: info.unavailable_reason ?? '' }))
  if (requiredBy.length > 0) hints.push(t('backup.sections.requiredBy', { names: requiredBy.join('、') }))
  if (missingRecommends.length > 0) hints.push(t('backup.sections.recommends', { names: missingRecommends.join('、') }))

  return (
    <div className="px-4 py-3">
      <div className="flex items-start gap-3">
        <Checkbox
          id={checkboxId}
          checked={checked}
          disabled={itemDisabled}
          ariaLabel={label}
          onCheckedChange={onToggle}
        />
        <label
          htmlFor={checkboxId}
          className={cn('min-w-0 flex-1 space-y-0.5 text-body text-label', itemDisabled ? 'cursor-not-allowed opacity-40' : 'cursor-pointer')}
        >
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <span>{label}</span>
            {sizeText ? <span className="text-footnote text-label-2">{sizeText}</span> : null}
          </span>
          {description ? <span className="block text-footnote text-label-2">{description}</span> : null}
        </label>
      </div>
      {hints.length > 0 ? (
        <div className="mt-1.5 space-y-0.5 pl-[calc(var(--check-size)+0.75rem)] text-footnote text-label-2">
          {hints.map((hint) => (
            <div key={hint}>{hint}</div>
          ))}
        </div>
      ) : null}
    </div>
  )
}
