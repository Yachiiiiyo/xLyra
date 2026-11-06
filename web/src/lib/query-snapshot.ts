import { useAuthStore } from '@/stores/auth-store'

const SNAPSHOT_PREFIX = 'xlyra:snapshot:'
const SNAPSHOT_USER_KEY = 'xlyra:snapshot-user'
const SNAPSHOT_VERSION = 1
const SNAPSHOT_MAX_AGE = 7 * 24 * 60 * 60 * 1000
const SNAPSHOT_MAX_BYTES = 256 * 1024

type QuerySnapshot = {
  v: number
  user: string
  at: number
  data: unknown
}

function currentSnapshotUserId(): string | null {
  const user = useAuthStore.getState().user?.id
  if (user) return user
  try {
    return localStorage.getItem(SNAPSHOT_USER_KEY)
  } catch {
    return null
  }
}

export function readQuerySnapshot<T>(name: string): T | undefined {
  const key = `${SNAPSHOT_PREFIX}${name}`
  try {
    const user = currentSnapshotUserId()
    if (!user) return undefined
    const stored = localStorage.getItem(key)
    if (!stored) return undefined
    const snapshot = JSON.parse(stored) as Partial<QuerySnapshot> | null
    if (
      !snapshot
      || snapshot.v !== SNAPSHOT_VERSION
      || snapshot.user !== user
      || typeof snapshot.at !== 'number'
      || !Number.isFinite(snapshot.at)
      || Date.now() - snapshot.at > SNAPSHOT_MAX_AGE
    ) {
      localStorage.removeItem(key)
      return undefined
    }
    return snapshot.data as T
  } catch {
    try {
      localStorage.removeItem(key)
    } catch { /* storage unavailable */ }
    return undefined
  }
}

export function writeQuerySnapshot(name: string, data: unknown): void {
  try {
    const user = useAuthStore.getState().user?.id
    if (!user) return
    const serialized = JSON.stringify({ v: SNAPSHOT_VERSION, user, at: Date.now(), data })
    if (new TextEncoder().encode(serialized).byteLength > SNAPSHOT_MAX_BYTES) return
    localStorage.setItem(`${SNAPSHOT_PREFIX}${name}`, serialized)
    localStorage.setItem(SNAPSHOT_USER_KEY, user)
  } catch { /* storage unavailable */ }
}

export function clearQuerySnapshots(): void {
  try {
    const keys: string[] = []
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index)
      if (key?.startsWith(SNAPSHOT_PREFIX)) keys.push(key)
    }
    for (const key of keys) localStorage.removeItem(key)
    localStorage.removeItem(SNAPSHOT_USER_KEY)
  } catch { /* storage unavailable */ }
}
