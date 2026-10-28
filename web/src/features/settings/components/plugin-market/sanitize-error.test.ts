import { describe, expect, it } from 'vitest'
import { sanitizeErrorText } from './sanitize-error'

describe('sanitizeErrorText', () => {
  it('replaces a URL inside a Go error string', () => {
    expect(sanitizeErrorText('Get "https://x.example/v1/root.json": EOF')).toBe('Get "…": EOF')
  })

  it('leaves text without a URL unchanged', () => {
    expect(sanitizeErrorText('dial tcp: lookup market: no such host')).toBe('dial tcp: lookup market: no such host')
  })

  it('keeps an empty string empty', () => {
    expect(sanitizeErrorText('')).toBe('')
  })

  it('replaces every URL', () => {
    expect(sanitizeErrorText('see https://a.example/x and http://b.example/y')).toBe('see … and …')
  })
})
