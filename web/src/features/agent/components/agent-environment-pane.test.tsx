import * as React from 'react'
import { act } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentEnvironmentPane } from './agent-environment-pane'
import { fetchAgentCapabilities, fetchAgentHealth, updateAgentCapabilities } from '@/features/agent/api/agent'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

const queryState = vi.hoisted(() => ({
  health: { status: 'healthy', sandbox: 'off' } as Record<string, unknown>,
  capabilities: { sandbox_enabled: false } as Record<string, unknown>,
}))
const invalidateQueries = vi.hoisted(() => vi.fn())

vi.mock('@tanstack/react-query', () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey.at(-1) === 'health') return { data: queryState.health, isLoading: false, isFetching: false, isSuccess: true, isError: false }
    return { data: queryState.capabilities, isLoading: false, isFetching: false, isSuccess: true, isError: false }
  },
  useQueryClient: () => ({ invalidateQueries }),
  useMutation: (options: {
    mutationFn: (value: unknown) => Promise<unknown>
    onSuccess?: () => unknown
    onError?: (error: Error) => unknown
  }) => ({
    isPending: false,
    mutate: (value: unknown) => {
      void Promise.resolve()
        .then(() => options.mutationFn(value))
        .then(() => options.onSuccess?.())
        .catch((error: Error) => options.onError?.(error))
    },
  }),
}))
vi.mock('@/features/agent/api/agent', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/features/agent/api/agent')>(),
  fetchAgentHealth: vi.fn(),
  fetchAgentCapabilities: vi.fn(),
  updateAgentCapabilities: vi.fn(),
}))
vi.mock('@/components/ui/switch', () => ({
  Switch: ({ checked, onCheckedChange }: { checked?: boolean; onCheckedChange?: (checked: boolean) => void }) =>
    React.createElement('button', {
      type: 'button',
      role: 'switch',
      'aria-checked': String(checked === true),
      onClick: () => onCheckedChange?.(checked !== true),
    }),
}))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()
beforeAll(async () => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  await i18n.init({ lng: 'zh', defaultNS: 'agent', ns: ['agent', 'common'], resources: { zh: { agent: zhAgent, common: zhCommon } } })
})
beforeEach(() => {
  queryState.health = { status: 'healthy', sandbox: 'off' }
  queryState.capabilities = { sandbox_enabled: false }
  invalidateQueries.mockReset().mockResolvedValue(undefined)
  vi.mocked(fetchAgentHealth).mockReset().mockResolvedValue(null)
  vi.mocked(fetchAgentCapabilities).mockReset().mockResolvedValue(null)
  vi.mocked(updateAgentCapabilities).mockReset().mockResolvedValue(undefined)
})

function view() {
  return <I18nextProvider i18n={i18n}><AgentEnvironmentPane /></I18nextProvider>
}

async function flush() {
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

function sandboxSwitch(container: MiniNode): MiniNode {
  const node = findMiniNodes(container, (candidate) => candidate.getAttribute('role') === 'switch')[0]
  if (!node) throw new Error('找不到命令沙箱开关')
  return node
}

async function toggle(container: MiniNode) {
  await act(async () => { sandboxSwitch(container).dispatchEvent(miniEvent('click')) })
  await flush()
}

describe('AgentEnvironmentPane 命令沙箱隔离开关', () => {
  it('能力配置缺省 sandbox_enabled 时开关为关闭', async () => {
    queryState.capabilities = {}
    const mounted = await mountMini(view())
    try {
      expect(sandboxSwitch(mounted.container).getAttribute('aria-checked')).toBe('false')
    } finally { await mounted.unmount() }
  })

  it('点击开关调用 updateAgentCapabilities 并传 sandbox_enabled: true', async () => {
    const mounted = await mountMini(view())
    try {
      await toggle(mounted.container)
      expect(updateAgentCapabilities).toHaveBeenCalledExactlyOnceWith({ sandbox_enabled: true })
      expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['agent', 'capabilities'] })
    } finally { await mounted.unmount() }
  })

  it('开关为开而实际状态为 off 时提示重启后生效，状态一致时不提示', async () => {
    queryState.capabilities = { sandbox_enabled: true }
    queryState.health = { status: 'healthy', sandbox: 'off' }
    const mismatched = await mountMini(view())
    try {
      expect(mismatched.container.textContent).toContain(zhAgent.settings.environment.sandboxRestartHint)
    } finally { await mismatched.unmount() }

    queryState.health = { status: 'healthy', sandbox: 'on' }
    const matched = await mountMini(view())
    try {
      expect(matched.container.textContent).not.toContain(zhAgent.settings.environment.sandboxRestartHint)
    } finally { await matched.unmount() }
  })
})
