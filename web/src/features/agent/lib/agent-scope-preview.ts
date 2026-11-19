import type { AgentAvailableSite } from '@/features/agent/api/agent'
import type { CanonicalModelItem } from '@/features/sites/api/sites'

export type AgentScopePreviewGroup = {
  site: AgentAvailableSite
  models: AgentAvailableSite['models']
}

export type AgentScopePreviewInput = {
  sites: AgentAvailableSite[]
  sitePolicy: 'allow_all' | 'allow_list'
  siteIds: string[]
  modelPolicy: 'allow_all' | 'allow_list'
  siteModelIds: string[]
}

/**
 * 「当前 Agent 可用范围」预览的分组：按生效策略过滤站点与模型，
 * 只保留仍有可用模型的站点，站点按名称排序。
 */
export function groupAgentScopePreview(input: AgentScopePreviewInput): AgentScopePreviewGroup[] {
  const { sites, sitePolicy, siteIds, modelPolicy, siteModelIds } = input
  const siteIdSet = new Set(siteIds)
  const modelIdSet = new Set(siteModelIds)
  return sites
    .filter((site) => site.enabled && (sitePolicy !== 'allow_list' || siteIdSet.has(site.site_id)))
    .map((site) => ({
      site,
      models: modelPolicy === 'allow_list'
        ? site.models.filter((model) => modelIdSet.has(model.id))
        : site.models,
    }))
    .filter((group) => group.models.length > 0)
    .sort((left, right) => left.site.site_name.localeCompare(right.site.site_name, 'zh-CN'))
}

export type AgentScopePreviewModel = AgentAvailableSite['models'][number]

export type AgentScopePreviewRow = {
  site: AgentAvailableSite
  model: AgentScopePreviewModel
  /** 排序、筛选下拉与点击复制使用的模型名（优先 model_key） */
  modelName: string
  /** 第一行展示名（优先规范模型显示名或站点模型显示名，缺省回退 modelName） */
  displayName: string
  /** 第二行站点上游真实模型名（缺省回退 modelName） */
  realName: string
}

/** 预览列表与筛选下拉展示的模型名，优先 canonical model_key。 */
export function scopePreviewModelName(model: AgentScopePreviewModel): string {
  return model.model_key?.trim() || model.upstream_model_name
}

/** 计算第一行展示名，优先使用区别于 model_key / upstream_model_name 的显示名称。 */
export function scopePreviewDisplayName(
  model: AgentScopePreviewModel,
  canonical?: Pick<CanonicalModelItem, 'display_name' | 'model_key'>,
): string {
  const modelName = scopePreviewModelName(model)
  const canonicalDisplay = canonical?.display_name?.trim() ?? ''
  const canonicalKey = canonical?.model_key?.trim() ?? ''
  const siteDisplay = model.display_name?.trim() ?? ''
  const upstream = model.upstream_model_name?.trim() ?? ''
  if (canonicalDisplay && (canonicalDisplay !== canonicalKey || !siteDisplay || siteDisplay === upstream)) {
    return canonicalDisplay
  }
  return siteDisplay || canonicalDisplay || modelName
}

/**
 * 扁平化预览列表：一级按站点名排序（同名站点按 site_id 归拢），
 * 同站点下二级按模型名排序。
 */
export function flattenAgentScopePreview(
  groups: AgentScopePreviewGroup[],
  canonicalById?: ReadonlyMap<string, Pick<CanonicalModelItem, 'display_name' | 'model_key'>>,
): AgentScopePreviewRow[] {
  return groups
    .flatMap((group) =>
      group.models.map((model) => {
        const modelName = scopePreviewModelName(model)
        const canonical = model.canonical_model_id ? canonicalById?.get(model.canonical_model_id) : undefined
        return {
          site: group.site,
          model,
          modelName,
          displayName: scopePreviewDisplayName(model, canonical),
          realName: model.upstream_model_name.trim() || modelName,
        }
      }),
    )
    .sort((left, right) => {
      const siteOrder = left.site.site_name.localeCompare(right.site.site_name, 'zh-CN')
      if (siteOrder !== 0) return siteOrder
      const siteIdOrder = left.site.site_id.localeCompare(right.site.site_id)
      if (siteIdOrder !== 0) return siteIdOrder
      const modelOrder = left.modelName.localeCompare(right.modelName, 'zh-CN')
      if (modelOrder !== 0) return modelOrder
      const upstreamOrder = left.model.upstream_model_name.localeCompare(right.model.upstream_model_name, 'zh-CN')
      if (upstreamOrder !== 0) return upstreamOrder
      return left.model.id.localeCompare(right.model.id)
    })
}

/** 提取并去重排序用于筛选下拉的模型名。 */
export function listAgentScopePreviewModelNames(rows: AgentScopePreviewRow[]): string[] {
  const names = new Set<string>()
  for (const row of rows) {
    if (row.modelName) names.add(row.modelName)
  }
  return [...names].sort((left, right) => left.localeCompare(right, 'zh-CN'))
}

/** 按关键词与模型筛选器过滤扁平列表（两者同时生效时取交集）。 */
export function filterAgentScopePreviewRows(
  rows: AgentScopePreviewRow[],
  options: { keyword?: string; modelName?: string },
): AgentScopePreviewRow[] {
  const key = (options.keyword ?? '').trim().toLowerCase()
  const selectedModel = (options.modelName ?? '').trim()
  if (!key && !selectedModel) return rows
  return rows.filter((row) => {
    if (selectedModel && row.modelName !== selectedModel) return false
    if (!key) return true
    return `${row.site.site_name} ${row.site.site_type} ${row.displayName} ${row.realName} ${row.model.display_name} ${row.model.upstream_model_name} ${row.model.model_key ?? ''}`
      .toLowerCase()
      .includes(key)
  })
}

/** 摘要：站点数与模型总数 */
export function summarizeAgentScopePreview(groups: AgentScopePreviewGroup[]): { sites: number; models: number } {
  return {
    sites: groups.length,
    models: groups.reduce((total, group) => total + group.models.length, 0),
  }
}
