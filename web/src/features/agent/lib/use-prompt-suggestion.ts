import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { fetchAgentSuggestion } from '@/features/agent/api/agent'

export type PromptSuggestion = { sessionId: string; text: string }

type UsePromptSuggestionOptions = {
  sessionId: string | null
  draft: string
  running: boolean
  enabled: boolean
  hasPendingInteraction: boolean
  onAccept: (text: string) => void
}

export function usePromptSuggestion(options: UsePromptSuggestionOptions) {
  const { sessionId, running, enabled, hasPendingInteraction } = options
  const [suggestion, setSuggestion] = useState<PromptSuggestion | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const requestVersionRef = useRef(0)
  const latestRef = useRef(options)
  useLayoutEffect(() => { latestRef.current = options })

  const invalidateRequest = useCallback(() => {
    requestVersionRef.current += 1
    abortRef.current?.abort()
    abortRef.current = null
  }, [])
  const clearSuggestion = useCallback(() => {
    invalidateRequest()
    setSuggestion(null)
  }, [invalidateRequest])

  // props 变化时在渲染期间清掉旧状态；请求失效和 abort 放在 layout effect，避免在渲染期写 ref。
  const blocked = running || !enabled || hasPendingInteraction
  const [previousSessionId, setPreviousSessionId] = useState(sessionId)
  const [previousBlocked, setPreviousBlocked] = useState(blocked)
  if (previousSessionId !== sessionId) {
    setPreviousSessionId(sessionId)
    setSuggestion(null)
  }
  if (previousBlocked !== blocked) {
    setPreviousBlocked(blocked)
    if (blocked) setSuggestion(null)
  }
  useLayoutEffect(() => { invalidateRequest() }, [sessionId, invalidateRequest])
  useLayoutEffect(() => {
    if (blocked) invalidateRequest()
  }, [blocked, invalidateRequest])
  useEffect(() => () => { invalidateRequest() }, [invalidateRequest])

  const requestSuggestion = useCallback((doneSessionId: string) => {
    clearSuggestion()
    const current = latestRef.current
    if (!current.enabled || current.sessionId !== doneSessionId || current.draft !== '' || current.hasPendingInteraction) return

    const controller = new AbortController()
    const version = requestVersionRef.current
    abortRef.current = controller
    void fetchAgentSuggestion(doneSessionId, controller.signal).then((text) => {
      const latest = latestRef.current
      if (!text || controller.signal.aborted || version !== requestVersionRef.current) return
      if (latest.sessionId !== doneSessionId || latest.running || latest.hasPendingInteraction || !latest.enabled) return
      setSuggestion({ sessionId: doneSessionId, text })
    }).finally(() => {
      if (abortRef.current === controller) abortRef.current = null
    })
  }, [clearSuggestion])

  const acceptSuggestion = useCallback(() => {
    if (!suggestion) return
    // 接受后预测保留:输入框之后被删空时灰字会重新出现,发送/停止/切会话/新一轮才作废
    latestRef.current.onAccept(suggestion.text)
  }, [suggestion])

  return { suggestion, requestSuggestion, clearSuggestion, acceptSuggestion }
}
