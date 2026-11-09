import { describe, expect, it } from 'vitest'
import { formatBytes } from '@/features/agent/lib/format-bytes'

describe('formatBytes', () => {
  it('按 1024 进制换算并保留一位小数', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(15 * 1024)).toBe('15 KB')
    expect(formatBytes(1024 * 1024)).toBe('1 MB')
    expect(formatBytes(2.5 * 1024 * 1024 * 1024)).toBe('2.5 GB')
  })

  it('非法输入返回空字符串', () => {
    expect(formatBytes(-1)).toBe('')
    expect(formatBytes(Number.NaN)).toBe('')
    expect(formatBytes(Number.POSITIVE_INFINITY)).toBe('')
  })
})
