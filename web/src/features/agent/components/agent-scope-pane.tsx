import { useCallback, useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChevronDown, Search } from 'lucide-react'
import { BrandMark } from '@/components/common/brand-mark'
import { buildModelGlyph, siteTypeIconPath } from '@/components/common/brand-utils'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { SiteModelScopePicker, type SiteModelScopePatch, type SiteModelScopePolicy } from '@/features/api-keys/components/site-model-scope-picker'
import { useSiteModelScope } from '@/features/api-keys/lib/use-site-model-scope'
import { fetchAgentAvailableModels, fetchAgentRuntimeSettings, updateAgentScopeSettings } from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { filterAgentScopePreview, groupAgentScopePreview, summarizeAgentScopePreview } from '@/features/agent/lib/agent-scope-preview'
import { listCanonicalModels, listSitesWithOAuth, type CanonicalModelItem } from '@/features/sites/api/sites'
import { formatSiteTypeLabel } from '@/features/sites/lib/site-utils'
import { canonicalModelIconInfo, modelNameIconInfo } from '@/features/sites/lib/model-icon'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

/**
 * 「范围」tab：站点/模型范围的策略与选择（SiteModelScopePicker，与 API Key 页同款）
 * 直接内联渲染在设置弹窗内容区，下方是「当前 Agent 可用范围」的可折叠预览。
 * 草稿与已保存值分离：修改后 dirty，底部「保存」可点，保存 / 取消（关闭弹窗）都会丢弃草稿。
 */
