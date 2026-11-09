import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { DatabaseBackup, LoaderCircle } from 'lucide-react'
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
import { inspectAutomaticBackupFile, type AutomaticBackupFile } from '@/features/settings/api/settings'
import { RestorePreview } from '@/features/settings/components/backup/restore-preview'

type AutomaticRestoreDialogProps = {
  file: AutomaticBackupFile | null
  onCancel: () => void
  onConfirm: (file: AutomaticBackupFile) => void
}

/** 自动备份文件的恢复确认：打开时读取预览，确认后才发起恢复 */
export function AutomaticRestoreDialog({ file, onCancel, onConfirm }: AutomaticRestoreDialogProps) {
  const { t } = useTranslation('settings')
  const inspectQuery = useQuery({
    queryKey: ['settings', 'backup', 'automatic', 'files', 'inspect', file?.key],
    queryFn: ({ signal }) => inspectAutomaticBackupFile(file!.key, signal),
    enabled: Boolean(file),
    retry: false,
  })
  const result = inspectQuery.data
  const unavailable = Boolean(result && result.backup.unavailable.length > 0)
  const confirmDisabled = inspectQuery.isPending || inspectQuery.isError || unavailable

  return (
    <Dialog open={Boolean(file)} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent presentation="center" className="w-[min(92vw,560px)]">
        <DialogHeader>
          <DialogTitle>{t('backup.automatic.restoreConfirmTitle')}</DialogTitle>
          <DialogDescription>{file ? t('backup.automatic.restoreConfirmDescription', { filename: file.filename }) : ''}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {inspectQuery.isPending ? (
            <div role="status" className="flex items-center gap-3 rounded-card bg-fill-3 px-4 py-3 text-body text-label-2">
              <LoaderCircle aria-hidden className="h-4 w-4 animate-spin" />
              {t('backup.restorePreview.loading')}
            </div>
          ) : inspectQuery.isError ? (
            <div role="alert" className="rounded-card border border-danger/35 bg-danger/80 px-4 py-3 text-body leading-6 text-danger">
              {t('backup.import.inspectFailed')}
              {inspectQuery.error instanceof Error && inspectQuery.error.message ? `：${inspectQuery.error.message}` : ''}
            </div>
          ) : result ? (
            <RestorePreview result={result} />
          ) : null}
        </DialogBody>
        <DialogFooter
          cancel={(
            <Button variant="secondary" onClick={onCancel}>
              {t('common:actions.cancel', { ns: 'common' })}
            </Button>
          )}
          confirm={(
            <Button variant="destructive" onClick={() => { if (file) onConfirm(file) }} disabled={confirmDisabled}>
              <DatabaseBackup className="h-4 w-4" />
              {t('backup.automatic.restoreConfirmAction')}
            </Button>
          )}
        />
      </DialogContent>
    </Dialog>
  )
}
