import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  bindJSPluginSite,
  previewJSPluginPricing,
  unbindJSPluginSite,
} from '@/features/settings/api/js-plugins'
import { sitesQueryKeys, type Site } from '@/features/sites/api/sites'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

export type SiteBindingTarget = {
  pluginId: string
  version: string
  exports: string[]
}

function mounted(site: Site, pluginId: string) {
  return site.gateway_config?.plugin_ids?.includes(pluginId) ?? false
}

// State and actions for mounting one plugin on sites. The hook lives in the
// dialog (not in the section below) because the Apply button is in the dialog
// footer, away from the site picker.
//
// Binding is optional: an enabled plugin is loaded either way, and its hooks
// only run on the sites picked here. A site can mount several plugins; each
// hook uses the first mounted plugin that exports it.
export function useSiteBinding(target: SiteBindingTarget | null, sites: Site[]) {
  const { t } = useTranslation(['settings'])
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<{ pluginId: string; ids: string[] } | null>(null)
  const [previews, setPreviews] = useState<Record<string, { items: number; groups: number }>>({})
  const [previewing, setPreviewing] = useState<string | null>(null)
  const [reviewed, setReviewed] = useState(false)

  const pluginId = target?.pluginId ?? ''
  const exports = target?.exports ?? []
  const bound = target ? sites.filter((site) => mounted(site, pluginId)).map((site) => site.id) : []
  // An edit belongs to the plugin it was made for, so it never leaks into another plugin's dialog.
  const selected = draft && draft.pluginId === pluginId ? draft.ids : bound
  const toBind = selected.filter((id) => !bound.includes(id))
  const toUnbind = bound.filter((id) => !selected.includes(id))
  const changed = toBind.length > 0 || toUnbind.length > 0
  const pricing = exports.includes('parsePricing')
  const pricingReady = !pricing || toBind.length === 0 || (reviewed && toBind.every((id) => previews[id]))
  const siteName = (id: string) => sites.find((site) => site.id === id)?.name ?? id

  const reset = () => {
    setDraft(null)
    setPreviews({})
    setPreviewing(null)
    setReviewed(false)
  }

  const setSelected = (ids: string[]) => {
    setDraft({ pluginId, ids })
    // Anything dropped from the pick no longer needs a review.
    if (ids.length < selected.length) setReviewed(false)
  }

  const applyMutation = useMutation({
    mutationFn: async () => {
      if (!target) return
      const failures: string[] = []
      const run = async (id: string, action: () => Promise<unknown>) => {
        try {
          await action()
        } catch (error) {
          failures.push(`${siteName(id)}: ${error instanceof APIError ? error.message : String(error)}`)
        }
      }
      for (const id of toUnbind) await run(id, () => unbindJSPluginSite(target.pluginId, id))
      for (const id of toBind) {
        await run(id, () =>
          bindJSPluginSite(target.pluginId, target.version, id, { confirmPricingReviewed: reviewed }),
        )
      }
      if (failures.length > 0) throw new Error(failures.join('\n'))
    },
    onSuccess: () => toast.success(t('settings:jsPlugins.siteBinding.applied')),
    onError: (error: unknown) =>
      toast.error(error instanceof Error ? error.message : t('settings:jsPlugins.siteBinding.failed')),
    onSettled: () => {
      reset()
      void queryClient.invalidateQueries({ queryKey: sitesQueryKeys.all })
    },
  })

  const preview = async (siteId: string) => {
    if (!target) return
    setPreviewing(siteId)
    try {
      const result = await previewJSPluginPricing(target.pluginId, siteId)
      setPreviews((current) => ({
        ...current,
        [siteId]: { items: result.items?.length ?? 0, groups: result.groups?.length ?? 0 },
      }))
    } catch (error) {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.previewPricingFailed'))
    } finally {
      setPreviewing(null)
    }
  }

  return {
    /** False when this plugin has nothing to bind (not site scoped, or no enabled version). */
    applicable: target != null,
    pluginId,
    exports,
    selected,
    setSelected,
    toBind,
    pricing,
    previews,
    previewing,
    preview,
    reviewed,
    setReviewed,
    canApply: target != null && changed && pricingReady && !applyMutation.isPending,
    applying: applyMutation.isPending,
    /** Resolves true when every change went through, so the caller can close the dialog. */
    apply: async () => {
      try {
        await applyMutation.mutateAsync()
        return true
      } catch {
        return false
      }
    },
    reset,
  }
}

export type SiteBinding = ReturnType<typeof useSiteBinding>
