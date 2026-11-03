import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { fetchAgentHealth } from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'

export function AgentEnvironmentPane() {
  const { t } = useTranslation('agent')
  const health = useQuery({
    queryKey: [...agentSettingsKey, 'health'],
    queryFn: fetchAgentHealth,
    retry: false,
    staleTime: 30_000,
    refetchInterval: 30_000,
  })
  const data = health.data
  const isolationLabel = data?.isolation === 'on'
    ? t('settings.environment.enabled')
    : data?.isolation === 'degraded'
      ? t('settings.environment.isolationDegraded')
      : data?.isolation === 'off'
        ? t('settings.environment.isolationOff')
        : data?.isolation === 'failed'
          ? t('settings.environment.isolationFailed')
          : t('settings.environment.unknown')
  const sandboxLabel = data?.sandbox === 'on'
    ? t('settings.environment.enabled')
    : data?.sandbox === 'off'
      ? t('settings.environment.sandboxOff')
      : data?.sandbox === 'unavailable' || data?.sandbox === 'failed'
        ? t('settings.environment.sandboxUnavailable')
        : t('settings.environment.unknown')

  return (
    <div className="space-y-4">
      <p className="text-xs leading-5 text-muted-soft">{t('settings.environment.hint')}</p>
      <div className="overflow-hidden rounded-xl border border-[hsl(var(--glass-border))]">
        <EnvironmentRow
          label={t('settings.environment.status')}
          value={data?.status === 'healthy' ? t('settings.environment.healthy') : data?.status === 'unhealthy' ? t('settings.environment.unhealthy') : t('settings.environment.unknown')}
          tone={data?.status === 'healthy' ? 'success' : data?.status === 'unhealthy' ? 'danger' : 'neutral'}
          reason={data?.reason}
        />
        <EnvironmentRow
          label={t('settings.environment.isolation')}
          value={isolationLabel}
          tone={data?.isolation === 'on' || data?.isolation === 'off' ? 'success' : data?.isolation === 'degraded' ? 'warning' : data?.isolation === 'failed' ? 'danger' : 'neutral'}
          reason={data?.isolation_reason}
        />
        <EnvironmentRow
          label={t('settings.environment.sandbox')}
          value={sandboxLabel}
          tone={data?.sandbox === 'on' || data?.sandbox === 'off' ? 'success' : data?.sandbox === 'unavailable' ? 'warning' : data?.sandbox === 'failed' ? 'danger' : 'neutral'}
          reason={data?.sandbox_reason}
        />
        <EnvironmentRow
          label={t('settings.environment.writeTools')}
          value={data?.write_tools_enabled == null ? t('settings.environment.unknown') : data.write_tools_enabled ? t('settings.environment.enabled') : t('settings.environment.readOnly')}
          tone={data?.write_tools_enabled == null ? 'neutral' : data.write_tools_enabled ? 'success' : 'warning'}
        />
        <EnvironmentRow
          label={t('settings.environment.bash')}
          value={data?.bash_enabled == null ? t('settings.environment.unknown') : data.bash_enabled ? t('settings.environment.enabled') : t('settings.environment.disabled')}
          tone={data?.bash_enabled == null ? 'neutral' : data.bash_enabled ? 'success' : 'warning'}
        />
      </div>
    </div>
  )
}

function EnvironmentRow({ label, value, tone, reason }: { label: string; value: string; tone: 'success' | 'warning' | 'danger' | 'neutral'; reason?: string }) {
  return (
    <div className="border-t border-[hsl(var(--glass-divider))] px-4 py-3 first:border-t-0">
      <div className="flex items-start justify-between gap-4">
        <span className="shrink-0 text-xs text-muted-soft">{label}</span>
        <Badge variant={tone} dot className="h-auto max-w-[75%] whitespace-normal text-right leading-5">{value}</Badge>
      </div>
      {reason ? <p className="mt-2 whitespace-pre-wrap break-words text-xs leading-5 text-muted-soft">{reason}</p> : null}
    </div>
  )
}
