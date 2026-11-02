import { useSearchParams } from 'react-router-dom'

export type PluginsTab = 'installed' | 'market'

export function resolvePluginsTab(tab: string | null): PluginsTab {
  return tab === 'market' ? 'market' : 'installed'
}

export function installedEnableParams(params: URLSearchParams, pluginId: string, version: string) {
  const next = new URLSearchParams(params)
  next.delete('tab')
  next.set('enable', `${pluginId}@${version}`)
  return next
}

export function usePluginsTab() {
  const [params, setParams] = useSearchParams()
  const tab = resolvePluginsTab(params.get('tab'))
  const selectTab = (next: string) => {
    const nextParams = new URLSearchParams(params)
    if (next === 'market') nextParams.set('tab', 'market')
    else nextParams.delete('tab')
    setParams(nextParams, { replace: true })
  }
  const selectInstalledEnable = (pluginId: string, version: string) => {
    setParams(installedEnableParams(params, pluginId, version), { replace: true })
  }
  return { tab, selectTab, params, setParams, selectInstalledEnable }
}