export function AgentScopePane({ onActionsChange }: { onActionsChange: (actions: { save: () => void; canSave: boolean; pending: boolean; dirty?: boolean } | null) => void }) {
  const { t } = useTranslation(['agent', 'components'])
  const queryClient = useQueryClient()
  const runtime = useQuery({ queryKey: [...agentSettingsKey, 'runtime'], queryFn: fetchAgentRuntimeSettings, retry: false })
  const sitesQuery = useQuery({ queryKey: [...agentSettingsKey, 'sites'], queryFn: listSitesWithOAuth, retry: false })
  const canonicalQuery = useQuery({ queryKey: [...agentSettingsKey, 'canonical-models'], queryFn: listCanonicalModels, retry: false })
  const available = useQuery({ queryKey: [...agentSettingsKey, 'available-models'], queryFn: fetchAgentAvailableModels, retry: false })
  const [sitePolicy, setSitePolicy] = useState<SiteModelScopePolicy | null>(null)
  const [modelPolicy, setModelPolicy] = useState<SiteModelScopePolicy | null>(null)
  const [siteIDs, setSiteIDs] = useState<string[] | null>(null)
  const [siteModelIDs, setSiteModelIDs] = useState<string[] | null>(null)
  const [previewExpanded, setPreviewExpanded] = useState(false)
  const [previewSearch, setPreviewSearch] = useState('')
  const [openSiteIds, setOpenSiteIds] = useState<Set<string>>(() => new Set())

  const sites = useMemo(() => sitesQuery.data?.items ?? [], [sitesQuery.data])
  const canonicalModels = useMemo(() => canonicalQuery.data?.items ?? [], [canonicalQuery.data])
  const canonicalById = useMemo(() => new Map(canonicalModels.map((model) => [model.id, model])), [canonicalModels])
  const effectiveSitePolicy = sitePolicy ?? runtime.data?.site_policy ?? 'allow_all'
  const effectiveModelPolicy = effectiveSitePolicy === 'allow_list' ? 'allow_list' : modelPolicy ?? runtime.data?.model_policy ?? 'allow_all'
  const storedSiteModelIDs = useMemo(() => siteModelIDs ?? runtime.data?.allowed_site_model_ids ?? [], [runtime.data?.allowed_site_model_ids, siteModelIDs])
  const selectedSiteIDs = useMemo(() => siteIDs ?? runtime.data?.allowed_site_ids ?? [], [runtime.data?.allowed_site_ids, siteIDs])
  const scope = useSiteModelScope({
    sites,
    sitePolicy: effectiveSitePolicy,
    modelPolicy: effectiveModelPolicy,
    siteIds: selectedSiteIDs,
    siteModelIds: storedSiteModelIDs,
  })
  // 自定义站点 + 模型访问「全部模型」= 所选站点下的全部模型:后端只认显式的模型 id 列表,
  // 空列表会把所有模型都挡掉(403 model_not_allowed),所以保存与预览都要展开成该站点集合的全部模型 id。
  const expandAllSiteModels = effectiveSitePolicy === 'allow_list' && (modelPolicy ?? runtime.data?.model_policy ?? 'allow_all') === 'allow_all'
  const effectiveSiteModelIDs = expandAllSiteModels ? scope.allSiteModelIds : storedSiteModelIDs
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
      setSitePolicy(null)
      setModelPolicy(null)
      setSiteIDs(null)
      setSiteModelIDs(null)
      await queryClient.invalidateQueries({ queryKey: agentSettingsKey })
      toast.success(t('agent:settings.scope.saved'))
    },
    onError: (error) => toast.error(t('agent:settings.saveFailed'), { description: error.message }),
  })
  const { mutate: save, isPending: saving } = saveScope
  const saveAction = useCallback(() => save(), [save])
  const canSave = !runtime.isLoading
    && runtime.data !== undefined
    && dirty
    && !(effectiveSitePolicy === 'allow_list' && scope.siteModelsLoading)
  useEffect(() => {
    onActionsChange({ save: saveAction, canSave, pending: saving, dirty })
    return () => onActionsChange(null)
  }, [canSave, dirty, onActionsChange, saveAction, saving])

  function handleScopeChange(patch: SiteModelScopePatch) {
    if (patch.sitePolicy !== undefined) setSitePolicy(patch.sitePolicy)
    if (patch.modelPolicy !== undefined) setModelPolicy(patch.modelPolicy)
    if (patch.siteIds !== undefined) setSiteIDs(patch.siteIds)
    if (patch.siteModelIds !== undefined) setSiteModelIDs(patch.siteModelIds)
  }

  const previewLoading = available.isLoading || runtime.isLoading
  const noSitesConfigured = !sitesQuery.isLoading && sites.length === 0
  const previewGroups = useMemo(() => groupAgentScopePreview({
    sites: available.data ?? [],
    sitePolicy: effectiveSitePolicy,
    siteIds: selectedSiteIDs,
    modelPolicy: effectiveModelPolicy,
    siteModelIds: effectiveSiteModelIDs,
  }), [available.data, effectiveModelPolicy, effectiveSitePolicy, selectedSiteIDs, effectiveSiteModelIDs])
  const previewKeyword = previewSearch.trim()
  const filteredGroups = useMemo(() => filterAgentScopePreview(previewGroups, previewKeyword), [previewGroups, previewKeyword])
  const summary = summarizeAgentScopePreview(previewGroups)
  const siteSummary = effectiveSitePolicy === 'allow_all'
    ? t('agent:settings.scope.allSites')
    : t('agent:settings.scope.siteCount', { count: summary.sites })
  const modelSummary = effectiveModelPolicy === 'allow_all'
    ? t('agent:settings.scope.allModels')
    : t('agent:settings.scope.modelCount', { count: summary.models })

  return (
    <div className="space-y-5">
      <div>
        <h3 className="text-sm font-semibold text-foreground">{t('agent:settings.scope.title')}</h3>
        <p className="mt-1 text-xs leading-5 text-muted-soft">{t('agent:settings.scope.description')}</p>
      </div>

      {noSitesConfigured ? (
        <p className="rounded-xl border border-[hsl(var(--glass-border))] px-4 py-8 text-center text-xs leading-5 text-muted-soft">
          {t('agent:settings.scope.previewEmpty')}
        </p>
      ) : (
        <>
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

          <section className="overflow-hidden rounded-xl border border-[hsl(var(--glass-border))]">
            <button
              type="button"
              onClick={() => setPreviewExpanded((expanded) => !expanded)}
              aria-expanded={previewExpanded}
              className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-[hsl(var(--surface-subtle))]"
            >
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium text-foreground">{t('agent:settings.scope.previewTitle')}</span>
                <span className="mt-0.5 block truncate text-xs text-muted-soft">
                  {t('agent:settings.scope.availableSites')}: {siteSummary} · {t('agent:settings.scope.availableModels')}: {modelSummary}
                </span>
              </span>
              <ChevronDown className={cn('h-4 w-4 shrink-0 text-muted-soft transition-transform', previewExpanded && 'rotate-180')} />
            </button>
            {previewExpanded ? (
              <div className="space-y-3 border-t border-[hsl(var(--glass-divider))] px-4 py-3">
                <div className="relative">
                  <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-foreground/40" />
                  <Input
                    value={previewSearch}
                    onChange={(event) => setPreviewSearch(event.target.value)}
                    placeholder={t('agent:settings.scope.previewSearch')}
                    className="h-9 pl-9"
                  />
                </div>
                <div className="max-h-72 overflow-y-auto overscroll-contain rounded-lg border border-[hsl(var(--glass-border))]">
                  {previewLoading ? (
                    <div className="space-y-2 p-3">
                      <Skeleton className="h-9 w-full" />
                      <Skeleton className="h-9 w-full" />
                      <Skeleton className="h-9 w-2/3" />
                    </div>
                  ) : filteredGroups.length === 0 ? (
                    <p className="py-8 text-center text-xs text-muted-soft">{t('agent:settings.scope.noModels')}</p>
                  ) : (
                    filteredGroups.map((group) => (
                      <ScopePreviewGroup
                        key={group.site.site_id}
                        group={group}
                        canonicalById={canonicalById}
                        open={previewKeyword ? true : openSiteIds.has(group.site.site_id)}
                        onToggle={() => setOpenSiteIds((current) => {
                          const next = new Set(current)
                          if (next.has(group.site.site_id)) next.delete(group.site.site_id)
                          else next.add(group.site.site_id)
                          return next
                        })}
                      />
                    ))
                  )}
                </div>
              </div>
            ) : null}
          </section>
        </>
      )}
    </div>
  )
}

