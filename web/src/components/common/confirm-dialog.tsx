import { LoaderCircle } from 'lucide-react'
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

type ConfirmDialogProps = {
  open: boolean
  title: string
  description: string
  confirmLabel: string
  pending?: boolean
  destructive?: boolean
  onCancel: () => void
  onConfirm: () => void
}

/** 通用确认弹窗，与站点删除同一款：主程序 Dialog，标题在上、说明紧随其后、按钮靠右，页头页脚不带分隔线。 */
export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  pending = false,
  destructive,
  onCancel,
  onConfirm,
}: ConfirmDialogProps) {
  const { t } = useTranslation('common')
  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen && !pending) onCancel() }}>
      <DialogContent presentation="center" className="w-[min(92vw,520px)] overflow-hidden">
        <DialogHeader className="border-b-0 pb-2">
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <DialogBody className="pt-0 text-body text-label-2">
          <DialogDescription className="mt-0">{description}</DialogDescription>
        </DialogBody>
        <DialogFooter
          className="border-t-0 pt-2"
          cancel={<Button variant="secondary" onClick={onCancel} disabled={pending}>{t('actions.cancel')}</Button>}
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
