import { useState } from 'react'
import * as PopoverPrimitive from '@radix-ui/react-popover'
import { useTranslation } from 'react-i18next'
import { Check, Folder, Globe, ShieldCheck, ShieldEllipsis, ShieldQuestion, TerminalSquare, TriangleAlert } from 'lucide-react'
import { useLiquidGlassRef } from '@/components/glass/use-liquid-glass'
import { Button } from '@/components/ui/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { menuButtonClass, popoverPanelClass } from '@/components/ui/field-styles'
import { cn } from '@/lib/utils'
import { PERMISSION_MODE_KEY, type PermissionMode } from '@/features/agent/lib/agent-permission-mode'

/** Confirmation shown before enabling full-access permission mode. */
function FullAccessConfirmDialog({
  open,
  onCancel,
  onConfirm,
}: {
  open: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation('agent')
  const items = [
    { icon: Folder, title: t('composer.fullConfirmFiles'), description: t('composer.fullConfirmFilesDesc') },
    { icon: TerminalSquare, title: t('composer.fullConfirmCommands'), description: t('composer.fullConfirmCommandsDesc') },
    { icon: Globe, title: t('composer.fullConfirmNetwork'), description: t('composer.fullConfirmNetworkDesc') },
  ]
  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="sm" presentation="center" className="w-[min(92vw,480px)]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <TriangleAlert className="h-4 w-4 text-amber-500" />
            {t('composer.fullConfirmTitle')}
          </DialogTitle>
          <DialogDescription>{t('composer.fullConfirmDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div className="space-y-1 rounded-xl border border-[hsl(var(--glass-border))] bg-[hsl(var(--surface-subtle))]/60 p-2">
            {items.map((item) => (
              <div key={item.title} className="flex items-start gap-3 rounded-lg px-3 py-2.5">
                <item.icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-soft" />
                <div className="min-w-0">
                  <p className="text-sm font-medium text-foreground">{item.title}</p>
                  <p className="mt-0.5 text-xs leading-5 text-muted-soft">{item.description}</p>
                </div>
              </div>
            ))}
          </div>
          <p className="text-xs leading-5 text-muted-soft">{t('composer.fullConfirmRisk')}</p>
        </DialogBody>
        <DialogFooter
          cancel={<Button variant="outline" onClick={onCancel}>{t('composer.fullConfirmCancel')}</Button>}
          confirm={<Button variant="destructive" onClick={onConfirm}>{t('composer.fullConfirmAccept')}</Button>}
        />
      </DialogContent>
    </Dialog>
  )
}

export function AgentPermissionMenu({
  value,
  onChange,
  classifierModel,
  runtimeSettingsLoaded,
}: {
  value: PermissionMode
  onChange: (mode: PermissionMode) => void
  classifierModel?: string
  runtimeSettingsLoaded: boolean
}) {
  const { t } = useTranslation('agent')
  const [open, setOpen] = useState(false)
  const [confirmFullAccess, setConfirmFullAccess] = useState(false)
  const glassRef = useLiquidGlassRef<HTMLDivElement>('regular')
  const modes = [
    { value: 'ask' as const, icon: ShieldQuestion, label: t('composer.permissionAsk'), hint: t('composer.permissionAskHint') },
    { value: 'auto' as const, icon: ShieldEllipsis, label: t('composer.permissionAuto'), hint: t('composer.permissionAutoHint') },
    { value: 'full' as const, icon: ShieldCheck, label: t('composer.permissionFull'), hint: t('composer.permissionFullHint') },
  ]
  const current = modes.find((mode) => mode.value === value) ?? modes[0]!

  function applyMode(next: PermissionMode) {
    onChange(next)
    try {
      window.localStorage.setItem(PERMISSION_MODE_KEY, next)
    } catch { /* ignore */ }
  }

  function selectMode(next: PermissionMode) {
    setOpen(false)
    if (next === 'full' && value !== 'full') {
      setConfirmFullAccess(true)
      return
    }
    applyMode(next)
  }

  return (
    <>
      <PopoverPrimitive.Root open={open} onOpenChange={setOpen}>
        <PopoverPrimitive.Trigger asChild>
          <button
            type="button"
            className="flex h-11 items-center gap-1.5 rounded-full px-2.5 text-footnote font-medium text-label-2 transition-colors active:bg-fill-3 active:text-label md:h-8 md:hover:bg-fill-3 md:hover:text-label"
            aria-label={t('composer.permissionMode')}
            title={current.hint}
          >
            <current.icon className="h-4 w-4" />
            <span className="hidden sm:inline">{current.label}</span>
          </button>
        </PopoverPrimitive.Trigger>
        <PopoverPrimitive.Portal>
          <PopoverPrimitive.Content
            ref={glassRef}
            side="top"
            align="start"
            sideOffset={10}
            collisionPadding={12}
            role="menu"
            aria-label={t('composer.permissionMode')}
            className={cn(popoverPanelClass, 'z-[130] w-[min(calc(100vw-24px),320px)] origin-(--radix-popover-content-transform-origin)')}
          >
            {modes.map((mode) => (
              <button
                key={mode.value}
                type="button"
                role="menuitemradio"
                aria-checked={mode.value === value}
                className={cn(menuButtonClass, 'items-start')}
                onClick={() => selectMode(mode.value)}
              >
                <mode.icon className="mt-0.5 h-4 w-4 shrink-0 text-label-2" />
                <span className="min-w-0 flex-1">
                  <span className="block text-sm font-medium text-label">{mode.label}</span>
                  <span className="mt-0.5 block text-xs leading-5 text-muted-soft">{mode.hint}</span>
                  {mode.value === 'auto' && runtimeSettingsLoaded && !classifierModel ? (
                    <span className="mt-1 block text-xs leading-5 text-amber-500">{t('composer.permissionAutoUnsetHint')}</span>
                  ) : null}
                </span>
                <span className="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center">
                  {mode.value === value ? <Check className="h-4 w-4" strokeWidth={2.5} /> : null}
                </span>
              </button>
            ))}
          </PopoverPrimitive.Content>
        </PopoverPrimitive.Portal>
      </PopoverPrimitive.Root>
      <FullAccessConfirmDialog
        open={confirmFullAccess}
        onCancel={() => setConfirmFullAccess(false)}
        onConfirm={() => {
          applyMode('full')
          setConfirmFullAccess(false)
        }}
      />
    </>
  )
}