function ScopePreviewGroup({
  group,
  canonicalById,
  open,
  onToggle,
}: {
  group: ReturnType<typeof groupAgentScopePreview>[number]
  canonicalById: Map<string, CanonicalModelItem>
  open: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation(['agent', 'components'])
  const site = group.site
  return (
    <div className="border-t border-[hsl(var(--glass-divider))] first:border-t-0">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-[hsl(var(--surface-subtle))]"
      >
        <BrandMark iconPath={siteTypeIconPath(site.site_type)} label={formatSiteTypeLabel(site.site_type)} fallback={site.site_name} fallbackText={buildModelGlyph(site.site_name)} size="sm" />
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground" title={site.site_name}>{site.site_name}</span>
        <span className="shrink-0 text-xs tabular-nums text-faint">{t('agent:settings.scope.modelCount', { count: group.models.length })}</span>
        <ChevronDown className={cn('h-4 w-4 shrink-0 text-muted-soft transition-transform', open && 'rotate-180')} />
      </button>
      {open ? (
        <div className="pb-1.5">
          {group.models.map((model) => (
            <ScopePreviewModelRow key={model.id} siteName={site.site_name} model={model} canonicalById={canonicalById} />
          ))}
        </div>
      ) : null}
    </div>
  )
}

function ScopePreviewModelRow({
  siteName,
  model,
  canonicalById,
}: {
  siteName: string
  model: ReturnType<typeof groupAgentScopePreview>[number]['models'][number]
  canonicalById: Map<string, CanonicalModelItem>
}) {
  const { t } = useTranslation(['agent', 'components'])
  const canonical = model.canonical_model_id ? canonicalById.get(model.canonical_model_id) : undefined
  const icon = canonical
    ? canonicalModelIconInfo(canonical, model.display_name || model.upstream_model_name)
    : modelNameIconInfo([model.display_name, model.upstream_model_name, model.model_key], model.model_key || model.upstream_model_name)
  return (
    <div className="flex min-w-0 items-center gap-3 px-4 py-1.5 pl-11">
      <BrandMark iconPath={icon.iconPath} label={icon.label} fallback={icon.fallback} fallbackText={icon.fallbackText} size="sm" />
      <button
        type="button"
        className="min-w-0 max-w-full truncate bg-transparent p-0 text-left text-sm text-foreground"
        title={model.upstream_model_name}
        onClick={() => copyToClipboard(model.model_key || model.upstream_model_name, t('components:modelsDraw.copied'), t('components:modelsDraw.copyFailed'))}
      >
        {model.model_key || model.upstream_model_name}
      </button>
      <span className="ml-auto shrink-0 truncate text-xs text-faint" title={siteName}>{siteName}</span>
    </div>
  )
}
