import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '@/lib/http'
import { fetchSessionFile, sessionFileErrorKind } from './agent'

afterEach(() => { vi.unstubAllGlobals() })

describe('会话文件 API', () => {
  it('按 Blob 类型区分图片与文本', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response(new Blob(['image'], { type: 'image/png' })))
      .mockResolvedValueOnce(new Response(new Blob(['hello'], { type: 'text/plain; charset=utf-8' }))))
    await expect(fetchSessionFile('s', 'a.png')).resolves.toMatchObject({ kind: 'image' })
    await expect(fetchSessionFile('s', 'a.txt')).resolves.toEqual({ kind: 'text', text: 'hello' })
  })

  it('区分文件读取错误', () => {
    expect(sessionFileErrorKind(new APIError('', 404))).toBe('missing')
    expect(sessionFileErrorKind(new APIError('', 413))).toBe('tooLarge')
    expect(sessionFileErrorKind(new APIError('', 415))).toBe('unsupported')
    expect(sessionFileErrorKind(new APIError('', 500))).toBe('failed')
    expect(sessionFileErrorKind(new Error())).toBe('failed')
  })
})
