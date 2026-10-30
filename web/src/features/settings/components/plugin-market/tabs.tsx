import type { ReactNode } from 'react'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { PluginsTab } from './tab-state'

export function PluginTabBar({
  tab,
  installedLabel,
  marketLabel,
  onTab,
  actions,
}: {
  tab: PluginsTab
  installedLabel: string
  marketLabel: string
  onTab: (next: string) => void
  actions?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <Tabs value={tab} onValueChange={onTab} variant="segmented-accent">
        <TabsList className="w-fit min-w-64 max-w-full">
          <TabsTrigger value="installed" className="px-4">{installedLabel}</TabsTrigger>
          <TabsTrigger value="market" className="px-4">{marketLabel}</TabsTrigger>
        </TabsList>
      </Tabs>
      {actions}
    </div>
  )
}
