import { describe, expect, it, vi } from 'vitest'
import { isUUID, newId } from '@/lib/id'

describe('newId', () => {
  it('prefers crypto.randomUUID when available', () => {
    const expected = '123e4567-e89b-42d3-a456-426614174000'
    vi.stubGlobal('crypto', { randomUUID: () => expected })

    expect(newId()).toBe(expected)
  })

  it('falls back when crypto.randomUUID throws', () => {
    vi.stubGlobal('crypto', {
      randomUUID: () => {
        throw new Error('unavailable')
      },
      getRandomValues: (bytes: Uint8Array) => {
        bytes.fill(1)
        return bytes
      },
    })

    expect(isUUID(newId())).toBe(true)
  })

  it('returns a UUID when randomUUID is unavailable in an insecure context', () => {
    vi.stubGlobal('crypto', {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.fill(0)
        return bytes
      },
    })

    const id = newId()

    expect(isUUID(id)).toBe(true)
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-8[0-9a-f]{3}-[0-9a-f]{12}$/)
  })

  it('still returns a UUID when Web Crypto is unavailable', () => {
    vi.stubGlobal('crypto', {})

    expect(isUUID(newId())).toBe(true)
  })
})
