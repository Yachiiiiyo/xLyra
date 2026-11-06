import type { QueryClient } from '@tanstack/react-query'
import {
  fetchAgentAvailableModels,
  fetchAgentHealth,
  fetchAgentModelMemory,
  fetchAgentRuntimeSettings,
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
