import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Search, Settings2 } from 'lucide-react'
import { BrandMark } from '@/components/common/brand-mark'
import { buildModelGlyph, siteTypeIconPath } from '@/components/common/brand-utils'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { useMobileLayout } from '@/hooks/use-media-query'
import { buildAgentAvailableModels, fetchAgentRuntimeSettings, updateAgentClassifierModel, updateAgentFastModel, updateAgentPromptSuggestion, type AgentRuntimeSettings } from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { agentModelCandidates, resolveAgentModel, type AgentModelCandidate } from '@/features/agent/lib/agent-fast-model'
import { agentImageModelsSummary } from '@/features/agent/lib/agent-image-model'
import { filterAgentScopePreview, groupAgentScopePreview, summarizeAgentScopePreview, type AgentScopePreviewGroup } from '@/features/agent/lib/agent-scope-preview'
import { listAllSiteModels, listCanonicalModels, listSitesLite, sitesQueryKeys, type CanonicalModelItem } from '@/features/sites/api/sites'
import { formatSiteTypeLabel } from '@/features/sites/lib/site-utils'
import { canonicalModelIconInfo, modelNameIconInfo } from '@/features/sites/lib/model-icon'
import { AgentImageModelDialog } from '@/features/agent/components/agent-image-model-dialog'
import { AgentScopeConfigDialog } from '@/features/agent/components/agent-scope-config-dialog'
import { toast } from '@/lib/toast'

