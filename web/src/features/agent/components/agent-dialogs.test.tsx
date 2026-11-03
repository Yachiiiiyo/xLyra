import * as React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentReferenceDialog } from './agent-reference-dialog'
import { AgentSettingsDialog } from './agent-settings-dialog'
import { ScopePreviewDialog } from './agent-scope-pane'
import zhAgent from '@/locales/zh/agent.json'
import zhCommon from '@/locales/zh/common.json'

const mobileSettingsView = vi.hoisted(() => ({ current: 'menu', tab: 'scope' }))

vi.mock('react', async (importOriginal) => {
  const actual = await importOriginal<typeof React>()
  return {
    ...actual,
    useState: <T,>(initial: T | (() => T)) => actual.useState(
      mobileSettingsView.current === 'detail' && initial === 'menu'
        ? 'detail' as T
        : mobileSettingsView.current === 'detail' && initial === 'scope'
          ? mobileSettingsView.tab as T
          : initial,
    ),
  }
})
vi.mock('@radix-ui/react-dialog', async () => {
  const React = await import('react')
  const pass = ({ children, open }: { children?: React.ReactNode; open?: boolean }) => open === false ? null : React.createElement(React.Fragment, null, children)
  const element = (tag: string) => React.forwardRef(({ children, ...props }: React.HTMLAttributes<HTMLElement>, ref: React.Ref<HTMLElement>) => React.createElement(tag, { ...props, ref }, children))
  return { Root: pass, Portal: pass, Trigger: element('button'), Overlay: element('div'), Content: element('div'), Close: element('button'), Title: element('h2'), Description: element('p') }
})
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => true }))
vi.mock('@/features/agent/components/agent-scope-pane', async (importOriginal) => ({
  ...await importOriginal<typeof import('./agent-scope-pane')>(),
  AgentScopePane: () => null,
}))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    ns: ['agent', 'common'],
    resources: { zh: { agent: zhAgent, common: zhCommon } },
    interpolation: { escapeValue: false },
  })
})

function render(node: React.ReactNode) {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <I18nextProvider i18n={i18n}>{node}</I18nextProvider>
    </QueryClientProvider>,
  )
}

describe('Agent 移动端弹窗', () => {
  it('设置菜单使用唯一的标准 DialogTitle', () => {
    mobileSettingsView.current = 'menu'
    const html = render(<AgentSettingsDialog open onOpenChange={() => {}} />)
    expect(html.match(/<h2/g)).toHaveLength(1)
    expect(html).toContain(`<h2 class="text-title-2 text-label">${zhAgent.settings.title}</h2>`)
    expect(html).not.toContain('uppercase tracking-[0.18em]')
  })

  it('可用模型详情不渲染外层保存页脚', () => {
    mobileSettingsView.current = 'detail'
    mobileSettingsView.tab = 'scope'
    const html = render(<AgentSettingsDialog open onOpenChange={() => {}} />)
    expect(html.match(/<h2/g)).toHaveLength(1)
    expect(html).toContain(`<h2 class="text-title-2 text-label truncate">${zhAgent.settings.tabScope}</h2>`)
    expect(html).not.toContain('grid grid-flow-col auto-cols-fr')
    expect(html).not.toContain(zhAgent.settings.cancel)
    expect(html).not.toContain(zhAgent.settings.save)
  })

  it('没有保存动作的设置详情不渲染页脚', () => {
    mobileSettingsView.current = 'detail'
    mobileSettingsView.tab = 'environment'
    const html = render(<AgentSettingsDialog open onOpenChange={() => {}} />)
    expect(html).toContain(zhAgent.settings.tabEnvironment)
    expect(html).not.toContain('grid grid-flow-col auto-cols-fr')
    expect(html).not.toContain(zhAgent.settings.cancel)
  })

  it('模型查看二级抽屉使用返回按钮且没有页脚', () => {
    const html = render(
      <ScopePreviewDialog
        open
        onOpenChange={() => {}}
        groups={[]}
        loading={false}
        canonicalById={new Map()}
      />,
    )
    expect(html).toContain(`aria-label="${zhAgent.settings.tabScope}"`)
    expect(html).not.toContain(zhAgent.settings.close)
    expect(html).not.toContain('grid grid-flow-col auto-cols-fr')
  })

  it('引用会话描述在移动端仅保留给读屏', () => {
    const html = render(
      <AgentReferenceDialog
        open
        onOpenChange={() => {}}
        sessions={[]}
        currentId={null}
        selectedIds={[]}
        onSelect={() => {}}
      />,
    )
    expect(html).toContain('max-lg:sr-only')
    expect(html).toContain(zhAgent.reference.dialogHint)
  })
})
