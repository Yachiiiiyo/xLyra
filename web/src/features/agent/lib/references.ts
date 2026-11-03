import type { AgentSession } from '@/features/agent/api/agent'

/** 一条消息最多引用的会话数（与后端契约一致） */
export const MAX_SESSION_REFERENCES = 3

/** 时间线 user 项 / 输入区 chip 共用的引用形态 */
export type AgentSessionReference = {
  sessionId: string
  title: string
}

const SESSION_ID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** 合法会话 ID（UUID）校验，用于「粘贴 ID 直接引用」 */
export function isSessionId(value: string): boolean {
  return SESSION_ID_PATTERN.test(value.trim())
}

export function shortSessionId(sessionId: string): string {
  return sessionId.slice(0, 8)
}

/** 引用 chip 文案：标题 · ID 前 8 位；无标题时只显示短 ID */
export function referenceChipLabel(reference: AgentSessionReference): string {
  const shortId = shortSessionId(reference.sessionId)
  return reference.title ? `${reference.title} · ${shortId}` : shortId
}

/** 引用选择列表：排除当前会话与已选中的引用 */
export function filterReferenceableSessions(sessions: AgentSession[], currentId: string | null, selectedIds: string[]): AgentSession[] {
  const excluded = new Set([...(currentId ? [currentId] : []), ...selectedIds])
  return sessions.filter((session) => !excluded.has(session.session_id))
}

/** 关键词过滤：标题 / 预览 / 会话 ID，大小写不敏感；空关键词返回全部 */
export function searchReferenceSessions(sessions: AgentSession[], query: string): AgentSession[] {
  const keyword = query.trim().toLowerCase()
  if (!keyword) return sessions
  return sessions.filter((session) =>
    session.title?.toLowerCase().includes(keyword)
    || session.preview?.toLowerCase().includes(keyword)
    || session.session_id.toLowerCase().includes(keyword))
}
