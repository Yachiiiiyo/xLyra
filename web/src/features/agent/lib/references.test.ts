import { describe, expect, it } from 'vitest'

import type { AgentSession } from '@/features/agent/api/agent'
import {
  MAX_SESSION_REFERENCES,
  filterReferenceableSessions,
  isSessionId,
  referenceChipLabel,
  searchReferenceSessions,
  shortSessionId,
} from '@/features/agent/lib/references'

const UUID_A = '01234567-89ab-cdef-0123-456789abcdef'
const UUID_B = 'fedcba98-7654-3210-fedc-ba9876543210'
const UUID_C = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'

function session(partial: Partial<AgentSession> & { session_id: string }): AgentSession {
  return { title: '', ...partial }
}

describe('isSessionId', () => {
  it('接受合法 UUID（大小写、首尾空格）', () => {
    expect(isSessionId(UUID_A)).toBe(true)
    expect(isSessionId(UUID_A.toUpperCase())).toBe(true)
    expect(isSessionId(`  ${UUID_B} `)).toBe(true)
  })

  it('拒绝非 UUID 形态', () => {
    expect(isSessionId('')).toBe(false)
    expect(isSessionId('01234567')).toBe(false)
    expect(isSessionId('01234567-89ab-cdef-0123-456789abcde')).toBe(false)
    expect(isSessionId('0123456z-89ab-cdef-0123-456789abcdef')).toBe(false)
    expect(isSessionId('01234567_89ab_cdef_0123_456789abcdef')).toBe(false)
  })
})

describe('referenceChipLabel', () => {
  it('有标题时展示「标题 · ID 前 8 位」', () => {
    expect(referenceChipLabel({ sessionId: UUID_A, title: '调试构建' })).toBe(`调试构建 · ${UUID_A.slice(0, 8)}`)
  })

  it('无标题时只显示短 ID', () => {
    expect(referenceChipLabel({ sessionId: UUID_B, title: '' })).toBe('fedcba98')
  })
})

describe('shortSessionId', () => {
  it('取 ID 前 8 位', () => {
    expect(shortSessionId(UUID_C)).toBe('aaaaaaaa')
  })
})

describe('filterReferenceableSessions', () => {
  const sessions = [
    session({ session_id: UUID_A, title: '当前会话' }),
    session({ session_id: UUID_B, title: '已选中的会话' }),
    session({ session_id: UUID_C, title: '可选会话' }),
  ]

  it('排除当前会话与已选中的引用', () => {
    const rows = filterReferenceableSessions(sessions, UUID_A, [UUID_B])
    expect(rows.map((row) => row.session_id)).toEqual([UUID_C])
  })

  it('无排除项时返回全部', () => {
    expect(filterReferenceableSessions(sessions, null, [])).toHaveLength(3)
  })
})

describe('searchReferenceSessions', () => {
  const sessions = [
    session({ session_id: UUID_A, title: '部署问题', preview: '' }),
    session({ session_id: UUID_B, title: '', preview: '帮我看看构建日志' }),
    session({ session_id: UUID_C, title: 'Other', preview: '' }),
  ]

  it('空关键词返回全部', () => {
    expect(searchReferenceSessions(sessions, '  ')).toHaveLength(3)
  })

  it('按标题、预览、ID 前缀匹配（大小写不敏感）', () => {
    expect(searchReferenceSessions(sessions, '部署').map((row) => row.session_id)).toEqual([UUID_A])
    expect(searchReferenceSessions(sessions, '构建日志').map((row) => row.session_id)).toEqual([UUID_B])
    expect(searchReferenceSessions(sessions, 'AAAAAAAA').map((row) => row.session_id)).toEqual([UUID_C])
    expect(searchReferenceSessions(sessions, '不存在')).toHaveLength(0)
  })
})

describe('MAX_SESSION_REFERENCES', () => {
  it('与后端契约一致，最多 3 个', () => {
    expect(MAX_SESSION_REFERENCES).toBe(3)
  })
})
