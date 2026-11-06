import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAuthStore } from '@/stores/auth-store'
import { readQuerySnapshot, writeQuerySnapshot } from '@/lib/query-snapshot'
import type { AdminRecord } from '@/features/auth/api/auth'

vi.mock('@/features/auth/api/auth', () => ({
  fetchAuthState: vi.fn(async () => ({
    initialized: true,
    authenticated: true,
    session: {
      expiresAt: null,
      csrfToken: null,
      admin: {
        id: 'user-1',
        username: 'user-1',
        nickname: 'user-1',
        avatar: '',
        role: 'admin',
        status: 'active',
        totpEnabled: false,
      },
    },
  })),
  deleteCurrentAdminSession: vi.fn(async () => undefined),
}))

const initialAuthState = useAuthStore.getState()

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

function buildAdmin(id: string): AdminRecord {
  return {
    id,
    username: id,
    nickname: id,
    avatar: '',
    role: 'admin',
    status: 'active',
    totpEnabled: false,
  }
}

function buildSession(admin: AdminRecord) {
  return {
    expiresAt: null,
    csrfToken: null,
    admin,
  }
}

afterEach(() => {
  useAuthStore.setState(initialAuthState, true)
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('auth store 快照隔离', () => {
  it('登录用户与 snapshot-user 不同时快照被清空', () => {
    const { values } = stubStorage()
    values.set('xlyra:snapshot:agent:sessions', JSON.stringify({
      v: 1,
      user: 'user-1',
      at: Date.now(),
      data: [{ session_id: 'session-1', title: '私有标题' }],
    }))
    values.set('xlyra:snapshot-user', 'user-1')

    useAuthStore.getState().setSession(buildSession(buildAdmin('user-2')))

    expect(useAuthStore.getState().user?.id).toBe('user-2')
    expect(values.has('xlyra:snapshot:agent:sessions')).toBe(false)
    expect(values.has('xlyra:snapshot-user')).toBe(false)
  })

  it('登录用户与 snapshot-user 相同时快照保留', () => {
    const { values } = stubStorage()
    values.set('xlyra:snapshot:agent:sessions', JSON.stringify({
      v: 1,
      user: 'user-1',
      at: Date.now(),
      data: [{ session_id: 'session-1', title: '私有标题' }],
    }))
    values.set('xlyra:snapshot-user', 'user-1')

    useAuthStore.getState().setSession(buildSession(buildAdmin('user-1')))

    expect(useAuthStore.getState().user?.id).toBe('user-1')
    expect(readQuerySnapshot('agent:sessions')).toEqual([{ session_id: 'session-1', title: '私有标题' }])
    expect(values.has('xlyra:snapshot-user')).toBe(true)
  })

  it('setAdmin 与 snapshot-user 不同时快照被清空', () => {
    const { values } = stubStorage()
    values.set('xlyra:snapshot-user', 'user-1')

    useAuthStore.getState().setAdmin(buildAdmin('user-2'))

    expect(values.has('xlyra:snapshot-user')).toBe(false)
  })

  it('initialize 鉴权成功后与 snapshot-user 不同时快照被清空', async () => {
    const { values } = stubStorage()
    values.set('xlyra:snapshot-user', 'user-other')

    await useAuthStore.getState().initialize(true)

    expect(useAuthStore.getState().user?.id).toBe('user-1')
    expect(values.has('xlyra:snapshot-user')).toBe(false)
  })

  it('写入快照后 snapshot-user 同步更新', () => {
    const { values } = stubStorage()
    useAuthStore.getState().setSession(buildSession(buildAdmin('user-1')))

    writeQuerySnapshot('agent:sessions', [])

    expect(values.get('xlyra:snapshot-user')).toBe('user-1')
  })
})
