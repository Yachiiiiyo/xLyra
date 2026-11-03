import { useTranslation } from 'react-i18next'
import { LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

type AgentConfirmDialogProps = {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  pending?: boolean
  destructive?: boolean
  onCancel: () => void
  onConfirm: () => void
}

/** Agent 页面专用的确认弹窗：主程序 Dialog（Liquid Glass thick 表面），居中呈现 */
export function AgentConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  pending = false,
  destructive,
  onCancel,
  onConfirm,
}: AgentConfirmDialogProps) {
  const { t } = useTranslation('common')

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="sm" presentation="center">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter
          cancel={<Button variant="outline" onClick={onCancel} disabled={pending}>{t('actions.cancel')}</Button>}
          confirm={(
            <Button variant={destructive ? 'destructive' : 'default'} onClick={onConfirm} disabled={pending}>
              {pending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : null}
              {confirmLabel}
            </Button>
          )}
        />
      </DialogContent>
    </Dialog>
  )
}
