import { QueryClient } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  agentHealthQueryOptions,
  agentModelMemoryQueryOptions,
  agentRuntimeSettingsQueryOptions,
  agentSessionsQueryOptions,
  agentWorkspaceAvailableModelsQueryOptions,
  applySessionTitleToCache,
  prefetchAgentWorkspace,
} from '@/features/agent/lib/agent-queries'

const api = vi.hoisted(() => ({
  fetchAgentAvailableModels: vi.fn(),
  fetchAgentHealth: vi.fn(),
  fetchAgentModelMemory: vi.fn(),
  fetchAgentRuntimeSettings: vi.fn(),
  listAgentSessions: vi.fn(),
}))
const readQuerySnapshot = vi.hoisted(() => vi.fn())
const writeQuerySnapshot = vi.hoisted(() => vi.fn())
const loadNamespaces = vi.hoisted(() => vi.fn())
const agentModuleLoaded = vi.hoisted(() => vi.fn())

vi.mock('@/features/agent/api/agent', () => api)
vi.mock('@/lib/query-snapshot', () => ({ readQuerySnapshot, writeQuerySnapshot }))
vi.mock('@/locales/i18n', () => ({ default: { loadNamespaces } }))
vi.mock('@/routes/agent', () => {
  agentModuleLoaded()
  return {}
})

let now = 1_000_000

beforeEach(() => {
  vi.resetAllMocks()
  now += 2001
  vi.spyOn(Date, 'now').mockImplementation(() => now)
  readQuerySnapshot.mockReturnValue(undefined)
  loadNamespaces.mockResolvedValue([])
  api.listAgentSessions.mockResolvedValue([])
  api.fetchAgentModelMemory.mockResolvedValue({ default_model: '', sessions: {} })
  api.fetchAgentRuntimeSettings.mockResolvedValue({ allowed_site_ids: [], allowed_site_model_ids: [] })
  api.fetchAgentAvailableModels.mockResolvedValue([])
  api.fetchAgentHealth.mockResolvedValue({ status: 'healthy' })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('agent query snapshots', () => {
  it('四个关键查询读取快照并立即后台刷新', () => {
    const queryClient = new QueryClient()
    const options = [
      agentSessionsQueryOptions(),
      agentModelMemoryQueryOptions(),
      agentRuntimeSettingsQueryOptions(),
      agentWorkspaceAvailableModelsQueryOptions(queryClient),
    ]

    for (const option of options) {
      expect(option.initialData()).toBeUndefined()
      expect(option.initialDataUpdatedAt).toBe(0)
    }
    expect(readQuerySnapshot.mock.calls.map(([name]) => name)).toEqual([
      'agent:sessions',
      'agent:model-memory',
      'agent:runtime',
      'agent:workspace-available-models',
    ])
    expect('initialData' in agentHealthQueryOptions()).toBe(false)
  })

  it('查询成功后写入对应快照', async () => {
    const sessions = [{ session_id: 'session-1' }]
    const memory = { default_model: 'model-1', sessions: {} }
    const runtime = { allowed_site_ids: ['site-1'], allowed_site_model_ids: ['model-1'] }
    const availableSites = [{ site_id: 'site-1' }]
    api.listAgentSessions.mockResolvedValue(sessions)
    api.fetchAgentModelMemory.mockResolvedValue(memory)
    api.fetchAgentRuntimeSettings.mockResolvedValue(runtime)
    api.fetchAgentAvailableModels.mockResolvedValue(availableSites)
    const queryClient = new QueryClient()
    const fetchQuery = vi.spyOn(queryClient, 'fetchQuery').mockResolvedValue(runtime)

    await agentSessionsQueryOptions().queryFn()
    await agentModelMemoryQueryOptions().queryFn()
    await agentRuntimeSettingsQueryOptions().queryFn()
    const available = await agentWorkspaceAvailableModelsQueryOptions(queryClient).queryFn()

    expect(fetchQuery).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['settings', 'agent', 'runtime'] }))
    expect(writeQuerySnapshot).toHaveBeenCalledWith('agent:sessions', sessions)
    expect(writeQuerySnapshot).toHaveBeenCalledWith('agent:model-memory', memory)
    expect(writeQuerySnapshot).toHaveBeenCalledWith('agent:runtime', runtime)
    expect(writeQuerySnapshot).toHaveBeenCalledWith('agent:workspace-available-models', {
      allowedSites: ['site-1'],
      allowedModels: ['model-1'],
      availableSites,
    })
    expect(available.availableSites).toBe(availableSites)
  })
})

