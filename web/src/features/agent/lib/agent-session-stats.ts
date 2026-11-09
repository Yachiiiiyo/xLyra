import type { AgentSessionStats } from '@/features/agent/api/agent'

export type ContextRingTone = 'calm' | 'warning' | 'danger'

export type ContextUsage = {
  /** 已用百分比，四舍五入并封顶 100 */
  percent: number
  /** 已用 token 原值（tooltip 用） */
  used: number
  window: number
  /** 压缩水位线百分比（0..100） */
  compactPercent: number
  tone: ContextRingTone
}

/** 累计 token：<1000 原数，≥1000 用 K，≥1e6 用 M，保留 1 位小数并去掉多余的 .0 */
export function formatTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0'
  if (value < 1000) return String(Math.round(value))
  const [divisor, unit] = value < 1_000_000 ? [1000, 'K'] : [1_000_000, 'M']
  return `${(value / divisor).toFixed(1).replace(/\.0$/, '')}${unit}`
}

/** 浮窗中的完整 token 数：千分位分隔，不使用 K/M 缩写 */
export function formatFullTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0'
  return Math.round(value).toLocaleString('en-US')
}

/** 比例 0..1 → 整数百分比；无数据返回 null */
export function statPercent(rate: number | null | undefined): number | null {
  if (typeof rate !== 'number' || !Number.isFinite(rate)) return null
  return Math.round(Math.min(Math.max(rate, 0), 1) * 100)
}

/** 轮次与步数都为空表示新会话/暂无数据，整行统计隐藏 */
export function hasSessionStats(stats: AgentSessionStats): boolean {
  return stats.turns > 0 || stats.steps > 0
}

/** 上下文占用；窗口未知（0）时没有圆环 */
export function contextUsage(stats: AgentSessionStats): ContextUsage | null {
  const window = stats.context_window
  if (!Number.isFinite(window) || window <= 0) return null
  const used = Number.isFinite(stats.context_tokens) ? Math.max(stats.context_tokens, 0) : 0
  const percent = Math.min(100, Math.round((used / window) * 100))
  const compactPercent = statPercent(stats.compact_ratio) ?? 100
  return { percent, used, window, compactPercent, tone: contextRingTone(percent, compactPercent) }
}

/** 自动压缩水位线对应的约略 token 数 */
export function compactTokenThreshold(stats: Pick<AgentSessionStats, 'context_window' | 'compact_ratio'>): number {
  if (!Number.isFinite(stats.context_window) || stats.context_window <= 0) return 0
  const ratio = Number.isFinite(stats.compact_ratio) ? Math.min(Math.max(stats.compact_ratio, 0), 1) : 1
  return Math.round(stats.context_window * ratio)
}

/** <70% 弱化色；70% 起到压缩水位线前琥珀色；达到或超过水位线红色 */
export function contextRingTone(percent: number, compactPercent: number): ContextRingTone {
  const watermark = Number.isFinite(compactPercent) ? compactPercent : 100
  if (percent >= watermark) return 'danger'
  return percent >= 70 ? 'warning' : 'calm'
}

/** 对齐请求页「总 Tokens」弹窗的口径：输入含缓存命中，命中率 = 缓存命中 ÷ 输入。 */
export function tokenUsageBreakdown(stats: AgentSessionStats) {
  return {
    total: stats.total_tokens,
    input: stats.input_tokens,
    output: stats.output_tokens,
    cached: stats.cache_read_tokens,
  }
}
