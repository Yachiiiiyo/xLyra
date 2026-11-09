import { Fragment, type ComponentProps, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Database, Gauge } from 'lucide-react'
import { cn } from '@/lib/utils'
import { HoverDetails } from '@/components/common/hover-details'
import { TokenUsageHoverCard } from '@/components/common/token-usage-hover-card'
import { useMobileLayout } from '@/hooks/use-media-query'
import type { AgentSessionStats as AgentSessionStatsData } from '@/features/agent/api/agent'
import {
  compactTokenThreshold,
  contextUsage,
  formatFullTokenCount,
  formatTokenCount,
  hasSessionStats,
  statPercent,
  tokenUsageBreakdown,
  type ContextRingTone,
} from '@/features/agent/lib/agent-session-stats'

const RING_RADIUS = 6.5
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS
const RING_TONE_CLASS: Record<ContextRingTone, string> = {
  calm: 'stroke-label-3',
  warning: 'stroke-warning',
  danger: 'stroke-danger',
}
const EMPTY_SESSION_STATS: AgentSessionStatsData = {
  turns: 0,
  steps: 0,
  total_tokens: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cache_hit_rate: null,
  tokens_per_second: null,
  context_tokens: 0,
  context_window: 200_000,
  compact_ratio: 0.9,
}

type AgentSessionStatsProps = {
  stats: AgentSessionStatsData | null
  reserve?: boolean
  /** 统计小字的字号/颜色/外边距；对齐方式由组件按桌面（靠左）/移动端（居中）决定 */
  className?: string
}

export function AgentSessionStats({ stats: providedStats, reserve = false, className }: AgentSessionStatsProps) {
  const { t } = useTranslation('agent')
  const isMobile = useMobileLayout()
  const stats = reserve && (!providedStats || !hasSessionStats(providedStats)) ? EMPTY_SESSION_STATS : providedStats

  if (!stats || (!reserve && !hasSessionStats(stats))) return null

  const tokensPerSecond = stats.tokens_per_second
  const cacheHit = statPercent(stats.cache_hit_rate)
  const usage = contextUsage(stats)
  const tokPerSecondText = typeof tokensPerSecond === 'number' && Number.isFinite(tokensPerSecond)
    ? t('chat.stats.tokPerSecond', { value: Math.round(tokensPerSecond) })
    : null
  const cacheHitText = cacheHit === null ? null : t('chat.stats.cacheHit', { percent: cacheHit })
  const tokenLabels = {
    total: t('chat.stats.tokenPopover.total'),
    input: t('chat.stats.tokenPopover.input'),
    output: t('chat.stats.tokenPopover.output'),
    cached: t('chat.stats.tokenPopover.cached'),
    hitRate: t('chat.stats.tokenPopover.hitRate'),
  }
  const tokenColumns = [{ usage: tokenUsageBreakdown(stats) }]
  const ring = usage ? (
    <HoverDetails
      asChild
      title={t('chat.stats.contextPopover.title')}
      contentClassName="w-[280px]"
      content={<ContextWindowDetails stats={stats} />}
    >
      <ContextRing percent={usage.percent} tone={usage.tone} />
    </HoverDetails>
  ) : null

  if (isMobile) {
    const tokenUsage = cacheHitText === null ? null : (
      <TokenUsageHoverCard columns={tokenColumns} labels={tokenLabels}>
        <span>{cacheHitText}</span>
      </TokenUsageHoverCard>
    )
    const items = [tokPerSecondText, tokenUsage, ring].filter((item) => item !== null)
    if (items.length === 0) return null
    return (
      <div className={cn('flex min-h-5 items-center justify-center gap-3 overflow-hidden whitespace-nowrap', className)}>
        <StatsItems items={items} />
      </div>
    )
  }

  const slots = [
    [
      <span key="turns" className="flex items-center gap-1">
        <Gauge className="size-3.5" aria-hidden="true" />
        {t('chat.stats.turns', { turns: stats.turns, steps: stats.steps })}
      </span>,
      tokPerSecondText,
    ],
    [
      <TokenUsageHoverCard key="token-usage" columns={tokenColumns} labels={tokenLabels}>
        <span className="flex items-center gap-1">
          <StatsItems items={[
            <span key="tokens" className="flex items-center gap-1">
              <Database className="size-3.5" aria-hidden="true" />
              {t('chat.stats.tokens', { value: formatTokenCount(stats.total_tokens) })}
            </span>,
            cacheHitText,
          ].filter((item) => item !== null)} />
        </span>
      </TokenUsageHoverCard>,
    ],
    ring ? [ring] : [],
  ].map((group) => group.filter((item) => item !== null))

  // 左（轮/步 · tok/s）、中（累计 token · 缓存命中）、右（上下文圆环）三栏对齐；
  // 固定三个槽位，某一栏为空时其余两栏位置不动
  const slotClassNames = ['justify-self-start', 'justify-self-center', 'justify-self-end']

  return (
    <div className={cn('grid min-h-5 min-w-0 grid-cols-[1fr_auto_1fr] items-center gap-3 overflow-hidden whitespace-nowrap px-4', className)}>
      {slots.map((group, index) => (
        <span key={index} className={cn('flex min-w-0 items-center gap-1', slotClassNames[index])}>
          <StatsItems items={group} />
        </span>
      ))}
    </div>
  )
}

