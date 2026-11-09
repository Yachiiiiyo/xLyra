import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppShell } from '@/components/layout/app-shell'
import zhComponents from '@/locales/zh/components.json'

const useHasAppBackground = vi.hoisted(() => vi.fn())

vi.mock('@/features/appearance/api/appearance', () => ({ useHasAppBackground }))

vi.mock('@/components/layout/app-background', async () => {
  const React = await import('react')
  return {
    AppBackground: ({ mainContentOverlay = false }: { mainContentOverlay?: boolean }) =>
      React.createElement('div', {
        'data-app-background': 'true',
        'data-main-content-overlay': String(mainContentOverlay),
      }),
  }
})

vi.mock('@/components/layout/app-sidebar', async () => {
  const React = await import('react')
  return {
    AppSidebar: () => React.createElement('div', { 'data-app-sidebar': 'true' }),
  }
})

vi.mock('@/components/layout/app-topbar', async () => {
  const React = await import('react')
  return {
    AppTopbar: () => React.createElement('div', { 'data-app-topbar': 'true' }),
  }
})

vi.mock('@/components/layout/system-version-text', async () => {
  const React = await import('react')
  return {
    SystemVersionText: () => React.createElement('span', null, 'dev'),
  }
})

vi.mock('@/components/layout/mobile-app-shell', async () => {
  const React = await import('react')
  return {
    MobileAppShell: ({ children }: { children?: React.ReactNode }) =>
      React.createElement('div', { 'data-mobile-shell': 'true' }, children),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { components: zhComponents } },
    interpolation: { escapeValue: false },
  })
})

beforeEach(() => {
  useHasAppBackground.mockReset()
})

function renderShell(path = '/dashboard') {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={[path]}>
        <AppShell>
          <div data-child-route="true" />
        </AppShell>
      </MemoryRouter>
    </I18nextProvider>,
  )
}

describe('AppShell 侧栏玻璃', () => {
  it('配置了背景图时主侧栏用 pane 并轻微压暗', () => {
    useHasAppBackground.mockReturnValue(true)
    const html = renderShell()
    expect(html).toContain('data-app-sidebar')
    expect(html).toContain('lg-pane')
    expect(html).toContain('lg-pane-dim')
    expect(html).not.toContain('lg-regular')
    expect(html).toContain('data-main-content-overlay="true"')
  })

  it('未配置背景图时保持 regular 不变', () => {
    useHasAppBackground.mockReturnValue(false)
    const html = renderShell()
    expect(html).toContain('data-app-sidebar')
    expect(html).toContain('lg-regular')
    expect(html).not.toContain('lg-pane')
    expect(html).not.toContain('lg-pane-dim')
  })

  it('仅让非 Agent 页面遮罩挖洞，Agent 侧栏不压暗', () => {
    useHasAppBackground.mockReturnValue(true)
    expect(renderShell('/agent')).toContain('data-main-content-overlay="false"')
    expect(renderShell('/agent')).not.toContain('lg-pane-dim')
    expect(renderShell('/dashboard')).toContain('data-main-content-overlay="true"')
  })
})