describe('applySessionTitleToCache', () => {
  it('更新缓存中对应会话的标题,其余会话不变', () => {
    const queryClient = new QueryClient()
    const sessions = [
      { session_id: 'session-1', title: '旧标题' },
      { session_id: 'session-2', title: '另一个会话' },
    ]
    queryClient.setQueryData(['agent', 'sessions'], sessions)

    applySessionTitleToCache(queryClient, 'session-1', '新标题')

    expect(queryClient.getQueryData(['agent', 'sessions'])).toEqual([
      { session_id: 'session-1', title: '新标题' },
      { session_id: 'session-2', title: '另一个会话' },
    ])
  })

  it('会话不在缓存或缓存缺失时不动、空标题忽略', () => {
    const queryClient = new QueryClient()
    // 缓存缺失:不创建缓存条目
    applySessionTitleToCache(queryClient, 'missing', '标题')
    expect(queryClient.getQueryData(['agent', 'sessions'])).toBeUndefined()

    const sessions = [{ session_id: 'session-1', title: '旧标题' }]
    queryClient.setQueryData(['agent', 'sessions'], sessions)
    // 找不到对应会话:原样保留
    applySessionTitleToCache(queryClient, 'other', '标题')
    expect(queryClient.getQueryData(['agent', 'sessions'])).toBe(sessions)
    // 空白标题:忽略
    applySessionTitleToCache(queryClient, 'session-1', '  ')
    expect(queryClient.getQueryData(['agent', 'sessions'])).toBe(sessions)
  })
})

describe('prefetchAgentWorkspace', () => {
  it('并行发出五个查询、路由 chunk 与 i18n 预取', async () => {
    const queryClient = new QueryClient()
    const prefetchQuery = vi.spyOn(queryClient, 'prefetchQuery').mockResolvedValue(undefined)

    prefetchAgentWorkspace(queryClient)

    expect(prefetchQuery).toHaveBeenCalledTimes(5)
    expect(prefetchQuery.mock.calls.map(([options]) => options.queryKey)).toEqual([
      ['agent', 'sessions'],
      ['agent', 'model-memory'],
      ['settings', 'agent', 'runtime'],
      ['settings', 'agent', 'workspace-available-models'],
      ['settings', 'agent', 'health'],
    ])
    await vi.waitFor(() => expect(agentModuleLoaded).toHaveBeenCalledOnce())
    expect(loadNamespaces).toHaveBeenCalledWith(['agent', 'chat', 'api-keys', 'components'])
  })

  it('两秒内重复调用会去抖', () => {
    const queryClient = new QueryClient()
    const prefetchQuery = vi.spyOn(queryClient, 'prefetchQuery').mockResolvedValue(undefined)

    prefetchAgentWorkspace(queryClient)
    now += 1999
    prefetchAgentWorkspace(queryClient)

    expect(prefetchQuery).toHaveBeenCalledTimes(5)
    expect(loadNamespaces).toHaveBeenCalledOnce()

    now += 1
    prefetchAgentWorkspace(queryClient)
    expect(prefetchQuery).toHaveBeenCalledTimes(10)
    expect(loadNamespaces).toHaveBeenCalledTimes(2)
  })

  it('预取请求失败时不抛错', async () => {
    const queryClient = new QueryClient()
    vi.spyOn(queryClient, 'prefetchQuery').mockRejectedValue(new Error('network failed'))
    loadNamespaces.mockRejectedValue(new Error('i18n failed'))

    expect(() => prefetchAgentWorkspace(queryClient)).not.toThrow()
    await Promise.resolve()
  })
})
