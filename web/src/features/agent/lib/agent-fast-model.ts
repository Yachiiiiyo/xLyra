import type { AgentScopePreviewGroup } from '@/features/agent/lib/agent-scope-preview'

export const NON_CHAT_CATEGORIES = new Set(['image', 'audio', 'embedding'])

export type AgentModelCandidate = {
  id: string
  name: string
  siteNames: string[]
}

/** 当前 Agent 可用范围内可用于后台任务的聊天模型。 */
export function agentModelCandidates(groups: AgentScopePreviewGroup[]): AgentModelCandidate[] {
  const merged = new Map<string, AgentModelCandidate>()
  for (const group of groups) {
    for (const model of group.models) {
      if (NON_CHAT_CATEGORIES.has(model.category ?? '')) continue
      const id = (model.model_key || model.upstream_model_name).trim()
      if (!id) continue
      const existing = merged.get(id)
      if (existing) {
        if (!existing.siteNames.includes(group.site.site_name)) existing.siteNames.push(group.site.site_name)
      } else {
        merged.set(id, { id, name: model.display_name || id, siteNames: [group.site.site_name] })
      }
    }
  }
  return [...merged.values()].sort((left, right) => left.name.localeCompare(right.name, undefined, { sensitivity: 'base' }))
}

/** 空配置与已不在候选中的配置都按「未配置」处理。 */
export function resolveAgentModel(configured: string, candidates: AgentModelCandidate[]): { model: AgentModelCandidate | null } {
  const id = configured.trim()
  return { model: id ? candidates.find((candidate) => candidate.id === id) ?? null : null }
}
