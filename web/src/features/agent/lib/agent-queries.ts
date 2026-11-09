import type { AgentSession } from '@/features/agent/api/agent'
import type { QueryClient } from '@tanstack/react-query'
import {
  fetchAgentAvailableModels,
  fetchAgentHealth,
  fetchAgentModelMemory,
  fetchAgentRuntimeSettings,
  fetchAgentSessionStats,
  listAgentSessions,
  type AgentAvailableSite,
} from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { readQuerySnapshot, writeQuerySnapshot } from '@/lib/query-snapshot'
import i18n from '@/locales/i18n'

let lastPrefetchAt: number | undefined

export function agentSessionsQueryOptions() {
  return {
    queryKey: ['agent', 'sessions'],
    queryFn: async () => {
      const sessions = await listAgentSessions()
      writeQuerySnapshot('agent:sessions', sessions)
      return sessions
    },
    retry: false,
    initialData: () => readQuerySnapshot<Awaited<ReturnType<typeof listAgentSessions>>>('agent:sessions'),
    initialDataUpdatedAt: 0,
  }
}

/** session_title 事件:就地更新会话列表缓存里对应会话的标题。
 * 缓存缺失或找不到会话时不动(终态后列本来就会 invalidate 重拉);
 * 标题为空串时忽略。不与终态刷新冲突:终态 invalidate 会重新拉列表,这里
 * 只是把运行中提前到达的新标题即时反映到侧栏。 */
export function applySessionTitleToCache(queryClient: QueryClient, sessionId: string, title: string): void {
  if (!title.trim()) return
  queryClient.setQueryData<AgentSession[]>(['agent', 'sessions'], (sessions) =>
    sessions?.map((session) => (session.session_id === sessionId ? { ...session, title } : session)),
  )
}

export function agentModelMemoryQueryOptions() {
  return {
    queryKey: ['agent', 'model-memory'],
    queryFn: async () => {
      const memory = await fetchAgentModelMemory()
      writeQuerySnapshot('agent:model-memory', memory)
      return memory
    },
    retry: false,
    initialData: () => readQuerySnapshot<Awaited<ReturnType<typeof fetchAgentModelMemory>>>('agent:model-memory'),
    initialDataUpdatedAt: 0,
  }
}

export function agentRuntimeSettingsQueryOptions() {
  return {
    queryKey: [...agentSettingsKey, 'runtime'],
    queryFn: async () => {
      const settings = await fetchAgentRuntimeSettings()
      writeQuerySnapshot('agent:runtime', settings)
      return settings
    },
    retry: false,
    initialData: () => readQuerySnapshot<Awaited<ReturnType<typeof fetchAgentRuntimeSettings>>>('agent:runtime'),
    initialDataUpdatedAt: 0,
  }
}

export function agentWorkspaceAvailableModelsQueryOptions(queryClient: QueryClient) {
  return {
    queryKey: [...agentSettingsKey, 'workspace-available-models'],
    queryFn: async () => {
      const [{ allowed_site_ids: allowedSites, allowed_site_model_ids: allowedModels }, availableSites] = await Promise.all([
        queryClient.fetchQuery(agentRuntimeSettingsQueryOptions()),
        fetchAgentAvailableModels(),
      ])
      const result = { allowedSites, allowedModels, availableSites }
      writeQuerySnapshot('agent:workspace-available-models', result)
      return result
    },
    retry: false,
    refetchOnMount: 'always' as const,
    refetchOnWindowFocus: 'always' as const,
    initialData: () => readQuerySnapshot<{
      allowedSites: string[]
      allowedModels: string[]
      availableSites: AgentAvailableSite[]
    }>('agent:workspace-available-models'),
    initialDataUpdatedAt: 0,
  }
}

export function agentHealthQueryOptions() {
  return {
    queryKey: [...agentSettingsKey, 'health'],
    queryFn: fetchAgentHealth,
    retry: false,
    staleTime: 30_000,
    refetchInterval: 30_000,
  }
}

/** 会话统计：轮次/步数/token/上下文占用；仅在选中会话时请求，运行中不轮询 */
export function agentSessionStatsQueryOptions(sessionId: string | null | undefined) {
  return {
    queryKey: ['agent', 'session-stats', sessionId ?? ''],
    queryFn: () => fetchAgentSessionStats(sessionId ?? ''),
    enabled: Boolean(sessionId),
    staleTime: 10_000,
    retry: false,
  }
}

/** run 终态与压缩完成：立即失效重拉 */
const SESSION_STATS_REFRESH_EVENTS = new Set(['agent_done', 'agent_error', 'agent_cancelled', 'context_compacted'])
/** 运行中每一步模型请求结束后会跟着出现的事件：节流后重拉，让统计随步更新 */
const SESSION_STATS_STEP_EVENTS = new Set(['tool_call', 'tool_result'])
export const SESSION_STATS_STEP_REFRESH_MS = 1200

const pendingStatsRefresh = new Map<string, ReturnType<typeof setTimeout>>()

function invalidateSessionStats(queryClient: QueryClient, sessionId: string): void {
  void queryClient.invalidateQueries({ queryKey: ['agent', 'session-stats', sessionId] })
}

/** 终态与压缩完成立即失效；运行中的每一步（tool_call / tool_result）按固定间隔合并成一次重拉，
 * 不必等一轮对话结束；文本 delta 等其它事件不刷新。 */
export function refreshAgentSessionStats(queryClient: QueryClient, sessionId: string, eventType: string): void {
  if (SESSION_STATS_REFRESH_EVENTS.has(eventType)) {
    const pending = pendingStatsRefresh.get(sessionId)
    if (pending !== undefined) clearTimeout(pending)
    pendingStatsRefresh.delete(sessionId)
    invalidateSessionStats(queryClient, sessionId)
    return
  }
  if (!SESSION_STATS_STEP_EVENTS.has(eventType) || pendingStatsRefresh.has(sessionId)) return
  pendingStatsRefresh.set(sessionId, setTimeout(() => {
    pendingStatsRefresh.delete(sessionId)
    invalidateSessionStats(queryClient, sessionId)
  }, SESSION_STATS_STEP_REFRESH_MS))
}

export function prefetchAgentWorkspace(queryClient: QueryClient): void {
  const now = Date.now()
  if (lastPrefetchAt !== undefined && now - lastPrefetchAt < 2000) return
  lastPrefetchAt = now

  void queryClient.prefetchQuery(agentSessionsQueryOptions()).catch(() => undefined)
  void queryClient.prefetchQuery(agentModelMemoryQueryOptions()).catch(() => undefined)
  void queryClient.prefetchQuery(agentRuntimeSettingsQueryOptions()).catch(() => undefined)
  void queryClient.prefetchQuery(agentWorkspaceAvailableModelsQueryOptions(queryClient)).catch(() => undefined)
  void queryClient.prefetchQuery(agentHealthQueryOptions()).catch(() => undefined)
  void import('@/routes/agent').catch(() => undefined)
  void i18n.loadNamespaces(['agent', 'chat', 'api-keys', 'components']).catch(() => undefined)
}
