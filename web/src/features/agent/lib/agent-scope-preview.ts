import type { AgentAvailableSite } from '@/features/agent/api/agent'

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

/**
 * 关键词过滤分组列表：站点名命中时保留该站点的全部模型；
 * 否则只保留模型名 / model_key 命中的模型，模型全部不命中的站点整组丢弃。
 */
export function filterAgentScopePreview(groups: AgentScopePreviewGroup[], keyword: string): AgentScopePreviewGroup[] {
  const key = keyword.trim().toLowerCase()
  if (!key) return groups
  return groups
    .map((group) => {
      if (`${group.site.site_name} ${group.site.site_type}`.toLowerCase().includes(key)) return group
      const models = group.models.filter((model) =>
        `${model.display_name} ${model.upstream_model_name} ${model.model_key ?? ''}`.toLowerCase().includes(key))
      return models.length > 0 ? { site: group.site, models } : null
    })
    .filter((group): group is AgentScopePreviewGroup => group !== null)
}

/** 摘要：站点数与模型总数 */
export function summarizeAgentScopePreview(groups: AgentScopePreviewGroup[]): { sites: number; models: number } {
  return {
    sites: groups.length,
    models: groups.reduce((total, group) => total + group.models.length, 0),
  }
}
