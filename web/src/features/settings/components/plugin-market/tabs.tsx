import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { PluginsTab } from './tab-state'

export function PluginTabBar({
  marketEnabled,
  tab,
  installedLabel,
  marketLabel,
  onTab,
}: {
  marketEnabled: boolean
  tab: PluginsTab
  installedLabel: string
  marketLabel: string
  onTab: (next: string) => void
}) {
  return (
    <Tabs value={tab} onValueChange={onTab} variant="underline">
      <TabsList>
        <TabsTrigger value="installed">{installedLabel}</TabsTrigger>
        {marketEnabled ? <TabsTrigger value="market">{marketLabel}</TabsTrigger> : null}
      </TabsList>
    </Tabs>
  )
}
