import { describe, expect, it } from 'vitest'

import { formatAbsoluteTime, formatMessageTime, formatRelativeTime } from '@/features/agent/lib/relative-time'

const NOW = new Date('2026-08-28T12:00:00Z').getTime()

describe('formatRelativeTime', () => {
  it('returns empty string for missing or invalid values', () => {
    expect(formatRelativeTime(undefined, 'zh', NOW)).toBe('')
    expect(formatRelativeTime('not-a-date', 'zh', NOW)).toBe('')
  })

  it('formats recent times in zh', () => {
    expect(formatRelativeTime(new Date(NOW - 30_000).toISOString(), 'zh', NOW)).toBe('30秒钟前')
    expect(formatRelativeTime(new Date(NOW - 7 * 3600_000).toISOString(), 'zh', NOW)).toBe('7小时前')
    expect(formatRelativeTime(new Date(NOW - 86400_000).toISOString(), 'zh', NOW)).toBe('昨天')
    expect(formatRelativeTime(new Date(NOW - 18 * 86400_000).toISOString(), 'zh', NOW)).toBe('18天前')
  })

  it('formats in en and jp locales', () => {
    expect(formatRelativeTime(new Date(NOW - 3600_000).toISOString(), 'en', NOW)).toBe('1 hour ago')
    expect(formatRelativeTime(new Date(NOW - 3600_000).toISOString(), 'jp', NOW)).toBe('1 時間前')
  })

  it('accepts numeric timestamps', () => {
    expect(formatRelativeTime(NOW - 2 * 86400_000, 'en', NOW)).toBe('2 days ago')
  })
})

describe('formatMessageTime', () => {
  it('12 小时内用相对时间', () => {
    expect(formatMessageTime(NOW - 5 * 60_000, 'zh', NOW)).toBe('5分钟前')
    expect(formatMessageTime(NOW - 11 * 3600_000, 'zh', NOW)).toBe('11小时前')
    expect(formatMessageTime(NOW - 3600_000, 'en', NOW)).toBe('1 hour ago')
  })

  it('超过 12 小时用具体时间,同年不带年份,跨年带年份', () => {
    const sameYear = formatMessageTime(NOW - 13 * 3600_000, 'zh', NOW)
    expect(sameYear).toMatch(/\d{2}\/\d{2}\s?\d{2}:\d{2}/)
    expect(sameYear).not.toContain('2026')
    const crossYear = formatMessageTime(new Date('2025-03-04T08:09:00').getTime(), 'zh', NOW)
    expect(crossYear).toContain('2025')
    expect(crossYear).toMatch(/\d{2}:\d{2}/)
  })

  it('边界:恰好 12 小时按具体时间,缺失或非法值返回空串', () => {
    expect(formatMessageTime(NOW - 12 * 3600_000, 'zh', NOW)).toBe(formatAbsoluteTime(NOW - 12 * 3600_000, 'zh', NOW))
    expect(formatMessageTime(undefined, 'zh', NOW)).toBe('')
    expect(formatMessageTime('bad', 'zh', NOW)).toBe('')
  })
})
