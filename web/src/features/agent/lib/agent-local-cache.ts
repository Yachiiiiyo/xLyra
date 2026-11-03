/**
 * agent 页关键 UI 状态的 localStorage 缓存：
 * - 最近会话 id：刷新后直接恢复上次会话，transcript 请求与 sessions/settings 并行发出
 */

const LAST_SESSION_KEY = 'xlyra-agent-last-session-id'
const CAPABILITY_NOTICE_DISMISSED_KEY = 'xlyra-agent-capability-notice-dismissed'

export function loadLastSessionId(): string | null {
  try {
    return window.localStorage.getItem(LAST_SESSION_KEY)
  } catch {
    return null
  }
}

export function loadCapabilityNoticeDismissed(): boolean {
  try {
    return window.localStorage.getItem(CAPABILITY_NOTICE_DISMISSED_KEY) === 'true'
  } catch {
    return false
  }
}

export function dismissCapabilityNotice() {
  try {
    window.localStorage.setItem(CAPABILITY_NOTICE_DISMISSED_KEY, 'true')
  } catch {
    // 写入失败时仅在当前页面记住关闭状态。
  }
}

export function saveLastSessionId(sessionId: string | null) {
  try {
    if (sessionId) {
      window.localStorage.setItem(LAST_SESSION_KEY, sessionId)
    } else {
      window.localStorage.removeItem(LAST_SESSION_KEY)
    }
  } catch {
    // 同上：写入失败仅意味着下次刷新不恢复会话
  }
}