export function AgentScopePane() {
  // api-keys 命名空间原本要等配置弹窗里的站点模型选择器首次渲染才加载,加载期间整棵 Suspense 树挂起重挂,
  // 会打乱叠在设置弹窗之上的 Radix 层级(配置弹窗整体不可点);这里提前加载,弹窗打开时不再挂起
  const { t } = useTranslation(['agent', 'components', 'api-keys'])
  const queryClient = useQueryClient()
  const runtimeKey = [...agentSettingsKey, 'runtime'] as const
  const runtime = useQuery({ queryKey: runtimeKey, queryFn: fetchAgentRuntimeSettings, retry: false })
  const sitesQuery = useQuery({ queryKey: [...agentSettingsKey, 'sites'], queryFn: listSitesLite, retry: false })
  const canonicalQuery = useQuery({ queryKey: [...agentSettingsKey, 'canonical-models'], queryFn: listCanonicalModels, retry: false })
  const modelsQuery = useQuery({ queryKey: sitesQueryKeys.allModels(), queryFn: listAllSiteModels, retry: false, staleTime: 0 })
  const available = useMemo(() => buildAgentAvailableModels(sitesQuery.data?.items ?? [], canonicalQuery.data?.items ?? [], modelsQuery.data ?? {}), [sitesQuery.data, canonicalQuery.data, modelsQuery.data])
  const [previewOpen, setPreviewOpen] = useState(false)
  const [scopeConfigOpen, setScopeConfigOpen] = useState(false)
  const [imageConfigOpen, setImageConfigOpen] = useState(false)
  const promptSuggestionMutation = useMutation({
    mutationFn: updateAgentPromptSuggestion,
    onMutate: async (enabled) => {
      await queryClient.cancelQueries({ queryKey: runtimeKey })
      const previous = queryClient.getQueryData<AgentRuntimeSettings>(runtimeKey)
      queryClient.setQueryData<AgentRuntimeSettings>(runtimeKey, (current) => current ? { ...current, prompt_suggestion_enabled: enabled } : current)
      return { previous }
    },
    onError: (error, _enabled, context) => {
      if (context?.previous) queryClient.setQueryData(runtimeKey, context.previous)
      toast.error(t('agent:settings.saveFailed'), { description: error.message })
    },
    onSuccess: async (_data, enabled) => {
      queryClient.setQueryData<AgentRuntimeSettings>(runtimeKey, (current) => current ? { ...current, prompt_suggestion_enabled: enabled } : current)
      await queryClient.invalidateQueries({ queryKey: runtimeKey })
    },
  })
  const canonicalById = useMemo(() => new Map((canonicalQuery.data?.items ?? []).map((model) => [model.id, model])), [canonicalQuery.data])
  const imageSummary = agentImageModelsSummary(runtime.data?.image_models ?? [], available)
  const previewGroups = useMemo(() => groupAgentScopePreview({
    sites: available,
    sitePolicy: runtime.data?.site_policy ?? 'allow_all',
    siteIds: runtime.data?.allowed_site_ids ?? [],
    modelPolicy: runtime.data?.model_policy ?? 'allow_all',
    siteModelIds: runtime.data?.allowed_site_model_ids ?? [],
  }), [available, runtime.data])
  const modelCandidates = useMemo(() => agentModelCandidates(previewGroups), [previewGroups])
  const modelSelectionLoading = runtime.isLoading || sitesQuery.isLoading || canonicalQuery.isLoading || modelsQuery.isLoading
  const scopeQueriesReady = runtime.isSuccess && sitesQuery.isSuccess && canonicalQuery.isSuccess && modelsQuery.isSuccess
    && !runtime.isFetching && !sitesQuery.isFetching && !canonicalQuery.isFetching && !modelsQuery.isFetching
  const summary = summarizeAgentScopePreview(previewGroups)
  const summaryText = runtime.data?.site_policy === 'allow_all' && runtime.data.model_policy === 'allow_all'
    ? `${t('agent:settings.scope.allSites')} · ${t('agent:settings.scope.allModels')}`
    : runtime.data?.site_policy === 'allow_all'
      ? `${t('agent:settings.scope.allSites')} · ${t('agent:settings.scope.modelCount', { count: summary.models })}`
      : runtime.data?.model_policy === 'allow_all'
        ? `${t('agent:settings.scope.siteCount', { count: summary.sites })} · ${t('agent:settings.scope.allModels')}`
        : `${t('agent:settings.scope.siteCount', { count: summary.sites })} · ${t('agent:settings.scope.modelCount', { count: summary.models })}`

  const dialogs = (
    <>
      <ScopePreviewDialog
        open={previewOpen}
        onOpenChange={setPreviewOpen}
        groups={previewGroups}
        loading={sitesQuery.isLoading || canonicalQuery.isLoading || modelsQuery.isLoading || runtime.isLoading}
        canonicalById={canonicalById}
      />
      <AgentScopeConfigDialog open={scopeConfigOpen} onOpenChange={setScopeConfigOpen} />
      <AgentImageModelDialog open={imageConfigOpen} onOpenChange={setImageConfigOpen} />
    </>
  )

  return (
    <div className="divide-y divide-[hsl(var(--glass-divider))]">
      <section className="flex flex-wrap items-center gap-x-4 gap-y-2 pb-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold text-foreground">{t('agent:settings.scope.previewTitle')}</p>
          <button type="button" className="mt-0.5 text-left text-xs text-muted-soft underline-offset-4 hover:text-foreground hover:underline" onClick={() => setPreviewOpen(true)}>
            {summaryText}
          </button>
        </div>
        <Button size="sm" variant="secondary" onClick={() => setScopeConfigOpen(true)}>
          <Settings2 className="h-3.5 w-3.5" />
          {t('agent:settings.scope.configure')}
        </Button>
      </section>

      <section className="flex flex-wrap items-center gap-x-4 gap-y-2 py-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold text-foreground">{t('agent:settings.scope.imageTitle')}</p>
          <p className="mt-0.5 line-clamp-2 break-words text-xs text-muted-soft">
            {imageSummary.length
              ? imageSummary.map((item, index) => (
                  <span key={item.key}>
                    {index > 0 ? ' › ' : ''}
                    <span className={item.available ? undefined : 'text-amber-500'} title={item.available ? undefined : t('agent:settings.scope.imageSiteUnavailable')}>
                      {item.name}
                    </span>
                  </span>
                ))
              : t('agent:settings.scope.imageNotConfigured')}
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={() => setImageConfigOpen(true)}>
          <Settings2 className="h-3.5 w-3.5" />
          {t('agent:settings.scope.configure')}
        </Button>
      </section>

      <ScopeModelSetting
        title={t('agent:settings.scope.classifierModelTitle')}
        description={t('agent:settings.scope.classifierModelDescription')}
        notConfigured={t('agent:settings.scope.classifierModelNotConfigured')}
        unsetHint={t('agent:settings.scope.classifierModelUnsetHint')}
        configuredModel={runtime.data?.classifier_model ?? ''}
        candidates={modelCandidates}
        loading={modelSelectionLoading}
        scopeQueriesReady={scopeQueriesReady}
        updateModel={updateAgentClassifierModel}
      />

      <ScopeModelSetting
        title={t('agent:settings.scope.fastModelTitle')}
        description={t('agent:settings.scope.fastModelConfigured')}
        notConfigured={t('agent:settings.scope.fastModelNotConfigured')}
        unsetHint={t('agent:settings.scope.fastModelUnsetHint')}
        configuredModel={runtime.data?.fast_model ?? ''}
        candidates={modelCandidates}
        loading={modelSelectionLoading}
        scopeQueriesReady={scopeQueriesReady}
        updateModel={updateAgentFastModel}
      />

      <section className="flex items-center gap-4 pt-4">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold text-foreground">{t('agent:settings.scope.promptSuggestionTitle')}</p>
          <p className="mt-0.5 text-xs leading-5 text-muted-soft">{t('agent:settings.scope.promptSuggestionDescription')}</p>
        </div>
        <Switch
          aria-label={t('agent:settings.scope.promptSuggestionTitle')}
          checked={runtime.data?.prompt_suggestion_enabled ?? true}
          disabled={!runtime.data || promptSuggestionMutation.isPending}
          onCheckedChange={(enabled) => promptSuggestionMutation.mutate(enabled)}
        />
      </section>

      {dialogs}
    </div>
  )
}

