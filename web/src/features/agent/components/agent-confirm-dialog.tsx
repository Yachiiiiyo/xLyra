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
  /** 叠在更高 z-index 的弹窗之上时提升层级，否则遮罩与内容会被压在下面 */
  elevated?: boolean
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
  elevated,
  onCancel,
  onConfirm,
}: AgentConfirmDialogProps) {
  const { t } = useTranslation('common')
  // 配置弹窗等第二层弹窗用 z-[60]，确认弹窗必须再高一层
  const layerClassName = elevated ? 'z-[70]' : undefined

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="sm" presentation="center" overlayClassName={layerClassName} className={layerClassName}>
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
