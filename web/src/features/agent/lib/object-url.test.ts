import { afterEach, describe, expect, it, vi } from 'vitest'
import { objectUrlFor } from './object-url'

describe('objectUrlFor', () => {
  afterEach(() => vi.restoreAllMocks())

  it('同一个 Blob 多次获取得到同一个 URL,且不会被提前 revoke', () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:one')
    const revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const blob = new Blob(['x'])
    expect(objectUrlFor(blob)).toBe('blob:one')
    expect(objectUrlFor(blob)).toBe('blob:one')
    expect(create).toHaveBeenCalledOnce()
    expect(revoke).not.toHaveBeenCalled()
  })

  it('不同 Blob 各自创建 URL', () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValueOnce('blob:a').mockReturnValueOnce('blob:b')
    expect(objectUrlFor(new Blob(['a']))).toBe('blob:a')
    expect(objectUrlFor(new Blob(['b']))).toBe('blob:b')
    expect(create).toHaveBeenCalledTimes(2)
  })
})
