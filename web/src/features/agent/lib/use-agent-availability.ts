import { useQuery } from '@tanstack/react-query'
import { fetchAgentHealth, type AgentHealth } from '@/features/agent/api/agent'

// Shares the query cache with the agent settings page, so nav visibility follows saves/refreshes there.
export const agentSettingsKey = ['settings', 'agent'] as const

/**
 * The built-in agent defaults to available while loading or on network failure.
 * Only an explicit 404 hides the entry; an unhealthy instance stays visible.
 */
export function isAgentAvailable(health: AgentHealth | null | undefined): boolean {
  return health?.status !== 'missing'
}

export function useAgentAvailability(): boolean {
  const query = useQuery({
    queryKey: [...agentSettingsKey, 'health'],
    queryFn: fetchAgentHealth,
    retry: false,
    staleTime: 30_000,
  })
  return isAgentAvailable(query.data)
}
