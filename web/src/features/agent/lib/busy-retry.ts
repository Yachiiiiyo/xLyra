import { APIError } from '@/lib/http'

export const BUSY_RETRY_WINDOW_MS = 3_000
export const BUSY_RETRY_DELAY_MS = 800
export const BUSY_RETRY_MAX = 4

/** 上一轮刚结束时后端还在收尾(会话互斥未释放),发送会撞「繁忙」:这段时间内的 409 自动重试 */
export function shouldRetryBusySend(error: unknown, sinceTerminalMs: number | null, attempt: number): boolean {
  if (!(error instanceof APIError) || (error.status !== 409 && error.code !== 'agent_busy')) return false
  return sinceTerminalMs !== null && sinceTerminalMs >= 0 && sinceTerminalMs < BUSY_RETRY_WINDOW_MS && attempt < BUSY_RETRY_MAX
}
