import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { AgentConfirmDialog } from '@/features/agent/components/agent-confirm-dialog'
import { SiteModelScopePicker, type SiteModelScopePatch, type SiteModelScopePolicy } from '@/features/api-keys/components/site-model-scope-picker'
import { useSiteModelScope } from '@/features/api-keys/lib/use-site-model-scope'
import { fetchAgentRuntimeSettings, updateAgentScopeSettings } from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { listCanonicalModels, listSitesWithOAuth } from '@/features/sites/api/sites'
import { useMobileLayout } from '@/hooks/use-media-query'
import { toast } from '@/lib/toast'

/** 可用模型与生图模型两个配置弹窗共用的尺寸与层级,保证高度一致 */
export const AGENT_CONFIG_DIALOG_CLASS = 'z-[60] h-[min(86svh,760px)] w-[min(94vw,860px)]'

export function AgentScopeConfigDialog({ open, onOpenChange }: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('agent')
  const mobileLayout = useMobileLayout()
  const queryClient = useQueryClient()
  const runtime = useQuery({ queryKey: [...agentSettingsKey, 'runtime'], queryFn: fetchAgentRuntimeSettings, retry: false, enabled: open })
  const sitesQuery = useQuery({ queryKey: [...agentSettingsKey, 'sites'], queryFn: listSitesWithOAuth, retry: false, enabled: open })
  const canonicalQuery = useQuery({ queryKey: [...agentSettingsKey, 'canonical-models'], queryFn: listCanonicalModels, retry: false, enabled: open })
  const [sitePolicy, setSitePolicy] = useState<SiteModelScopePolicy | null>(null)
  const [modelPolicy, setModelPolicy] = useState<SiteModelScopePolicy | null>(null)
  const [siteIDs, setSiteIDs] = useState<string[] | null>(null)
  const [siteModelIDs, setSiteModelIDs] = useState<string[] | null>(null)
  const [confirmClose, setConfirmClose] = useState(false)
  const sites = useMemo(() => sitesQuery.data?.items ?? [], [sitesQuery.data])
  const canonicalModels = useMemo(() => canonicalQuery.data?.items ?? [], [canonicalQuery.data])
  const effectiveSitePolicy = sitePolicy ?? runtime.data?.site_policy ?? 'allow_all'
  const effectiveModelPolicy = effectiveSitePolicy === 'allow_list' ? 'allow_list' : modelPolicy ?? runtime.data?.model_policy ?? 'allow_all'
  const storedSiteModelIDs = siteModelIDs ?? runtime.data?.allowed_site_model_ids ?? []
  const selectedSiteIDs = siteIDs ?? runtime.data?.allowed_site_ids ?? []
  const scope = useSiteModelScope({
    sites,
    sitePolicy: effectiveSitePolicy,
    modelPolicy: effectiveModelPolicy,
    siteIds: selectedSiteIDs,
    siteModelIds: storedSiteModelIDs,
  })
  const expandAllSiteModels = effectiveSitePolicy === 'allow_list' && (modelPolicy ?? runtime.data?.model_policy ?? 'allow_all') === 'allow_all'
  const dirty = sitePolicy !== null || modelPolicy !== null || siteIDs !== null || siteModelIDs !== null

  const saveScope = useMutation({
    mutationFn: () => updateAgentScopeSettings({
      site_policy: effectiveSitePolicy,
      model_policy: effectiveModelPolicy,
      allowed_site_ids: effectiveSitePolicy === 'allow_list' ? selectedSiteIDs : [],
      allowed_site_model_ids: effectiveModelPolicy === 'allow_list'
        ? (expandAllSiteModels ? scope.allSiteModelIds : scope.selectedSiteModelIds.filter((id) => scope.availableSiteModelIds.has(id)))
        : [],
    }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: agentSettingsKey })
      toast.success(t('settings.scope.saved'))
      close()
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })
  const canSave = !runtime.isLoading && runtime.data !== undefined && dirty && !(effectiveSitePolicy === 'allow_list' && scope.siteModelsLoading)

  function handleScopeChange(patch: SiteModelScopePatch) {
    if (patch.sitePolicy !== undefined) setSitePolicy(patch.sitePolicy)
    if (patch.modelPolicy !== undefined) setModelPolicy(patch.modelPolicy)
    if (patch.siteIds !== undefined) setSiteIDs(patch.siteIds)
    if (patch.siteModelIds !== undefined) setSiteModelIDs(patch.siteModelIds)
  }

  function close() {
    setSitePolicy(null)
    setModelPolicy(null)
    setSiteIDs(null)
    setSiteModelIDs(null)
    onOpenChange(false)
  }

  function requestClose() {
    if (saveScope.isPending) return
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
            <DialogTitle>{t('settings.scope.configDialogTitle')}</DialogTitle>
            <DialogDescription className="max-lg:sr-only">{t('settings.scope.configDialogHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-6">
            <section className="space-y-3">
              <h3 className="text-sm font-semibold text-foreground">{t('settings.scope.configTitle')}</h3>
              <SiteModelScopePicker
                scope={scope}
                sitesLoading={sitesQuery.isLoading}
                canonicalModels={canonicalModels}
                canonicalModelsLoading={canonicalQuery.isLoading}
                sitePolicy={effectiveSitePolicy}
                modelPolicy={modelPolicy ?? runtime.data?.model_policy ?? 'allow_all'}
                siteIds={selectedSiteIDs}
                siteModelIds={storedSiteModelIDs}
                onChange={handleScopeChange}
              />
            </section>
          </DialogBody>
          <div className="shrink-0">
            {dirty ? <p className="px-6 pt-3 text-xs text-amber-500">{t('settings.scope.unsavedChanges')}</p> : null}
            <DialogFooter
              className={dirty ? 'border-t-0' : undefined}
              cancel={<Button variant="outline" onClick={requestClose} disabled={saveScope.isPending}>{t('settings.cancel')}</Button>}
              confirm={<Button onClick={() => saveScope.mutate()} disabled={!canSave || saveScope.isPending}>{t('settings.save')}</Button>}
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
