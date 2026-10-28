import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  fetchPluginMarketSettings,
  pluginMarketQueryKeys,
  updatePluginMarketSettings,
  type PluginMarketSettings,
} from '@/features/settings/api/plugin-market'
import { fetchSystemProxyConfig, type ProxyConfig } from '@/features/settings/api/settings'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

const DIRECT = 'direct'

export function PluginMarketSettingsDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({
    queryKey: pluginMarketQueryKeys.settings(),
    queryFn: ({ signal }) => fetchPluginMarketSettings(signal),
  })
  const proxyQuery = useQuery({
    queryKey: ['settings', 'system-proxy'],
    queryFn: fetchSystemProxyConfig,
    enabled: open,
  })
  const [canSave, setCanSave] = useState(true)
  const save = useMutation({
    mutationFn: updatePluginMarketSettings,
    onSuccess: async () => {
      toast.success(t('settings:jsPlugins.market.saved'))
      await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.all })
      onOpenChange(false)
    },
    onError: (error) => {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.market.saveFailed'))
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) setCanSave(true)
        onOpenChange(next)
      }}
    >
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{t('settings:jsPlugins.market.settingsTitle')}</DialogTitle>
          <DialogDescription>{t('settings:jsPlugins.market.settingsHint')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          {settingsQuery.isError ? (
            <p className="text-body text-danger">{t('settings:jsPlugins.market.loadFailed')}</p>
          ) : !settingsQuery.data ? (
            <p className="text-body text-label-2">{t('settings:jsPlugins.loading')}</p>
          ) : open ? (
            <MarketSettingsForm
              key={settingsQuery.dataUpdatedAt}
              initial={settingsQuery.data}
              proxies={proxyQuery.data?.proxies ?? []}
              pending={save.isPending}
              onCanSave={setCanSave}
              onSave={(next) => save.mutate(next)}
            />
          ) : null}
        </DialogBody>
        <DialogFooter>
          <Button type="button" variant="secondary" onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button type="submit" form="plugin-market-settings" disabled={!settingsQuery.data || !canSave || save.isPending}>
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function MarketSettingsForm({
  initial,
  proxies,
  pending,
  onCanSave,
  onSave,
}: {
  initial: PluginMarketSettings
  proxies: ProxyConfig[]
  pending: boolean
  onCanSave: (valid: boolean) => void
  onSave: (next: PluginMarketSettings) => void
}) {
  const { t } = useTranslation('settings')
  const [draft, setDraft] = useState(initial)
  const proxyValue = draft.proxy_id || DIRECT
  const knownProxy = !draft.proxy_id || proxies.some((proxy) => proxy.id === draft.proxy_id)
  const update = (next: PluginMarketSettings) => {
    setDraft(next)
    onCanSave(next.index_sync_hours >= 1 && next.index_sync_hours <= 24)
  }
  return (
    <form
      id="plugin-market-settings"
      className="space-y-4"
      onSubmit={(event) => {
        event.preventDefault()
        if (pending || draft.index_sync_hours < 1 || draft.index_sync_hours > 24) return
        onSave({ ...draft, source_url: draft.source_url.trim() })
      }}
    >
      <Switch
        checked={draft.enabled}
        label={t('jsPlugins.market.enabled')}
        onCheckedChange={(checked) => update({ ...draft, enabled: checked })}
      />
      <FormField label={t('jsPlugins.market.sourceUrl')} description={t('jsPlugins.market.sourceUrlHint')}>
        <Input
          value={draft.source_url}
          placeholder={t('jsPlugins.market.sourceUrlHint')}
          onChange={(event) => update({ ...draft, source_url: event.target.value })}
        />
      </FormField>
      <FormField label={t('jsPlugins.market.proxy')}>
        <Select value={proxyValue} onValueChange={(value) => update({ ...draft, proxy_id: value === DIRECT ? '' : value })}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent searchable={false}>
            <SelectItem value={DIRECT}>{t('jsPlugins.market.proxyDirect')}</SelectItem>
            {!knownProxy && draft.proxy_id ? <SelectItem value={draft.proxy_id}>{draft.proxy_id}</SelectItem> : null}
            {proxies.map((proxy) => (
              <SelectItem key={proxy.id} value={proxy.id}>
                {proxy.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FormField>
      <Switch
        checked={draft.auto_update}
        label={t('jsPlugins.market.autoUpdate')}
        onCheckedChange={(checked) => update({ ...draft, auto_update: checked })}
      />
      <FormField label={t('jsPlugins.market.syncHours')} description={t('jsPlugins.market.syncHoursHint')}>
        <Input
          type="number"
          min={1}
          max={24}
          value={draft.index_sync_hours}
          onChange={(event) => {
            const next = Number(event.target.value)
            update({ ...draft, index_sync_hours: Number.isFinite(next) ? Math.trunc(next) : 0 })
          }}
        />
      </FormField>
    </form>
  )
}
