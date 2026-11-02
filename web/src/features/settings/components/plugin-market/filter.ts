import type { PluginMarketItem } from '@/features/settings/api/plugin-market'

export type MarketListFilter = {
  query: string
  kind: string
  showIncompatible: boolean
}

export function filterMarketPlugins(plugins: PluginMarketItem[], filter: MarketListFilter) {
  const query = filter.query.trim().toLowerCase()
  return plugins.filter((plugin) => {
    if (!filter.showIncompatible && !plugin.has_compatible_version) return false
    if (filter.kind && filter.kind !== 'all' && plugin.kind !== filter.kind) return false
    if (!query) return true
    return [plugin.name, plugin.id, plugin.summary].some((value) => value.toLowerCase().includes(query))
  })
}
