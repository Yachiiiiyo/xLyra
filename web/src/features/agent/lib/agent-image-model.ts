import type { CanonicalModel, Site, SiteModel } from '@/features/sites/api/sites'
import type { AgentAvailableSite, AgentImageModelSelection } from '@/features/agent/api/agent'

export function isAgentImageModel(model: SiteModel, canonical?: CanonicalModel): boolean {
  const endpointTypes = imageEndpointTypes(model.capabilities) ?? imageEndpointTypes(canonical?.capabilities)
  if (endpointTypes?.some((type) => ['openai-image', 'images'].includes(type.trim().toLowerCase()))) return true
  return hasImageOutput(model.capabilities) || hasImageOutput(canonical?.capabilities)
}

export type AgentImageModelGroup = {
  site: Site
  models: SiteModel[]
}

/** 系统内所有已启用站点的生图模型，按站点分组；与 Agent 的可用站点/模型白名单无关。 */
export function agentImageModelGroups(
  sites: Site[],
  modelsBySite: Map<string, SiteModel[]>,
  canonicalById: Map<string, CanonicalModel>,
): AgentImageModelGroup[] {
  return sites.flatMap((site) => {
    if (!site.enabled) return []
    const models = (modelsBySite.get(site.id) ?? []).filter((model) =>
      model.status === 'active'
      && isAgentImageModel(model, model.canonical_model_id ? canonicalById.get(model.canonical_model_id) : undefined))
    return models.length ? [{ site, models }] : []
  })
}

function imageEndpointTypes(capabilities?: Record<string, unknown>): string[] | null {
  if (!capabilities) return null
  const direct = capabilities.supported_endpoint_types
  if (Array.isArray(direct)) return direct.filter((value): value is string => typeof value === 'string')
  const raw = capabilities.raw
  if (raw && typeof raw === 'object' && Array.isArray((raw as Record<string, unknown>).supported_endpoint_types)) {
    return ((raw as Record<string, unknown>).supported_endpoint_types as unknown[]).filter((value): value is string => typeof value === 'string')
  }
  return null
}

function hasImageOutput(capabilities?: Record<string, unknown>): boolean {
  if (!capabilities) return false
  for (const key of ['output_modalities', 'output_modality', 'modalities', 'modality']) {
    const value = capabilities[key]
    if (typeof value === 'string' && value.toLowerCase().includes('image')) return true
    if (Array.isArray(value) && value.some((item) => typeof item === 'string' && item.toLowerCase().includes('image'))) return true
  }
  const output = capabilities.output
  return Boolean(output && typeof output === 'object' && hasImageOutput(output as Record<string, unknown>))
}

/** 生图模型的稳定标识:规范模型 key,没有规范模型时回落到上游模型名;同名模型跨站点视为同一项。 */
export function agentImageModelKey(model: SiteModel, canonicalById: Map<string, CanonicalModel>): string {
  const canonicalKey = model.canonical_model_id ? canonicalById.get(model.canonical_model_id)?.model_key : undefined
  return canonicalKey || model.upstream_model_name
}

export type AgentImageModelGroupView = {
  site: Site
  models: Array<{ model: SiteModel; key: string }>
}

/** 按站点分组并给每个模型算出去重用的 key。 */
export function agentImageModelGroupViews(groups: AgentImageModelGroup[], canonicalById: Map<string, CanonicalModel>): AgentImageModelGroupView[] {
  return groups.map(({ site, models }) => ({ site, models: models.map((model) => ({ model, key: agentImageModelKey(model, canonicalById) })) }))
}

/** 某个站点上的某个模型是否已选:条目存在且(站点在列表里,或该条目不限站点)。 */
export function isImageModelSiteSelected(selected: AgentImageModelSelection[], key: string, siteId: string): boolean {
  const entry = selected.find((item) => item.model === key)
  return Boolean(entry && (entry.site_ids.length === 0 || entry.site_ids.includes(siteId)))
}

/**
 * 切换「站点 + 模型」这一对:站点与模型是两个独立维度,同名模型在其他站点的选择不受影响。
 * 新模型追加到排序末尾;某模型的站点全部取消时整条移除;「不限站点」的旧条目先展开成它实际覆盖的站点再改。
 */
export function toggleImageModelSite(
  selected: AgentImageModelSelection[],
  key: string,
  siteId: string,
  providedSiteIds: string[],
): AgentImageModelSelection[] {
  const entry = selected.find((item) => item.model === key)
  if (!entry) return [...selected, { model: key, site_ids: [siteId] }]
  const sites = entry.site_ids.length === 0 ? providedSiteIds : entry.site_ids
  const next = sites.includes(siteId) ? sites.filter((id) => id !== siteId) : [...sites, siteId]
  if (next.length === 0) return selected.filter((item) => item.model !== key)
  return selected.map((item) => (item.model === key ? { ...item, site_ids: next } : item))
}

/** 在已选模型里按模型整条移除。 */
export function removeImageModel(selected: AgentImageModelSelection[], key: string): AgentImageModelSelection[] {
  return selected.filter((item) => item.model !== key)
}

/** 上移/下移(按模型排序);越界不变。 */
export function moveImageModel(selected: AgentImageModelSelection[], key: string, delta: -1 | 1): AgentImageModelSelection[] {
  const from = selected.findIndex((item) => item.model === key)
  const to = from + delta
  if (from < 0 || to < 0 || to >= selected.length) return selected
  const next = selected.slice()
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved!)
  return next
}

export type AgentImageModelSummaryItem = {
  key: string
  name: string
  /** 选中的站点名;不限站点时为 null */
  siteNames: string[] | null
  /** 当前没有可用的(已启用)站点提供该模型,运行时会被跳过 */
  available: boolean
}

/** 概览摘要:按顺序列出已选模型及其站点,并标出已经没有可用站点提供的项。 */
export function agentImageModelsSummary(selected: AgentImageModelSelection[], sites: AgentAvailableSite[]): AgentImageModelSummaryItem[] {
  return selected.map((entry) => {
    const providers = sites.filter((site) => site.enabled && site.models.some((model) => (model.model_key || model.upstream_model_name) === entry.model))
    const scoped = entry.site_ids.length === 0 ? providers : providers.filter((site) => entry.site_ids.includes(site.site_id))
    const display = scoped[0]?.models.find((model) => (model.model_key || model.upstream_model_name) === entry.model)?.display_name
    return {
      key: entry.model,
      name: display || entry.model,
      siteNames: entry.site_ids.length === 0 ? null : scoped.map((site) => site.site_name),
      available: scoped.length > 0,
    }
  })
}
