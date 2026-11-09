import { useEffect, useRef } from 'react'
import { useAgentAvailability } from '@/features/agent/lib/use-agent-availability'
import i18n from '@/locales/i18n'

export function useAgentPrefetch() {
  const agentAvailable = useAgentAvailability()
  const started = useRef(false)

  useEffect(() => {
    const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
    if (!agentAvailable || started.current || connection?.saveData) return

    const prefetch = () => {
      started.current = true
      void import('@/routes/agent').catch(() => undefined)
      void i18n.loadNamespaces(['agent', 'chat', 'api-keys', 'components']).catch(() => undefined)
    }

    if (typeof window.requestIdleCallback === 'function') {
      const idleId = window.requestIdleCallback(prefetch)
      return () => window.cancelIdleCallback(idleId)
    }

    const timeoutId = window.setTimeout(prefetch, 2000)
    return () => window.clearTimeout(timeoutId)
  }, [agentAvailable])
}
