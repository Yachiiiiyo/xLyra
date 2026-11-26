import { chartSliceStroke } from '@/components/charts/chart-theme'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip, type TooltipContentProps } from 'recharts'
import { X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { AnalyticsUsage } from '@/features/analytics/api/analytics'
import type { AnalyticsBreakdownDimension } from '@/features/analytics/lib/analytics-utils'
import { dashboardChartColors, dashboardTooltipStyle } from '@/features/dashboard/components/chart-style'
import { formatCompactNumber, formatDashboardCurrency, formatDashboardTokens } from '@/features/dashboard/lib/dashboard-utils'
import { AnalyticsPanel, AnalyticsSlashTabs } from './analytics-panel'

// 玫瑰图参数
const ROSE_OUTER_MIN_RATIO = 0.42 // 最小外径不低于最大外径的此比例，防止极端值消失

/**
 * 用对数刻度把值域 [minVal, maxVal] 映射到外径 [outerMin, outerMax]。
 * 对数压缩在极端分布（如 58% vs 0.4%）时比 sqrt 更均匀。
 */
function roseOuterRadius(value: number, minVal: number, maxVal: number, outerMax: number): number {
  if (maxVal <= 0) return outerMax
  const outerMin = outerMax * ROSE_OUTER_MIN_RATIO
  if (minVal >= maxVal) return outerMax
  // log 压缩：log(1 + v/min) / log(1 + max/min)
  const scale =
    Math.log(1 + value / minVal) / Math.log(1 + maxVal / minVal)
  return Math.round(outerMin + (outerMax - outerMin) * scale)
}

type DonutMode = 'cost' | 'tokens' | 'requests'

type DonutSlice = {
  key: string
  id?: string | null
  name: string
  value: number
  cost: number
  tokens: number
  requests: number
  color: string
  outerRadius: number
  unpriced: boolean
}

type AnalyticsCostDonutProps = {
  className?: string
  usage: AnalyticsUsage
  onDrillDown: (dimension: AnalyticsBreakdownDimension, item: { key: string; id?: string | null }) => void
  onClearDrillDown: (dimension: AnalyticsBreakdownDimension) => void
  activeSiteIds: string[]
  activeModelKeys: string[]
  activeApiKeyIds: string[]
}

export function AnalyticsCostDonut({
  className,
  usage,
  onDrillDown,
  onClearDrillDown,
  activeSiteIds,
  activeModelKeys,
  activeApiKeyIds,
}: AnalyticsCostDonutProps) {
  const { t } = useTranslation('analytics')
  const [dimension, setDimension] = useState<AnalyticsBreakdownDimension>('model')
  const bodyRef = useRef<HTMLDivElement>(null)
  const [bodyWidth, setBodyWidth] = useState(0)
  const currency = usage.meta.currency || 'USD'
  const roseSize = bodyWidth > 0 ? Math.min(240, Math.max(160, Math.round(bodyWidth * 0.3))) : 160
  const roseInnerRadius = Math.round(roseSize * 0.2)
  const roseOuterMax = Math.round(roseSize * 0.45)
  const roseCenterLabelSize = Math.min(12, Math.max(10, Math.round(roseSize * 0.05)))
  const roseCenterValueSize = Math.min(14, Math.max(12, Math.round(roseSize * 0.06)))

  useEffect(() => {
    const element = bodyRef.current
    if (!element) return
    const updateWidth = () => setBodyWidth(element.getBoundingClientRect().width)
    updateWidth()
    const observer = new ResizeObserver(updateWidth)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  const { mode, slices, unpricedRows } = useMemo<{
    mode: DonutMode
    slices: DonutSlice[]
    unpricedRows: DonutSlice[]
  }>(() => {
    const activeItems = [...usage.breakdowns[dimension]]
      .filter((item) => item.cost > 0 || item.total_tokens > 0 || item.requests > 0)
      .sort((a, b) => (
        b.cost - a.cost
        || b.total_tokens - a.total_tokens
        || b.requests - a.requests
        || a.label.localeCompare(b.label)
      ))
    if (activeItems.length === 0) {
      return { mode: 'cost', slices: [], unpricedRows: [] }
    }

    const priced = activeItems.filter((item) => item.cost > 0)
    const unpriced = activeItems.filter((item) => item.cost <= 0)

    if (priced.length > 0) {
      const maxVal = priced[0].cost
      const minVal = priced[priced.length - 1].cost
      return {
        mode: 'cost',
        slices: priced.map((item, index) => ({
          key: item.key,
          id: item.id,
          name: item.label,
          value: item.cost,
          cost: item.cost,
          tokens: item.total_tokens,
          requests: item.requests,
          color: dashboardChartColors[index % dashboardChartColors.length],
          outerRadius: roseOuterRadius(item.cost, minVal, maxVal, roseOuterMax),
          unpriced: false,
        })),
        unpricedRows: unpriced.map((item) => ({
          key: item.key,
          id: item.id,
          name: item.label,
          value: 0,
          cost: 0,
          tokens: item.total_tokens,
          requests: item.requests,
          color: '',
          outerRadius: 0,
          unpriced: true,
        })),
      }
    }

    const useTokens = activeItems.some((item) => item.total_tokens > 0)
    const fallbackMode: DonutMode = useTokens ? 'tokens' : 'requests'
    const fallbackItems = activeItems
      .filter((item) => (useTokens ? item.total_tokens > 0 : item.requests > 0))
      .sort((a, b) => (
        useTokens
          ? b.total_tokens - a.total_tokens || b.requests - a.requests || a.label.localeCompare(b.label)
          : b.requests - a.requests || b.total_tokens - a.total_tokens || a.label.localeCompare(b.label)
      ))
    const fallbackValue = (item: (typeof fallbackItems)[number]) => (useTokens ? item.total_tokens : item.requests)
    const maxVal = fallbackValue(fallbackItems[0])
    const minVal = fallbackValue(fallbackItems[fallbackItems.length - 1])
    return {
      mode: fallbackMode,
      slices: fallbackItems.map((item, index) => {
        const value = fallbackValue(item)
        return {
          key: item.key,
          id: item.id,
          name: item.label,
          value,
          cost: item.cost,
          tokens: item.total_tokens,
          requests: item.requests,
          color: dashboardChartColors[index % dashboardChartColors.length],
          outerRadius: roseOuterRadius(value, minVal, maxVal, roseOuterMax),
          unpriced: true,
        }
      }),
      unpricedRows: [],
    }
  }, [dimension, roseOuterMax, usage.breakdowns])

  const total = slices.reduce((sum, slice) => sum + slice.value, 0)
  const listItems = useMemo(() => [...slices, ...unpricedRows], [slices, unpricedRows])

  const activeKeys = dimension === 'site'
    ? activeSiteIds
    : dimension === 'model'
      ? activeModelKeys
      : activeApiKeyIds
  const hasActive = activeKeys.length > 0

  const activeLabels = useMemo(() => {
    if (!hasActive) return []
    return activeKeys.map((key) => {
      const found = listItems.find((s) => s.id === key || s.key === key)
      return found?.name ?? key
    })
  }, [activeKeys, hasActive, listItems])

  const panelDescription = hasActive
    ? undefined
    : mode === 'tokens'
      ? t('donut.fallbackTokensDesc')
      : mode === 'requests'
        ? t('donut.fallbackRequestsDesc')
        : unpricedRows.length > 0
          ? t('donut.unpricedHint', { count: unpricedRows.length })
          : t('donut.description')

  const centerLabel = mode === 'cost'
    ? t('donut.total')
    : mode === 'tokens'
      ? t('donut.totalTokens')
      : t('donut.totalRequests')

  const centerValue = mode === 'cost'
    ? formatDashboardCurrency(total, currency)
    : mode === 'tokens'
      ? formatDashboardTokens(total)
      : formatCompactNumber(total)

  return (
    <AnalyticsPanel
      className={className}
      title={t('donut.title')}
      description={panelDescription}
      bodyClassName="flex min-h-0 flex-col"
      action={(
        <div className="flex flex-wrap items-center gap-3">
          {hasActive && (
            <span className="flex flex-wrap items-center gap-1 text-footnote text-label-2">
              {activeLabels.slice(0, 2).map((label) => (
                <span
                  key={label}
                  className="inline-flex max-w-[10rem] items-center gap-1 truncate rounded-full bg-fill-3 px-2 py-0.5 text-label"
                  title={label}
                >
                  {label}
                </span>
              ))}
              {activeLabels.length > 2 && (
                <span className="rounded-full bg-fill-3 px-2 py-0.5 text-label">
                  +{activeLabels.length - 2}
                </span>
              )}
              <button
                type="button"
                onClick={() => onClearDrillDown(dimension)}
                className="ml-1 inline-flex shrink-0 items-center gap-0.5 rounded-full bg-fill-3 px-1.5 py-0.5 text-footnote text-label-2 hover:text-label"
                title={t('donut.clearFilter')}
              >
                <X className="h-3 w-3" />
                {t('donut.clear')}
              </button>
            </span>
          )}
          <AnalyticsSlashTabs
            value={dimension}
            onValueChange={setDimension}
            items={[
              { label: t('dimensions.site'), value: 'site' as const },
              { label: t('dimensions.model'), value: 'model' as const },
              { label: t('dimensions.apiKey'), value: 'api_key' as const },
            ]}
          />
        </div>
      )}
    >
      {slices.length ? (
        <div ref={bodyRef} className="flex min-h-0 flex-1 items-center gap-6">
          <div
            className="relative shrink-0"
            style={{ width: roseSize, height: roseSize }}
          >
            <ResponsiveContainer width="100%" height="100%">
              <PieChart accessibilityLayer={false}>
                <Pie
                  data={slices}
                  dataKey="value"
                  nameKey="name"
                  cx="50%"
                  cy="50%"
                  innerRadius={roseInnerRadius}
                  outerRadius={(dataPoint: DonutSlice) => dataPoint.outerRadius}
                  paddingAngle={slices.length > 8 ? 1 : 2}
                  stroke={chartSliceStroke}
                  strokeWidth={1.5}
                  isAnimationActive={true}
                  animationBegin={0}
                  animationDuration={500}
                  animationEasing="ease-out"
                  onClick={(entry) => {
                    const slice = entry as unknown as DonutSlice
                    if (slice.key === 'unknown') return
                    onDrillDown(dimension, { key: slice.key, id: slice.id })
                  }}
                >
                  {slices.map((slice) => (
                    <Cell
                      key={slice.key}
                      fill={slice.color}
                      className={slice.key === 'unknown' ? 'opacity-40' : 'cursor-pointer'}
                    />
                  ))}
                </Pie>
                <Tooltip
                  isAnimationActive={false}
                  wrapperStyle={{ zIndex: 10 }}
                  content={(props) => (
                    <DonutTooltip
                      {...props}
                      mode={mode}
                      currency={currency}
                      total={total}
                      unpricedLabel={t('donut.unpriced')}
                      formatRequests={(count) => t('donut.requestsCount', { count: formatCompactNumber(count) })}
                    />
                  )}
                />
              </PieChart>
            </ResponsiveContainer>
            {/* 中心总计：pointer-events-none + z-0，Tooltip 在其上方 */}
            <div className="pointer-events-none absolute inset-0 z-0 flex flex-col items-center justify-center">
              <span className="leading-tight text-label-2" style={{ fontSize: roseCenterLabelSize }}>
                {centerLabel}
              </span>
              <span
                className="font-semibold tabular-nums leading-tight text-label"
                style={{ fontSize: roseCenterValueSize }}
              >
                {centerValue}
              </span>
            </div>
          </div>

          <div className="min-h-0 min-w-0 flex-1 self-center overflow-y-auto pr-2" style={{ maxHeight: roseSize }}>
            <div className="grid grid-cols-1 divide-y divide-separator">
              {listItems.map((slice) => {
                const isUnpricedRow = mode === 'cost' && slice.unpriced
                const primaryMetric = slice.unpriced
                  ? t('donut.unpriced')
                  : formatDashboardCurrency(slice.cost, currency, 2)
                const secondaryMetric = isUnpricedRow
                  ? `${formatDashboardTokens(slice.tokens)} · ${t('donut.requestsCount', { count: formatCompactNumber(slice.requests) })}`
                  : mode === 'requests'
                    ? `${t('donut.requestsCount', { count: formatCompactNumber(slice.requests) })}${total > 0 ? ` · ${((slice.value / total) * 100).toFixed(1)}%` : ''}`
                    : `${formatDashboardTokens(slice.tokens)}${total > 0 ? ` · ${((slice.value / total) * 100).toFixed(1)}%` : ''}`
                return (
                  <button
                    key={slice.key}
                    type="button"
                    onClick={() => {
                      if (slice.key === 'unknown') return
                      onDrillDown(dimension, { key: slice.key, id: slice.id })
                    }}
                    className="flex min-w-0 items-start gap-2 py-2 text-left first:pt-0 last:pb-0 disabled:cursor-default"
                    disabled={slice.key === 'unknown'}
                  >
                    <span
                      className={isUnpricedRow
                        ? 'mt-1.5 size-2 shrink-0 rounded-[2px] border border-label-3 bg-transparent'
                        : 'mt-1.5 size-2 shrink-0 rounded-[2px]'}
                      style={isUnpricedRow
                        ? undefined
                        : {
                            backgroundColor: slice.color,
                            opacity: slice.key === 'unknown' ? 0.5 : 1,
                          }}
                    />
                    {/* 名称完整显示（可换行），金额/无计费 · Token · 占比放在第二行，窄卡片里不再被截成「gpt…」 */}
                    <span className="min-w-0 flex-1">
                      <span className="block break-all text-body text-label">{slice.name}</span>
                      <span className="mt-0.5 block font-mono text-caption-1 tabular-nums text-label-3">
                        {`${primaryMetric} · ${secondaryMetric}`}
                      </span>
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
        </div>
      ) : (
        <div className="flex h-full items-center justify-center text-body text-label-2">
          {t('donut.empty')}
        </div>
      )}
    </AnalyticsPanel>
  )
}

function DonutTooltip({
  active,
  payload,
  mode,
  currency,
  total,
  unpricedLabel,
  formatRequests,
}: TooltipContentProps & {
  mode: DonutMode
  currency: string
  total: number
  unpricedLabel: string
  formatRequests: (count: number) => string
}) {
  if (!active || !payload?.length) return null
  const item = payload[0]
  const slice = item.payload as DonutSlice | undefined
  const value = Number(item.value ?? 0)
  const tokens = Number(slice?.tokens ?? 0)
  const requests = Number(slice?.requests ?? 0)
  const pct = total > 0 ? ` · ${((value / total) * 100).toFixed(1)}%` : ''
  const detail = mode === 'cost'
    ? `${formatDashboardCurrency(value, currency, 4)} · ${formatDashboardTokens(tokens)}${pct}`
    : mode === 'tokens'
      ? `${unpricedLabel} · ${formatDashboardTokens(tokens)} · ${formatRequests(requests)}${pct}`
      : `${unpricedLabel} · ${formatRequests(requests)}${pct}`
  return (
    <div style={dashboardTooltipStyle} className="min-w-[160px] text-footnote">
      <div className="mb-1 flex items-center gap-1.5 text-label">
        <span className="size-2 shrink-0 rounded-[2px]" style={{ backgroundColor: item.payload?.fill ?? item.color }} />
        <span className="truncate font-medium">{item.name}</span>
      </div>
      <div className="tabular-nums text-label-2">
        {detail}
      </div>
    </div>
  )
}
