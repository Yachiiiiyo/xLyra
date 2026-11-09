import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteObject } from 'react-router-dom'
import { appRouter } from '@/app/router'
import { prefetchAgentWorkspace } from '@/features/agent/lib/agent-queries'
import { queryClient } from '@/lib/query-client'

vi.mock('@/features/agent/lib/agent-queries', () => ({ prefetchAgentWorkspace: vi.fn() }))
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return {
    ...actual,
    createBrowserRouter: (routes: RouteObject[]) => routes,
  }
})

beforeEach(() => {
  vi.mocked(prefetchAgentWorkspace).mockClear()
})

describe('agent route loader', () => {
  it('触发工作台预取并同步返回 null', () => {
    const routes = appRouter as unknown as RouteObject[]
    const root = routes.find((route) => route.path === '/')
    const agentRoute = root?.children?.find((route) => route.path === 'agent')
    const loader = agentRoute?.loader as (() => unknown) | undefined

    expect(loader).toBeTypeOf('function')
    const result = loader?.()

    expect(result).toBeNull()
    expect(result).not.toBeInstanceOf(Promise)
    expect(prefetchAgentWorkspace).toHaveBeenCalledOnce()
    expect(prefetchAgentWorkspace).toHaveBeenCalledWith(queryClient)
  })
})
