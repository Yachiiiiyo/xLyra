import { afterEach, describe, expect, it, vi } from 'vitest'
import { clearQuerySnapshots, readQuerySnapshot, writeQuerySnapshot } from '@/lib/query-snapshot'
import { useAuthStore } from '@/stores/auth-store'

const initialAuthState = useAuthStore.getState()

function setUser(id: string | null) {
  useAuthStore.setState({
    status: id ? 'authenticated' : 'unauthenticated',
    user: id ? {
      id,
      username: id,
      nickname: id,
      avatar: '',
      displayName: id,
      role: 'Admin',
      status: 'active',
      totpEnabled: false,
    } : null,
  })
}

function stubStorage() {
  const values = new Map<string, string>()
  const storage = {
    get length() { return values.size },
    clear: vi.fn(() => values.clear()),
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    key: vi.fn((index: number) => Array.from(values.keys())[index] ?? null),
    removeItem: vi.fn((key: string) => values.delete(key)),
    setItem: vi.fn((key: string, value: string) => values.set(key, value)),
  }
  vi.stubGlobal('localStorage', storage)
  return { storage, values }
}

afterEach(() => {
  useAuthStore.setState(initialAuthState, true)
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('query snapshots', () => {
  it('按当前用户读写快照', () => {
    const { storage } = stubStorage()
    setUser('user-1')
    const data = { sessions: [{ session_id: 'session-1', title: '标题' }] }

    writeQuerySnapshot('agent:sessions', data)

    expect(readQuerySnapshot<typeof data>('agent:sessions')).toEqual(data)
    expect(JSON.parse(storage.setItem.mock.calls[0][1])).toEqual({
      v: 1,
      user: 'user-1',
      at: expect.any(Number),
      data,
    })
  })

  it('无登录用户且无 snapshot-user 时不读不写', () => {
    const { storage } = stubStorage()
    setUser(null)

    writeQuerySnapshot('agent:sessions', [])
    expect(readQuerySnapshot('agent:sessions')).toBeUndefined()
    expect(storage.setItem).not.toHaveBeenCalled()
    expect(storage.getItem).not.toHaveBeenCalledWith('xlyra:snapshot:agent:sessions')
  })

  it('鉴权未完成（user 为 null）但有 snapshot-user 时可读到快照', () => {
    const { values } = stubStorage()
    setUser('user-1')
    const data = [{ session_id: 'session-1', title: '标题' }]
    writeQuerySnapshot('agent:sessions', data)
    setUser(null)

    expect(readQuerySnapshot('agent:sessions')).toEqual(data)
    expect(values.get('xlyra:snapshot-user')).toBe('user-1')
  })

  it('用户不匹配时忽略并删除快照', () => {
    const { values } = stubStorage()
    setUser('user-1')
    writeQuerySnapshot('agent:sessions', [{ session_id: 'session-1' }])
    setUser('user-2')

    expect(readQuerySnapshot('agent:sessions')).toBeUndefined()
    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
  })

  it('过期快照会被删除', () => {
    const { values } = stubStorage()
    setUser('user-1')
    values.set('xlyra:snapshot:agent:sessions', JSON.stringify({
      v: 1,
      user: 'user-1',
      at: Date.now() - 8 * 24 * 60 * 60 * 1000,
      data: [],
    }))

    expect(readQuerySnapshot('agent:sessions')).toBeUndefined()
    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
  })

  it.each([
    '{broken',
    JSON.stringify({ v: 2, user: 'user-1', at: Date.now(), data: [] }),
  ])('损坏或版本不符的快照会被删除', (stored) => {
    const { values } = stubStorage()
    setUser('user-1')
    values.set('xlyra:snapshot:agent:sessions', stored)

    expect(readQuerySnapshot('agent:sessions')).toBeUndefined()
    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
  })

  it('序列化后超过 256KB 时跳过写入', () => {
    const { storage, values } = stubStorage()
    setUser('user-1')

    writeQuerySnapshot('agent:sessions', 'x'.repeat(256 * 1024))

    expect(storage.setItem).not.toHaveBeenCalled()
    expect(values.size).toBe(0)
  })

  it('存储抛错时静默忽略', () => {
    setUser('user-1')
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => { throw new Error('blocked') }),
      setItem: vi.fn(() => { throw new Error('quota exceeded') }),
      removeItem: vi.fn(() => { throw new Error('blocked') }),
    })

    expect(readQuerySnapshot('agent:sessions')).toBeUndefined()
    expect(() => writeQuerySnapshot('agent:sessions', [])).not.toThrow()
  })

  it('clearQuerySnapshots 清掉 snapshot-user', () => {
    const { values } = stubStorage()
    setUser('user-1')
    writeQuerySnapshot('agent:sessions', [])

    clearQuerySnapshots()

    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
    expect(values.has('xlyra:snapshot-user')).toBe(false)
  })

  it('只清理查询快照', () => {
    const { values } = stubStorage()
    setUser('user-1')
    writeQuerySnapshot('agent:sessions', [])
    writeQuerySnapshot('agent:model-memory', {})
    values.set('xlyra-i18n-lang', 'zh')

    clearQuerySnapshots()

    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
    expect(values.has('xlyra:snapshot:agent:model-memory')).toBe(false)
    expect(values.get('xlyra-i18n-lang')).toBe('zh')
  })

  it('clearSession 在清空用户前删除快照', () => {
    const { values } = stubStorage()
    setUser('user-1')
    writeQuerySnapshot('agent:sessions', [{ session_id: 'session-1', title: '私有标题' }])
    values.set('xlyra-i18n-lang', 'zh')

    useAuthStore.getState().clearSession()

    expect(useAuthStore.getState().user).toBeNull()
    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
    expect(values.get('xlyra-i18n-lang')).toBe('zh')
  })
})
