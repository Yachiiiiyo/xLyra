import { useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, X } from 'lucide-react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { AGENT_CONFIG_DIALOG_CLASS } from '@/features/agent/components/agent-scope-config-dialog'
import { AgentConfirmDialog } from '@/features/agent/components/agent-confirm-dialog'
import { dedupeImageModels, fetchAgentRuntimeSettings, updateAgentImageModels, type AgentImageModelSelection } from '@/features/agent/api/agent'
import { agentImageModelGroups, agentImageModelGroupViews, isImageModelSiteSelected, moveImageModel, removeImageModel, toggleImageModelSite } from '@/features/agent/lib/agent-image-model'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { listCanonicalModels, listSiteModels, listSitesWithOAuth, sitesQueryKeys } from '@/features/sites/api/sites'
import { siteModelItems } from '@/features/sites/lib/site-cache'
import { useMobileLayout } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'
import { toast } from '@/lib/toast'

/**
 * 生图模型配置:左侧只列出「带有生图模型」的站点,右侧是该站点的生图模型(多选);
 * 站点与模型是两个独立维度,选择的是「站点 + 模型」这一对,同名模型在别的站点互不影响。
 * 已选按模型名去重并排序,执行时先按模型顺序、再在该模型选中的站点内按路由兜底。
 * 候选来自系统内全部已启用站点,与 Agent 的可用站点/模型白名单互不影响。
 */
