import { useCallback, useRef, useState } from 'react'
import type { DragEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { DatabaseBackup, FileArchive, LoaderCircle, Upload, X } from 'lucide-react'
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
import { Input } from '@/components/ui/input'
import { inspectBackupFile, type BackupInspectResult } from '@/features/settings/api/settings'
import { RestorePreview } from '@/features/settings/components/backup/restore-preview'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

type ImportBackupDialogProps = {
  open: boolean
  onClose: () => void
  onConfirm: (file: File, passphrase: string) => void
  disabled?: boolean
}

/** 手动导入备份的两步对话框：选文件 + 口令 → 恢复前预览 → 确认恢复 */
export function ImportBackupDialog({ open, onClose, onConfirm, disabled = false }: ImportBackupDialogProps) {
  const { t } = useTranslation('settings')
  const [file, setFile] = useState<File | null>(null)
  const [passphrase, setPassphrase] = useState('')
  const [preview, setPreview] = useState<BackupInspectResult | null>(null)
  const [dragging, setDragging] = useState(false)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const inspectMutation = useMutation({
    mutationFn: (input: { file: File; passphrase: string }) => inspectBackupFile(input.file, input.passphrase),
    onSuccess: (result) => {
      setPreview(result)
    },
    onError: (error) => {
      toast.error(t('backup.import.inspectFailed'), { description: error.message })
    },
  })

  function reset() {
    setFile(null)
    setPassphrase('')
    setPreview(null)
    setDragging(false)
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  function close() {
    reset()
    inspectMutation.reset()
    onClose()
  }

  const selectFile = useCallback((next: File | null) => {
    if (!next) return
    if (!isBackupFile(next)) {
      toast.error(t('backup.validation.fileInvalid'))
      return
    }
    setFile(next)
    // 换了文件，旧预览不再可信，回到第一步。
    setPreview(null)
  }, [t])

  const handleDrop = useCallback((event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    setDragging(false)
    selectFile(event.dataTransfer.files.item(0))
  }, [selectFile])

  function handleNext() {
    if (disabled || inspectMutation.isPending) return
    if (!file) {
      toast.error(t('backup.validation.fileRequired'))
      return
    }
    if (!isBackupFile(file)) {
      toast.error(t('backup.validation.fileInvalid'))
      return
    }
    if (!passphrase.trim()) {
      toast.error(t('backup.validation.passphraseRequired'))
      return
    }
    inspectMutation.mutate({ file, passphrase })
  }

  function handleConfirm() {
    if (!file || !passphrase) return
    onConfirm(file, passphrase)
    reset()
  }

  const unavailable = preview ? preview.backup.unavailable.length > 0 : false

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) close() }}>
      <DialogContent className="w-[min(92vw,620px)]">
        <DialogHeader>
          <DialogTitle>{t('backup.import.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('backup.import.confirmDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {preview ? (
            <RestorePreview result={preview} />
          ) : (
            <>
              <div
                onDrop={handleDrop}
                onDragOver={(event) => {
                  event.preventDefault()
                  setDragging(true)
                }}
                onDragLeave={(event) => {
                  event.preventDefault()
                  setDragging(false)
                }}
                onClick={() => fileInputRef.current?.click()}
                className={cn(
                  'flex cursor-pointer flex-col items-center justify-center gap-2 rounded-inner border-2 border-dashed px-4 py-8 text-center transition-colors',
                  dragging
                    ? 'border-tint bg-tint/5'
                    : 'border-separator bg-fill-4 hover:border-tint/50 hover:bg-fill-3',
                )}
              >
                <Upload className="h-8 w-8 text-label-2" />
                <div className="text-body font-medium text-label">{t('backup.import.dropzone')}</div>
                <div className="text-footnote text-label-2">{t('backup.import.fileHint')}</div>
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".xlyra,application/vnd.xlyra.backup"
                  className="hidden"
                  onChange={(event) => {
                    selectFile(event.target.files?.item(0) ?? null)
                    event.target.value = ''
                  }}
                />
              </div>

              {file ? (
                <div className="flex items-center gap-3 rounded-control border border-separator bg-fill-4 px-3 py-2">
                  <FileArchive className="h-4 w-4 shrink-0 text-label-2" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-body text-label">{file.name}</div>
                    <div className="text-footnote text-label-2">{formatFileSize(file.size)}</div>
                  </div>
                  <button
                    type="button"
                    onClick={() => {
                      setFile(null)
                      setPreview(null)
                    }}
                    className="shrink-0 rounded p-1 text-label-2 transition-colors hover:bg-fill-3 hover:text-label"
                    aria-label={t('backup.import.removeFile')}
                  >
                    <X className="h-3.5 w-3.5" />
                  </button>
                </div>
              ) : null}

              <FormField label={t('backup.passphrase')} description={t('backup.import.passphraseDesc')} required>
                <Input
                  type="password"
                  autoComplete="current-password"
                  value={passphrase}
                  onChange={(event) => {
                    setPassphrase(event.target.value)
                    // 口令变化后旧预览不再可信，回到第一步。
                    setPreview(null)
                  }}
                />
              </FormField>
            </>
          )}
        </DialogBody>
        <DialogFooter
          cancel={(
            <Button variant="secondary" onClick={preview ? () => setPreview(null) : close} disabled={inspectMutation.isPending}>
              {preview ? t('backup.import.backAction') : t('common:actions.cancel', { ns: 'common' })}
            </Button>
          )}
          confirm={preview ? (
            <Button variant="destructive" onClick={handleConfirm} disabled={disabled || unavailable}>
              <DatabaseBackup className="h-4 w-4" />
              {t('backup.import.confirmAction')}
            </Button>
          ) : (
            <Button variant="destructive" onClick={handleNext} disabled={disabled || inspectMutation.isPending}>
              {inspectMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : <DatabaseBackup className="h-4 w-4" />}
              {t('backup.import.previewAction')}
            </Button>
          )}
        />
      </DialogContent>
    </Dialog>
  )
}

function isBackupFile(file: File) {
  return file.name.endsWith('.xlyra') || file.type === 'application/vnd.xlyra.backup'
}

function formatFileSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