export function ContextWindowDetails({ stats }: { stats: AgentSessionStatsData }) {
  const { t } = useTranslation('agent')
  const usage = contextUsage(stats)
  if (!usage) return null

  return (
    <div className="space-y-1">
      <StatsDetailRow label={t('chat.stats.contextPopover.window')} value={formatFullTokenCount(usage.window)} />
      <StatsDetailRow label={t('chat.stats.contextPopover.used')} value={formatFullTokenCount(usage.used)} />
      <StatsDetailRow label={t('chat.stats.contextPopover.usage')} value={`${usage.percent}%`} />
      <StatsDetailRow
        label={t('chat.stats.contextPopover.compact')}
        value={t('chat.stats.contextPopover.compactValue', {
          percent: usage.compactPercent,
          tokens: formatFullTokenCount(compactTokenThreshold(stats)),
        })}
      />
    </div>
  )
}

function StatsDetailRow({ label, value, indented = false }: { label: string; value: string; indented?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <span className={cn('min-w-0 text-label-3', indented && 'pl-3')}>{label}</span>
      <span className="shrink-0 text-right font-medium tabular-nums text-label">{value}</span>
    </div>
  )
}

/** 同组统计之间用「·」分隔 */
function StatsItems({ items }: { items: ReactNode[] }) {
  return (
    <>
      {items.map((item, index) => (
        <Fragment key={index}>
          {index > 0 ? <span aria-hidden="true">·</span> : null}
          {item}
        </Fragment>
      ))}
    </>
  )
}

/** 上下文占用圆环：直径 16px，颜色随占用与水线变化 */
function ContextRing({ percent, tone, className, ...props }: ComponentProps<'span'> & { percent: number; tone: ContextRingTone }) {
  return (
    <span {...props} className={cn('flex shrink-0 items-center gap-1', className)}>
      <svg viewBox="0 0 16 16" className="size-4 -rotate-90" aria-hidden="true">
        <circle cx="8" cy="8" r={RING_RADIUS} fill="none" strokeWidth="2.5" className="stroke-fill-3" />
        <circle
          cx="8"
          cy="8"
          r={RING_RADIUS}
          fill="none"
          strokeWidth="2.5"
          strokeDasharray={RING_CIRCUMFERENCE}
          strokeDashoffset={RING_CIRCUMFERENCE * (1 - percent / 100)}
          strokeLinecap="round"
          className={RING_TONE_CLASS[tone]}
        />
      </svg>
      <span>{percent}%</span>
    </span>
  )
}
