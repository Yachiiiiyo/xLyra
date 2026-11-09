import { useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import type { BackupSectionInfo } from '@/features/settings/api/settings'
import { BackupSectionPicker } from '@/features/settings/components/backup/backup-section-picker'

type BackupSectionsFieldProps = {
  title: string
  hint?: string
  sections: BackupSectionInfo[] | undefined
  loading: boolean
  error: boolean
  value: string[]
  onChange: (next: string[]) => void
  disabled?: boolean
  emptyError?: boolean
}

export function BackupSectionsField({
  title,
  hint,
  sections,
  loading,
  error,
  value,
  onChange,
  disabled = false,
  emptyError = false,
}: BackupSectionsFieldProps) {
  const { t } = useTranslation('settings')
  const [open, setOpen] = useState(false)
  const selected = new Set(value)
  const labels = sections?.filter((section) => selected.has(section.id)).map((section) => (
    t(`backup.sections.items.${section.id}.label`, { defaultValue: section.id })
  )) ?? []
  const names = labels.slice(0, 3).join('、') + (labels.length > 3 ? '等' : '')
  const summary = loading
    ? t('backup.sections.loading')
    : error
      ? t('backup.sections.loadFailed')
      : labels.length > 0
        ? t('backup.sections.summary', { count: labels.length, names })
        : t('backup.sections.summaryNone')

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-body font-medium text-label">{title}</div>
          <p className="mt-0.5 text-footnote text-label-2">{summary}</p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setOpen(true)}
          disabled={disabled || loading || error || !sections}
        >
          {t('backup.sections.choose')}
        </Button>
      </div>
      {hint ? <p className="text-footnote text-label-2">{hint}</p> : null}
      {emptyError ? <p className="text-footnote text-danger">{t('backup.sections.atLeastOne')}</p> : null}
      {open && sections ? (
        <BackupSectionsDialog
          sections={sections}
          value={value}
          disabled={disabled}
          onCancel={() => setOpen(false)}
          onConfirm={(next) => {
            onChange(next)
            setOpen(false)
          }}
        />
      ) : null}
    </div>
  )
}

function BackupSectionsDialog({
  sections,
  value,
  disabled,
  onCancel,
  onConfirm,
}: {
  sections: BackupSectionInfo[]
  value: string[]
  disabled: boolean
  onCancel: () => void
  onConfirm: (next: string[]) => void
}) {
  const { t } = useTranslation('settings')
  const [draft, setDraft] = useState(value)
  const empty = draft.length === 0

  return (
    <Dialog open onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t('backup.sections.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('backup.sections.dialogDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <BackupSectionPicker sections={sections} value={draft} onChange={setDraft} disabled={disabled} />
          {empty ? <p className="text-footnote text-danger">{t('backup.sections.atLeastOne')}</p> : null}
        </DialogBody>
        <DialogFooter
          cancel={(
            <Button variant="secondary" onClick={onCancel}>
              {t('common:actions.cancel', { ns: 'common' })}
            </Button>
          )}
          confirm={(
            <Button onClick={() => onConfirm(draft)} disabled={disabled || empty}>
              {t('backup.sections.done')}
            </Button>
          )}
        />
      </DialogContent>
    </Dialog>
  )
}
