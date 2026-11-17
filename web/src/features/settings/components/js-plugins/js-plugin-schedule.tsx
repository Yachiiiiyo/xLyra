import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import {
  getJSPluginSchedule,
  jsPluginQueryKeys,
  updateJSPluginSchedule,
} from '@/features/settings/api/js-plugins'
import { resolveText } from '@/features/settings/lib/localized-text'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

export function JSPluginScheduleSection({ pluginId }: { pluginId: string }) {
  const { t, i18n } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()

  const scheduleQuery = useQuery({
    queryKey: jsPluginQueryKeys.schedule(pluginId),
    queryFn: ({ signal }) => getJSPluginSchedule(pluginId, signal),
  })

  const toggleMutation = useMutation({
    mutationFn: (enabled: boolean) => updateJSPluginSchedule(pluginId, enabled),
    onSuccess: async (next) => {
      await queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.schedule(pluginId) })
      toast.success(t(next.enabled ? 'settings:jsPlugins.schedule.enabledToast' : 'settings:jsPlugins.schedule.disabledToast'))
    },
    onError: (error: unknown) => {
      const detail = error instanceof APIError ? error.message : t('settings:jsPlugins.schedule.saveFailed')
      toast.error(detail)
    },
  })

  const data = scheduleQuery.data
  if (!data || !data.supported) return null

  const customLabel = resolveText(data.label, i18n.language)
  const minutes = data.every_minutes ?? 60
  const label = customLabel || t('settings:jsPlugins.schedule.switchLabel', { minutes })
  const nextTick = data.next_tick_at ? new Date(data.next_tick_at).toLocaleString(i18n.language) : null

  return (
    <section className="space-y-3 border-t border-separator pt-4">
      <div className="space-y-1">
        <h4 className="text-subheadline font-semibold text-label">{t('settings:jsPlugins.schedule.title')}</h4>
        <p className="text-footnote text-label-2">{t('settings:jsPlugins.schedule.hint')}</p>
      </div>
      <div className="rounded-inner border border-separator p-3 space-y-2">
        <Switch
          checked={data.enabled}
          disabled={toggleMutation.isPending}
          onCheckedChange={(checked) => toggleMutation.mutate(checked)}
          label={label}
          description={t('settings:jsPlugins.schedule.intervalDescription', { minutes })}
        />
        {data.enabled && nextTick ? (
          <p className="text-footnote text-label-3">
            {t('settings:jsPlugins.schedule.nextTick', { time: nextTick })}
          </p>
        ) : null}
      </div>
    </section>
  )
}