const CURRENT_CONVERSATION_MODEL = '__current_conversation_model__'

type AgentModelUpdater = (model: string) => Promise<unknown>

function ScopeModelSetting({ title, description, notConfigured, unsetHint, configuredModel, candidates, loading, scopeQueriesReady, updateModel }: {
  title: string
  description: string
  notConfigured: string
  unsetHint: string
  configuredModel: string
  candidates: AgentModelCandidate[]
  loading: boolean
  scopeQueriesReady: boolean
  updateModel: AgentModelUpdater
}) {
  const { t } = useTranslation('agent')
  const queryClient = useQueryClient()
  const model = resolveAgentModel(configuredModel, candidates).model
  useClearUnavailableModel(configuredModel, model, scopeQueriesReady, updateModel)
  const save = useMutation({
    mutationFn: updateModel,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: [...agentSettingsKey, 'runtime'] })
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  return (
    <section className="flex flex-wrap items-center gap-x-4 gap-y-2 py-4">
      <div className="min-w-0 flex-1">
        <p className="text-sm font-semibold text-foreground">{title}</p>
        {loading ? (
          <div className="mt-1 space-y-1">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-3 w-full max-w-md" />
          </div>
        ) : model ? (
          <p className="mt-0.5 text-xs leading-5 text-muted-soft">{description}</p>
        ) : (
          <>
            <p className="mt-0.5 text-xs text-muted-soft">{notConfigured}</p>
            <p className="mt-0.5 text-xs leading-5 text-muted-soft">{unsetHint}</p>
          </>
        )}
      </div>
      {loading ? <Skeleton className="h-9 w-full sm:w-64" /> : (
        <Select
          value={model?.id ?? CURRENT_CONVERSATION_MODEL}
          disabled={save.isPending}
          onValueChange={(value) => save.mutate(value === CURRENT_CONVERSATION_MODEL ? '' : value)}
        >
          <SelectTrigger aria-label={title} className="w-full sm:w-64">
            <SelectValue placeholder={t('settings.scope.modelFollowCurrent')} />
          </SelectTrigger>
          <SelectContent searchable={false} widthMode="content">
            <SelectItem value={CURRENT_CONVERSATION_MODEL}>{t('settings.scope.modelFollowCurrent')}</SelectItem>
            {candidates.map((candidate) => (
              <SelectItem key={candidate.id} value={candidate.id}>{candidate.name}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </section>
  )
}

function useClearUnavailableModel(configuredModel: string, model: AgentModelCandidate | null, scopeQueriesReady: boolean, updateModel: AgentModelUpdater) {
  const queryClient = useQueryClient()
  const clearedModel = useRef('')
  useEffect(() => {
    if (!configuredModel || model) {
      clearedModel.current = ''
      return
    }
    if (!scopeQueriesReady || clearedModel.current === configuredModel) return
    clearedModel.current = configuredModel
    void updateModel('')
      .then(() => queryClient.invalidateQueries({ queryKey: [...agentSettingsKey, 'runtime'] }))
      .catch(() => undefined)
  }, [configuredModel, model, queryClient, scopeQueriesReady, updateModel])
}

/** 「查看」弹出的第二个 Dialog：搜索框 + 按站点分组的模型平铺列表，内容区滚动，模型行点击复制 model_key。 */
export function ScopePreviewDialog({
  open,
  onOpenChange,
  groups,
  loading,
  canonicalById,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  groups: AgentScopePreviewGroup[]
  loading: boolean
  canonicalById: Map<string, CanonicalModelItem>
}) {
  const { t } = useTranslation(['agent', 'components'])
  const mobileLayout = useMobileLayout()
  const [search, setSearch] = useState('')
  const keyword = search.trim()
  const filteredGroups = useMemo(() => filterAgentScopePreview(groups, keyword), [groups, keyword])

  const content = (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="relative shrink-0">
        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-foreground/40" />
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t('agent:settings.scope.previewSearch')}
          className="h-9 pl-9"
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain rounded-lg border border-[hsl(var(--glass-border))]">
        {loading ? (
          <div className="space-y-2 p-3">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-2/3" />
          </div>
        ) : groups.length === 0 ? (
          <p className="py-8 text-center text-xs leading-5 text-muted-soft">{t('agent:settings.scope.previewEmpty')}</p>
        ) : filteredGroups.length === 0 ? (
          <p className="py-8 text-center text-xs text-muted-soft">{t('agent:settings.scope.noModels')}</p>
        ) : (
          filteredGroups.map((group) => (
            <ScopePreviewGroup key={group.site.site_id} group={group} canonicalById={canonicalById} />
          ))
        )}
      </div>
    </div>
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        size="md"
        aria-describedby={undefined}
        overlayClassName="z-[60]"
        onBack={mobileLayout ? () => onOpenChange(false) : undefined}
        backLabel={t('agent:settings.tabScope')}
        className="z-[60] h-[min(80svh,600px)]"
      >
        <DialogHeader>
          <DialogTitle>{t('agent:settings.scope.previewTitle')}</DialogTitle>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-3 px-6">{content}</div>
        {!mobileLayout ? (
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>{t('agent:settings.close')}</Button>
          </DialogFooter>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function ScopePreviewGroup({
  group,
  canonicalById,
}: {
  group: AgentScopePreviewGroup
  canonicalById: Map<string, CanonicalModelItem>
}) {
  const { t } = useTranslation(['agent', 'components'])
  const site = group.site
  return (
    <div className="border-t border-[hsl(var(--glass-divider))] first:border-t-0">
      <div className="flex items-center gap-3 px-4 py-2.5">
        <BrandMark iconPath={siteTypeIconPath(site.site_type)} label={formatSiteTypeLabel(site.site_type)} fallback={site.site_name} fallbackText={buildModelGlyph(site.site_name)} size="sm" />
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground" title={site.site_name}>{site.site_name}</span>
        <span className="shrink-0 text-xs tabular-nums text-faint">{t('agent:settings.scope.modelCount', { count: group.models.length })}</span>
      </div>
      <div className="pb-1.5">
        {group.models.map((model) => (
          <ScopePreviewModelRow key={model.id} siteName={site.site_name} model={model} canonicalById={canonicalById} />
        ))}
      </div>
    </div>
  )
}

function ScopePreviewModelRow({
  siteName,
  model,
  canonicalById,
}: {
  siteName: string
  model: AgentScopePreviewGroup['models'][number]
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