export function AgentImageModelDialog({ open, onOpenChange }: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('agent')
  const mobileLayout = useMobileLayout()
  const queryClient = useQueryClient()
  const runtime = useQuery({ queryKey: [...agentSettingsKey, 'runtime'], queryFn: fetchAgentRuntimeSettings, retry: false, enabled: open })
  const sitesQuery = useQuery({ queryKey: [...agentSettingsKey, 'sites'], queryFn: listSitesWithOAuth, retry: false, enabled: open })
  const canonicalQuery = useQuery({ queryKey: [...agentSettingsKey, 'canonical-models'], queryFn: listCanonicalModels, retry: false, enabled: open })
  const [draft, setDraft] = useState<AgentImageModelSelection[] | undefined>(undefined)
  const [activeSiteId, setActiveSiteId] = useState<string | null>(null)
  const [confirmClose, setConfirmClose] = useState(false)
  const enabledSites = useMemo(() => (sitesQuery.data?.items ?? []).filter((site) => site.enabled), [sitesQuery.data])
  const canonicalById = useMemo(() => new Map((canonicalQuery.data?.items ?? []).map((model) => [model.id, model])), [canonicalQuery.data])
  const modelQueries = useQueries({
    queries: enabledSites.map((site) => ({
      queryKey: sitesQueryKeys.models(site.id),
      queryFn: () => listSiteModels(site.id),
      retry: false,
      enabled: open,
    })),
  })
  const groups = useMemo(() => {
    const modelsBySite = new Map(enabledSites.map((site, index) => [site.id, siteModelItems(modelQueries[index]?.data)]))
    return agentImageModelGroupViews(agentImageModelGroups(enabledSites, modelsBySite, canonicalById), canonicalById)
  }, [enabledSites, modelQueries, canonicalById])
  const names = useMemo(() => new Map(groups.flatMap((group) => group.models.map(({ model, key }) => [key, model.display_name || key] as const))), [groups])
  const siteNames = useMemo(() => new Map(groups.map((group) => [group.site.id, group.site.name] as const)), [groups])
  // 每个模型 key 实际由哪些站点提供(「不限站点」的旧条目按它展开)
  const providers = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const group of groups) for (const { key } of group.models) map.set(key, [...(map.get(key) ?? []), group.site.id])
    return map
  }, [groups])
  const loading = sitesQuery.isLoading || modelQueries.some((query) => query.isLoading)
  const failed = sitesQuery.isError || modelQueries.some((query) => query.isError)
  const selected = draft ?? runtime.data?.image_models ?? []
  const activeGroup = groups.find((group) => group.site.id === activeSiteId) ?? groups[0]
  // 与已保存的配置逐项比较:改了又改回来不算未保存
  const dirty = draft !== undefined && JSON.stringify(dedupeImageModels(draft)) !== JSON.stringify(dedupeImageModels(runtime.data?.image_models ?? []))

  const save = useMutation({
    mutationFn: () => updateAgentImageModels(selected),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: agentSettingsKey })
      toast.success(t('settings.scope.saved'))
      close()
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  function close() {
    setDraft(undefined)
    setActiveSiteId(null)
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
            <DialogTitle>{t('settings.scope.imageDialogTitle')}</DialogTitle>
            <DialogDescription className="max-lg:sr-only">{t('settings.scope.imageDialogHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-5">
            {loading ? (
              <div className="space-y-2">
                <Skeleton className="h-9 w-full" />
                <Skeleton className="h-9 w-full" />
                <Skeleton className="h-9 w-full" />
              </div>
            ) : failed ? (
              <div className="flex flex-col items-center gap-2 py-8">
                <p className="text-center text-xs text-muted-soft">{t('settings.scope.imageLoadFailed')}</p>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    void sitesQuery.refetch()
                    for (const query of modelQueries) if (query.isError) void query.refetch()
                  }}
                >
                  {t('settings.scope.imageRetry')}
                </Button>
              </div>
            ) : groups.length === 0 ? (
              <p className="py-8 text-center text-xs text-muted-soft">{t('settings.scope.imageEmpty')}</p>
            ) : (
              <>
              <section className="space-y-2">
              <div className="flex h-80 shrink-0 flex-col overflow-hidden rounded-xl border border-[hsl(var(--glass-border))] md:flex-row">
                <div className="flex max-h-40 shrink-0 flex-col overflow-y-auto border-b border-[hsl(var(--glass-divider))] md:max-h-none md:w-56 md:border-b-0 md:border-r">
                  <p className="px-4 py-3 text-xs text-muted-soft">{t('settings.scope.imageSites')}</p>
                  {groups.map(({ site, models }) => (
                    <button
                      key={site.id}
                      type="button"
                      onClick={() => setActiveSiteId(site.id)}
                      className={cn(
                        'flex items-center justify-between gap-2 px-4 py-2.5 text-left text-sm transition-colors hover:bg-[hsl(var(--surface-subtle))]',
                        activeGroup?.site.id === site.id && 'nav-item-selected font-medium text-foreground',
                      )}
                    >
                      <span className="min-w-0 truncate" title={site.name}>{site.name}</span>
                      <span className="shrink-0 text-xs text-muted-soft tabular-nums">
                        {`${models.filter(({ key }) => isImageModelSiteSelected(selected, key, site.id)).length}/${models.length}`}
                      </span>
                    </button>
                  ))}
                </div>
                <div className="flex min-h-0 min-w-0 flex-1 flex-col">
                  <p className="border-b border-[hsl(var(--glass-divider))] px-4 py-3 text-xs text-muted-soft">{t('settings.scope.imageModels')}</p>
                  <div className="min-h-0 flex-1 overflow-y-auto">
                    {activeGroup?.models.map(({ model, key }) => (
                      <label key={model.id} className="flex cursor-pointer items-center gap-3 px-4 py-2.5 hover:bg-[hsl(var(--surface-subtle))]">
                        <input
                          type="checkbox"
                          checked={isImageModelSiteSelected(selected, key, activeGroup.site.id)}
                          onChange={() => setDraft(toggleImageModelSite(selected, key, activeGroup.site.id, providers.get(key) ?? []))}
                          className="h-4 w-4 shrink-0 accent-[hsl(var(--primary))]"
                        />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm text-foreground">{model.display_name || model.upstream_model_name}</span>
                          <span className="block truncate text-xs text-muted-soft">{key}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                </div>
              </div>
              </section>
              <section className="space-y-2">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <h3 className="text-sm font-semibold text-foreground">{t('settings.scope.imageOrder')}</h3>
                    <p className="mt-0.5 text-xs leading-5 text-muted-soft">{t('settings.scope.imageOrderHint')}</p>
                  </div>
                  {selected.length ? (
                    <Button type="button" size="sm" variant="ghost" className="shrink-0" onClick={() => setDraft([])}>{t('settings.scope.imageClear')}</Button>
                  ) : null}
                </div>
                <div className="rounded-xl border border-[hsl(var(--glass-border))] p-2">
                  {selected.length === 0 ? (
                    <p className="px-2 py-3 text-center text-xs text-muted-soft">{t('settings.scope.imageNotConfigured')}</p>
                  ) : (
                    <ol className="space-y-1">
                      {selected.map((entry, index) => {
                        const key = entry.model
                        const siteIds = entry.site_ids.length === 0 ? providers.get(key) ?? [] : entry.site_ids
                        return (
                          <li key={key} className="space-y-1.5 rounded-lg bg-[hsl(var(--surface-subtle))] px-2.5 py-1.5">
                            <div className="flex items-center gap-2">
                              <span className="w-5 shrink-0 text-center text-xs text-muted-soft tabular-nums">{index + 1}</span>
                              <span className="min-w-0 flex-1 truncate text-sm text-foreground" title={key}>{names.get(key) ?? key}</span>
                              <button type="button" aria-label={t('settings.scope.imageMoveUp')} disabled={index === 0} onClick={() => setDraft(moveImageModel(selected, key, -1))} className="rounded p-1 text-muted-soft hover:text-foreground disabled:opacity-30"><ArrowUp className="h-3.5 w-3.5" /></button>
                              <button type="button" aria-label={t('settings.scope.imageMoveDown')} disabled={index === selected.length - 1} onClick={() => setDraft(moveImageModel(selected, key, 1))} className="rounded p-1 text-muted-soft hover:text-foreground disabled:opacity-30"><ArrowDown className="h-3.5 w-3.5" /></button>
                              <button type="button" aria-label={t('settings.scope.imageRemove')} onClick={() => setDraft(removeImageModel(selected, key))} className="rounded p-1 text-muted-soft hover:text-foreground"><X className="h-3.5 w-3.5" /></button>
                            </div>
                            <ul className="flex flex-wrap gap-1 pl-7">
                              {siteIds.map((siteId) => (
                                <li key={siteId} className="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-muted-soft ring-1 ring-[hsl(var(--glass-border))]">
                                  <span className="max-w-[10rem] truncate">{siteNames.get(siteId) ?? siteId}</span>
                                  <button type="button" aria-label={t('settings.scope.imageRemoveSite')} onClick={() => setDraft(toggleImageModelSite(selected, key, siteId, providers.get(key) ?? []))} className="hover:text-foreground"><X className="h-3 w-3" /></button>
                                </li>
                              ))}
                            </ul>
                          </li>
                        )
                      })}
                    </ol>
                  )}
                </div>
              </section>
              </>
            )}
          </DialogBody>
          <div className="shrink-0">
            {dirty ? <p className="px-6 pt-3 text-xs text-amber-500">{t('settings.scope.unsavedChanges')}</p> : null}
            <DialogFooter
              className={dirty ? 'border-t-0' : undefined}
              cancel={<Button variant="outline" onClick={requestClose} disabled={save.isPending}>{t('settings.cancel')}</Button>}
              confirm={<Button onClick={() => save.mutate()} disabled={!dirty || runtime.data === undefined || save.isPending}>{t('settings.save')}</Button>}
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
