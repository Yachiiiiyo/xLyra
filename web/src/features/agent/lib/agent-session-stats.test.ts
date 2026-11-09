import { describe, expect, it } from 'vitest'
import {
  compactTokenThreshold,
  contextRingTone,
  contextUsage,
  formatFullTokenCount,
  formatTokenCount,
  hasSessionStats,
  statPercent,
  tokenUsageBreakdown,
} from '@/features/agent/lib/agent-session-stats'
import type { AgentSessionStats } from '@/features/agent/api/agent'

const baseStats: AgentSessionStats = {
  turns: 2,
  steps: 44,
  total_tokens: 2_600_000,
  input_tokens: 2_400_000,
  output_tokens: 200_000,
  cache_read_tokens: 1_980_000,
  cache_write_tokens: 120_000,
  cache_hit_rate: 0.83,
  tokens_per_second: 159,
  context_tokens: 54_000,
  context_window: 200_000,
  compact_ratio: 0.9,
}

describe('formatTokenCount', () => {
  it('小于 1000 用原数', () => {
    expect(formatTokenCount(0)).toBe('0')
    expect(formatTokenCount(999)).toBe('999')
  })

  it('≥1000 用 K、≥1e6 用 M，保留 1 位小数并去掉多余的 .0', () => {
    expect(formatTokenCount(1_000)).toBe('1K')
    expect(formatTokenCount(1_599)).toBe('1.6K')
    expect(formatTokenCount(2_600_000)).toBe('2.6M')
    expect(formatTokenCount(1_000_000)).toBe('1M')
    expect(formatTokenCount(12_500_000)).toBe('12.5M')
  })

  it('非法与负数归零', () => {
    expect(formatTokenCount(Number.NaN)).toBe('0')
    expect(formatTokenCount(-5)).toBe('0')
  })
})

describe('formatFullTokenCount', () => {
  it('保留完整数字并添加千分位', () => {
    expect(formatFullTokenCount(0)).toBe('0')
    expect(formatFullTokenCount(999)).toBe('999')
    expect(formatFullTokenCount(1_000)).toBe('1,000')
    expect(formatFullTokenCount(2_600_000)).toBe('2,600,000')
  })

  it('非法与负数归零', () => {
    expect(formatFullTokenCount(Number.NaN)).toBe('0')
    expect(formatFullTokenCount(-5)).toBe('0')
  })
})

describe('statPercent', () => {
  it('把 0..1 的比例四舍五入成整数百分比并夹取越界值', () => {
    expect(statPercent(0.83)).toBe(83)
    expect(statPercent(0.835)).toBe(84)
    expect(statPercent(1.2)).toBe(100)
    expect(statPercent(-0.1)).toBe(0)
  })

  it('无数据返回 null', () => {
    expect(statPercent(null)).toBeNull()
    expect(statPercent(undefined)).toBeNull()
    expect(statPercent(Number.NaN)).toBeNull()
  })
})

describe('hasSessionStats', () => {
  it('轮与步都为空时视为无数据', () => {
    expect(hasSessionStats(baseStats)).toBe(true)
    expect(hasSessionStats({ ...baseStats, turns: 0, steps: 0 })).toBe(false)
    expect(hasSessionStats({ ...baseStats, turns: 0, steps: 3 })).toBe(true)
  })
})

describe('contextUsage', () => {
  it('按 context_tokens ÷ context_window 四舍五入并封顶 100', () => {
    expect(contextUsage(baseStats)?.percent).toBe(27)
    expect(contextUsage({ ...baseStats, context_tokens: 199_600 })?.percent).toBe(100)
    expect(contextUsage({ ...baseStats, context_tokens: 400_000 })?.percent).toBe(100)
    expect(contextUsage({ ...baseStats, context_tokens: 0 })?.percent).toBe(0)
  })

  it('水位线百分比与已用 token 用于 tooltip', () => {
    const usage = contextUsage(baseStats)
    expect(usage).toMatchObject({ used: 54_000, window: 200_000, compactPercent: 90 })
  })

  it('窗口未知时没有圆环', () => {
    expect(contextUsage({ ...baseStats, context_window: 0 })).toBeNull()
    expect(contextUsage({ ...baseStats, context_window: Number.NaN })).toBeNull()
  })
})

describe('compactTokenThreshold', () => {
  it('按水位线比例计算约略 token 数', () => {
    expect(compactTokenThreshold(baseStats)).toBe(180_000)
    expect(compactTokenThreshold({ ...baseStats, compact_ratio: 0.5 })).toBe(100_000)
  })

  it('夹取越界比例，窗口未知时为 0', () => {
    expect(compactTokenThreshold({ ...baseStats, compact_ratio: 1.2 })).toBe(200_000)
    expect(compactTokenThreshold({ ...baseStats, compact_ratio: -0.1 })).toBe(0)
    expect(compactTokenThreshold({ ...baseStats, context_window: 0 })).toBe(0)
  })
})

describe('contextRingTone', () => {
  it('70% 以下弱化色，70% 起到水位线前琥珀色，达到水位线红色', () => {
    expect(contextRingTone(0, 90)).toBe('calm')
    expect(contextRingTone(69, 90)).toBe('calm')
    expect(contextRingTone(70, 90)).toBe('warning')
    expect(contextRingTone(89, 90)).toBe('warning')
    expect(contextRingTone(90, 90)).toBe('danger')
    expect(contextRingTone(100, 90)).toBe('danger')
  })

  it('水位线低于 70% 时达到水位线即红色', () => {
    expect(contextRingTone(60, 60)).toBe('danger')
  })
})

describe('tokenUsageBreakdown', () => {
  it('对齐请求页「总 Tokens」弹窗：总计、输入（含缓存）、输出、缓存命中', () => {
    expect(tokenUsageBreakdown(baseStats)).toEqual({
      total: 2_600_000,
      input: 2_400_000,
      output: 200_000,
      cached: 1_980_000,
    })
  })
})
