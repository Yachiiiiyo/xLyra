import { act } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchAgentSuggestion } from '@/features/agent/api/agent'
import { usePromptSuggestion } from './use-prompt-suggestion'
import { mountMini } from '@/test/mini-dom'

vi.mock('@/features/agent/api/agent', () => ({ fetchAgentSuggestion: vi.fn() }))

type Options = Parameters<typeof usePromptSuggestion>[0]
type HookValue = ReturnType<typeof usePromptSuggestion>

function Harness({ options, onValue }: { options: Options; onValue: (value: HookValue) => void }) {
  onValue(usePromptSuggestion(options))
  return null
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

async function setup() {
  let current: HookValue | undefined
  const onAccept = vi.fn()
  const base: Options = {
    sessionId: 'session-1',
    draft: '',
    running: false,
    enabled: true,
    hasPendingInteraction: false,
    onAccept,
  }
  let options = base
  const element = () => <Harness options={options} onValue={(value) => { current = value }} />
  const mounted = await mountMini(element())
  return {
    mounted,
    onAccept,
    get current() {
      if (!current) throw new Error('hook has not rendered')
      return current
    },
    async update(patch: Partial<Options>) {
      options = { ...options, ...patch }
      await mounted.render(element())
    },
  }
}

beforeEach(() => {
  vi.mocked(fetchAgentSuggestion).mockReset()
})

describe('usePromptSuggestion', () => {
  it('满足会话、输入框、开关和待处理交互条件时请求并保存预测', async () => {
    const pending = deferred<string | null>()
    vi.mocked(fetchAgentSuggestion).mockReturnValue(pending.promise)
    const hook = await setup()
    try {
      await act(async () => { hook.current.requestSuggestion('session-1') })
      expect(fetchAgentSuggestion).toHaveBeenCalledWith('session-1', expect.any(AbortSignal))
      await act(async () => { pending.resolve('预测下一条'); await pending.promise })
      expect(hook.current.suggestion).toEqual({ sessionId: 'session-1', text: '预测下一条' })
    } finally { await hook.mounted.unmount() }
  })

  it('开关关闭、会话不匹配、已有输入或有待处理交互时不请求', async () => {
    const hook = await setup()
    try {
      await act(async () => { hook.current.requestSuggestion('session-2') })
      await hook.update({ enabled: false })
      await act(async () => { hook.current.requestSuggestion('session-1') })
      await hook.update({ enabled: true, draft: '已有输入' })
      await act(async () => { hook.current.requestSuggestion('session-1') })
      await hook.update({ draft: '', hasPendingInteraction: true })
      await act(async () => { hook.current.requestSuggestion('session-1') })
      expect(fetchAgentSuggestion).not.toHaveBeenCalled()
    } finally { await hook.mounted.unmount() }
  })

  it('新 run、切换会话、出现待处理交互或关闭开关都会清空并 abort', async () => {
    for (const patch of [
      { running: true },
      { sessionId: 'session-2' },
      { hasPendingInteraction: true },
      { enabled: false },
    ] as const) {
      const pending = deferred<string | null>()
      vi.mocked(fetchAgentSuggestion).mockReturnValueOnce(pending.promise)
      const hook = await setup()
      try {
        await act(async () => { hook.current.requestSuggestion('session-1') })
        const signal = vi.mocked(fetchAgentSuggestion).mock.calls.at(-1)?.[1]
        expect(signal?.aborted).toBe(false)
        await hook.update(patch)
        expect(signal?.aborted).toBe(true)
        expect(hook.current.suggestion).toBeNull()
      } finally { await hook.mounted.unmount() }
    }
  })

  it('请求返回时输入框已非空：预测保留，输入框之后删空仍可显示', async () => {
    const pending = deferred<string | null>()
    vi.mocked(fetchAgentSuggestion).mockReturnValue(pending.promise)
    const hook = await setup()
    try {
      await act(async () => { hook.current.requestSuggestion('session-1') })
      await hook.update({ draft: '用户已经开始输入' })
      await act(async () => { pending.resolve('仍然有效的预测'); await pending.promise })
      expect(hook.current.suggestion?.text).toBe('仍然有效的预测')
      await hook.update({ draft: '' })
      expect(hook.current.suggestion?.text).toBe('仍然有效的预测')
    } finally { await hook.mounted.unmount() }
  })

  it('显式清空会 abort，可覆盖发送消息和停止动作', async () => {
    const pending = deferred<string | null>()
    vi.mocked(fetchAgentSuggestion).mockReturnValue(pending.promise)
    const hook = await setup()
    try {
      await act(async () => { hook.current.requestSuggestion('session-1') })
      const signal = vi.mocked(fetchAgentSuggestion).mock.calls[0]?.[1]
      await act(async () => { hook.current.clearSuggestion() })
      expect(signal?.aborted).toBe(true)
      await act(async () => { pending.resolve('过期预测'); await pending.promise })
      expect(hook.current.suggestion).toBeNull()
    } finally { await hook.mounted.unmount() }
  })

  it('接受时回填文本，预测保留；输入框被删空后可重新显示，显式清空才作废', async () => {
    const pending = deferred<string | null>()
    vi.mocked(fetchAgentSuggestion).mockReturnValue(pending.promise)
    const hook = await setup()
    try {
      await act(async () => { hook.current.requestSuggestion('session-1') })
      await act(async () => { pending.resolve('接受的预测'); await pending.promise })
      await act(async () => { hook.current.acceptSuggestion() })
      expect(hook.onAccept).toHaveBeenCalledExactlyOnceWith('接受的预测')
      expect(hook.current.suggestion?.text).toBe('接受的预测')
      await hook.update({ draft: '接受的预测' })
      expect(hook.current.suggestion?.text).toBe('接受的预测')
      await hook.update({ draft: '' })
      expect(hook.current.suggestion?.text).toBe('接受的预测')
      await act(async () => { hook.current.clearSuggestion() })
      expect(hook.current.suggestion).toBeNull()
    } finally { await hook.mounted.unmount() }
  })
})
