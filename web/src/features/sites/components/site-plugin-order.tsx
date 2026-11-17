import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowUp, GripVertical, LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { jsPluginQueryKeys, listUploadedJSPlugins } from '@/features/settings/api/js-plugins'
import { sitesQueryKeys, updateSitePluginOrder, type Site } from '@/features/sites/api/sites'
import { movePlugin } from '@/features/sites/lib/site-plugin-order'
import { toast } from '@/lib/toast'

function sameOrder(left: string[], right: string[]) {
  return left.length === right.length && left.every((id, index) => id === right[index])
}

export function SitePluginOrderSection({ site }: { site: Site }) {
  const { t } = useTranslation('sites')
  const queryClient = useQueryClient()
  const serverIds = site.gateway_config?.plugin_ids ?? []
  const serverKey = serverIds.join('\0')
  const [items, setItems] = useState(serverIds)
  const [draggedId, setDraggedId] = useState<string | null>(null)
  const pluginsQuery = useQuery({
    queryKey: jsPluginQueryKeys.uploaded(),
    queryFn: ({ signal }) => listUploadedJSPlugins(signal),
    enabled: serverIds.length > 1,
  })
  const names = useMemo(() => {
    const map = new Map<string, string>()
    for (const plugin of pluginsQuery.data ?? []) {
      if (plugin.name) map.set(plugin.id, plugin.name)
    }
    return map
  }, [pluginsQuery.data])

  useEffect(() => {
    const next = serverKey ? serverKey.split('\0') : []
    const frame = requestAnimationFrame(() => setItems(next))
    return () => cancelAnimationFrame(frame)
  }, [serverKey])

  const saveMutation = useMutation({
    mutationFn: () => updateSitePluginOrder(site.id, items),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: sitesQueryKeys.all })
      toast.success(t('form.pluginOrder.saved'))
    },
    onError: (error) => toast.error(t('form.pluginOrder.failed'), { description: error.message }),
  })

  if (serverIds.length <= 1) return null

  const pending = saveMutation.isPending
  const changed = !sameOrder(items, serverIds)

  return (
    <div className="space-y-3">
      <div>
        <span className="block text-body font-medium">{t('form.pluginOrder.title')}</span>
        <p className="text-label-2 mt-1 text-footnote">{t('form.pluginOrder.description')}</p>
      </div>
      <ol className="space-y-2" aria-label={t('form.pluginOrder.title')}>
        {items.map((id, index) => {
          const name = names.get(id) ?? id
          return (
            <li
              key={id}
              className="flex items-center gap-2 rounded-inner border border-separator p-2"
              onDragOver={(event) => { if (draggedId && !pending) event.preventDefault() }}
              onDrop={(event) => {
                event.preventDefault()
                if (draggedId && !pending) setItems((current) => movePlugin(current, draggedId, id))
                setDraggedId(null)
              }}
            >
              <Button
                type="button"
                variant="ghost"
                size="icon"
                draggable={!pending}
                disabled={pending}
                aria-label={t('form.pluginOrder.drag', { name })}
                onDragStart={(event) => {
                  event.dataTransfer.setData('text/plain', id)
                  event.dataTransfer.effectAllowed = 'move'
                  const row = event.currentTarget.closest('li')
                  if (row) {
                    const bounds = row.getBoundingClientRect()
                    event.dataTransfer.setDragImage(row, event.clientX - bounds.left, event.clientY - bounds.top)
                  }
                  setDraggedId(id)
                }}
                onDragEnd={() => setDraggedId(null)}
                onKeyDown={(event) => {
                  const target = event.key === 'ArrowUp' ? items[index - 1] : event.key === 'ArrowDown' ? items[index + 1] : undefined
                  if (target) {
                    event.preventDefault()
                    setItems((current) => movePlugin(current, id, target))
                  }
                }}
              >
                <GripVertical className="h-4 w-4" />
              </Button>
              <div className="min-w-0 flex-1">
                <div className="truncate text-body font-medium">{name}</div>
                <div className="truncate text-footnote text-label-3">{id}</div>
              </div>
              <Button type="button" variant="ghost" size="icon" disabled={pending || index === 0} aria-label={t('form.pluginOrder.up', { name })} onClick={() => setItems((current) => movePlugin(current, id, items[index - 1]))}>
                <ArrowUp className="h-4 w-4" />
              </Button>
              <Button type="button" variant="ghost" size="icon" disabled={pending || index === items.length - 1} aria-label={t('form.pluginOrder.down', { name })} onClick={() => setItems((current) => movePlugin(current, id, items[index + 1]))}>
                <ArrowDown className="h-4 w-4" />
              </Button>
            </li>
          )
        })}
      </ol>
      <Button type="button" disabled={pending || !changed} onClick={() => saveMutation.mutate()}>
        {pending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : null}
        {t('form.pluginOrder.save')}
      </Button>
    </div>
  )
}
