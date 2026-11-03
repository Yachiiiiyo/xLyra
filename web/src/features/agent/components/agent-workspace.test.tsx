import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentMobileDrawer, AgentWorkspace } from '@/features/agent/components/agent-workspace'
import { AgentLiquidGlassPanel } from '@/features/agent/components/liquid-glass/agent-liquid-glass'
import zhAgent from '@/locales/zh/agent.json'
import zhChat from '@/locales/zh/chat.json'

const useHasAppBackground = vi.hoisted(() => vi.fn())
const useAppBackgroundImage = vi.hoisted(() => vi.fn())

vi.mock('@/features/appearance/api/appearance', () => ({ useAppBackgroundImage, useHasAppBackground }))

vi.mock('@/features/agent/api/agent', () => ({
  compactAgentSession: vi.fn(),
  createAgentSession: vi.fn(),
  deleteAgentSession: vi.fn(),
  fetchAgentAvailableModels: vi.fn(),
  fetchAgentHealth: vi.fn(),
  fetchAgentModelMemory: vi.fn(),
  fetchAgentRuntimeSettings: vi.fn(),
  fetchAgentTranscript: vi.fn(),
  followAgentEvents: vi.fn(),
  forkAgentSession: vi.fn(),
  grantAgentAccess: vi.fn(),
  listAgentSessions: vi.fn(),
  retryAgentSession: vi.fn(),
  revokeAgentAccess: vi.fn(),
  stopAgentSession: vi.fn(),
}))

vi.mock('@/features/agent/components/agent-settings-dialog', async () => {
  const React = await import('react')
  return {
    AgentSettingsDialog: () => React.createElement('div', { 'data-agent-settings-dialog': 'true' }),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    ns: ['agent', 'chat'],
    resources: { zh: { agent: zhAgent, chat: zhChat } },
    interpolation: { escapeValue: false },
  })
})

beforeEach(() => {
  useHasAppBackground.mockReset()
  useAppBackgroundImage.mockReset()
  useAppBackgroundImage.mockReturnValue('')
})

function renderWorkspace() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
  })
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <MemoryRouter initialEntries={['/agent']}>
          <AgentWorkspace />
        </MemoryRouter>
      </I18nextProvider>
    </QueryClientProvider>,
  )
}

describe('AgentWorkspace 玻璃面板', () => {
  it('配置了背景图时新建对话输入区用 WebGL 面板采样背景图（真折射）', () => {
    useHasAppBackground.mockReturnValue(true)
    useAppBackgroundImage.mockReturnValue('/api/v1/appearance/backgrounds/appearance-bg-abcd.png')
    const html = renderWorkspace()
    expect(html).toMatch(/<aside class="lg-pane [^"]*lg-pane-dim/)
    expect(html).toContain('agent-liquid-surface agent-liquid-surface--dark')
    expect(html).toContain('agent-liquid-composer agent-composer-on-background')
    expect(html).toContain('agent-liquid-surface__canvas')
    expect(html).not.toContain('lg-regular agent-composer')
    expect(html).toContain('text-label [text-shadow:0_1px_3px_rgb(0_0_0/0.6),0_2px_14px_rgb(0_0_0/0.5)]')
  })

  it('未配置背景图时侧栏与输入区保持 regular 不变', () => {
    useHasAppBackground.mockReturnValue(false)
    const html = renderWorkspace()
    expect(html).toContain('<aside class="lg-regular ')
    expect(html).toContain('lg-regular agent-composer')
    expect(html).not.toContain('agent-liquid-surface')
    expect(html).not.toContain('lg-pane')
    expect(html).not.toContain('lg-pane-dim')
    expect(html).toContain('mt-3 text-center text-xs text-faint')
  })

  it('WebGL 不可用时面板仍渲染内容（无会话 + 有背景图仍走 WebGL 面板）', () => {
    useHasAppBackground.mockReturnValue(true)
    useAppBackgroundImage.mockReturnValue('/bg.png')
    const html = renderToStaticMarkup(
      <AgentLiquidGlassPanel backgroundImage="/bg.png" variant="dark" sampleBackground={1}>
        <span>输入框</span>
      </AgentLiquidGlassPanel>,
    )
    expect(html).toContain('agent-liquid-surface__canvas')
    expect(html).toContain('输入框')
  })

  it('移动端侧栏复用 LiquidGlass，并随背景图切换压暗强度', () => {
    useHasAppBackground.mockReturnValue(true)
    const withBackground = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AgentMobileDrawer open onClose={() => {}} hasBackgroundImage>
          <div>侧栏</div>
        </AgentMobileDrawer>
      </I18nextProvider>,
    )
    expect(withBackground).toMatch(/<aside class="lg-pane [^"]*lg-pane-dim/)
    expect(withBackground).toContain('rounded-shell')
    expect(withBackground).not.toContain('lg-thick')

    const withoutBackground = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <AgentMobileDrawer open onClose={() => {}} hasBackgroundImage={false}>
          <div>侧栏</div>
        </AgentMobileDrawer>
      </I18nextProvider>,
    )
    expect(withoutBackground).toContain('<aside class="lg-regular ')
    expect(withoutBackground).not.toContain('lg-pane-dim')
  })
})
