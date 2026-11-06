import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Circle, Search } from 'lucide-react'
import { BrandMark } from '@/components/common/brand-mark'
import { Button } from '@/components/ui/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { AGENT_CONFIG_DIALOG_CLASS } from '@/features/agent/components/agent-scope-config-dialog'
import { AgentConfirmDialog } from '@/features/agent/components/agent-confirm-dialog'
import { updateAgentFastModel } from '@/features/agent/api/agent'
import type { AgentFastModelCandidate } from '@/features/agent/lib/agent-fast-model'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { modelNameIconInfo } from '@/features/sites/lib/model-icon'
import { useMobileLayout } from '@/hooks/use-media-query'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

export function AgentFastModelDialog({ open, onOpenChange, candidates, current, loading }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  candidates: AgentFastModelCandidate[]
  current: string
  loading: boolean
}) {
  const { t } = useTranslation('agent')
  const mobileLayout = useMobileLayout()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<string | undefined>(undefined)
  const [search, setSearch] = useState('')
  const [confirmClose, setConfirmClose] = useState(false)
  const selected = draft ?? current
  const dirty = draft !== undefined && draft !== current
  const keyword = search.trim().toLowerCase()
  const filteredCandidates = useMemo(() => candidates.filter((candidate) =>
    !keyword || `${candidate.name} ${candidate.id} ${candidate.siteNames.join(' ')}`.toLowerCase().includes(keyword),
  ), [candidates, keyword])

  const save = useMutation({
    mutationFn: () => updateAgentFastModel(draft ?? current),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: agentSettingsKey })
      toast.success(t('settings.scope.saved'))
      close()
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  function close() {
    setDraft(undefined)
    setSearch('')
    onOpenChange(false)
  }

  function requestClose() {
    if (save.isPending) return
    if (dirty) setConfirmClose(true)
    else close()
  }

  return (
    <>
      <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) requestClose() }}>
        <DialogContent
          size="form"
          aria-describedby={undefined}
          overlayClassName="z-[60]"
          onBack={mobileLayout ? requestClose : undefined}
          backLabel={t('settings.tabScope')}
          className={AGENT_CONFIG_DIALOG_CLASS}
        >
          <DialogHeader>
            <DialogTitle>{t('settings.scope.fastDialogTitle')}</DialogTitle>
            <DialogDescription className="max-lg:sr-only">{t('settings.scope.fastDialogHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="flex min-h-0 flex-1 flex-col gap-3 overflow-hidden">
            <div className="relative shrink-0">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-foreground/40" />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('settings.scope.fastSearch')}
                className="h-9 pl-9"
              />
            </div>
            <div role="radiogroup" aria-label={t('settings.scope.fastModelTitle')} className="min-h-0 flex-1 space-y-1 overflow-y-auto overscroll-contain rounded-lg border border-[hsl(var(--glass-border))] p-1.5">
              <FastModelChoice
                label={t('settings.scope.fastFollowCurrent')}
                selected={selected === ''}
                onSelect={() => setDraft('')}
              />
              {loading ? (
                <div className="space-y-2 p-1.5">
                  <Skeleton className="h-12 w-full" />
                  <Skeleton className="h-12 w-full" />
                  <Skeleton className="h-12 w-full" />
                </div>
              ) : candidates.length === 0 ? (
                <p className="px-3 py-8 text-center text-xs leading-5 text-muted-soft">{t('settings.scope.fastEmpty')}</p>
              ) : filteredCandidates.length === 0 ? (
                <p className="px-3 py-8 text-center text-xs text-muted-soft">{t('settings.scope.noModels')}</p>
              ) : (
                filteredCandidates.map((candidate) => (
                  <FastModelChoice
                    key={candidate.id}
                    candidate={candidate}
                    selected={selected === candidate.id}
                    onSelect={() => setDraft(candidate.id)}
                  />
                ))
              )}
            </div>
          </DialogBody>
          <div className="shrink-0">
            {dirty ? <p className="px-6 pt-3 text-xs text-amber-500">{t('settings.scope.unsavedChanges')}</p> : null}
            <DialogFooter
              className={dirty ? 'border-t-0' : undefined}
              cancel={<Button variant="outline" onClick={requestClose} disabled={save.isPending}>{t('settings.cancel')}</Button>}
              confirm={<Button onClick={() => save.mutate()} disabled={!dirty || loading || save.isPending}>{t('settings.save')}</Button>}
            />
          </div>
        </DialogContent>
      </Dialog>
      <AgentConfirmDialog
        open={confirmClose}
        elevated
        title={t('settings.scope.discardTitle')}
        description={t('settings.scope.discardDescription')}
        confirmLabel={t('settings.scope.discardConfirm')}
        onCancel={() => setConfirmClose(false)}
        onConfirm={() => {
          setConfirmClose(false)
          close()
        }}
      />
    </>
  )
}

function FastModelChoice({ candidate, label, selected, onSelect }: {
  candidate?: AgentFastModelCandidate
  label?: string
  selected: boolean
  onSelect: () => void
}) {
  const icon = candidate ? modelNameIconInfo([candidate.name, candidate.id], candidate.id) : undefined
  const secondary = candidate ? [candidate.id, ...candidate.siteNames].join(' · ') : undefined
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        'flex w-full min-w-0 items-center gap-3 rounded-md border px-3 py-2.5 text-left transition-colors',
        selected ? 'border-tint bg-tint/5' : 'border-transparent hover:bg-fill-3',
      )}
    >
      {candidate && icon ? (
        <BrandMark iconPath={icon.iconPath} label={icon.label} fallback={icon.fallback} fallbackText={icon.fallbackText} size="sm" />
      ) : null}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-foreground" title={candidate?.name ?? label}>{candidate?.name ?? label}</span>
        {secondary ? <span className="mt-0.5 block truncate text-xs text-muted-soft" title={secondary}>{secondary}</span> : null}
      </span>
      <span
        aria-hidden="true"
        className={cn(
          'inline-flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] border-white/30 bg-fill-4 text-transparent',
          selected && 'border-tint bg-tint text-tint-foreground',
        )}
      >
        <Circle className="size-1.5 fill-current" strokeWidth={0} />
      </span>
    </button>
  )
}
