import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { act, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter, useSearchParams } from 'react-router-dom'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { jsPluginQueryKeys } from '@/features/settings/api/js-plugins'
import {
  pluginMarketQueryKeys,
  type PluginMarketItem,
  type PluginMarketPlugin,
  type PluginMarketSnapshot,
  type PluginMarketVersion,
} from '@/features/settings/api/plugin-market'
import { OverlayModeContext } from '@/components/ui/overlay-mode'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'
import zh from '@/locales/zh/settings.json'
import { filterMarketPlugins } from './filter'
import { installControlModel } from './install-control'
import { marketInstallErrorText } from './install-error'
import { MarketInstallActions, MarketPluginDetail, MarketPluginList, MarketStatusBar, PluginMarketPanel } from './market-panel'
import { PlainText } from './plain-text'
import { installedEnableParams, resolvePluginsTab, usePluginsTab } from './tab-state'
import { updateReasonText } from './update-reason'
import { PluginTabBar } from './tabs'

const installPluginMarketPlugin = vi.hoisted(() => vi.fn())

vi.mock('@/features/settings/api/plugin-market', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/plugin-market')>()
  return { ...actual, installPluginMarketPlugin }
})

vi.mock('@/lib/toast', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock('@radix-ui/react-popover', async () => {
  const React = await import('react')
  const pass = ({ children }: { children?: React.ReactNode }) => React.createElement(React.Fragment, null, children)
  return {
    Root: pass,
    Trigger: ({ children }: { children?: React.ReactNode }) => children,
    Portal: pass,
    Content: ({ children }: { children?: React.ReactNode }) => React.createElement('div', null, children),
  }
})

vi.mock('@/components/ui/dialog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/ui/dialog')>()
  const { createElement, Fragment } = await import('react')
  const pass = (tag: string) => ({ children, ...props }: { children?: unknown }) =>
    createElement(tag, props, children as never)
  return {
    ...actual,
    Dialog: ({ open, children }: { open?: boolean; children?: unknown }) =>
      (open ? createElement(Fragment, null, children as never) : null),
    DialogContent: pass('div'),
    DialogTitle: pass('h2'),
    DialogDescription: pass('p'),
  }
})

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { settings: zh } },
    interpolation: { escapeValue: false },
  })
})

beforeEach(() => {
  installPluginMarketPlugin.mockReset()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

const freshSnapshot: PluginMarketSnapshot = {
  index_expired: false,
  revocations_expired: false,
  last_error: '',
  source_url: '',
  root_keys_configured: true,
  enabled: true,
}

function marketVersion(version: string, extra: Partial<PluginMarketVersion> = {}): PluginMarketVersion {
  return {
    version,
    published_at: '2026-10-01T00:00:00Z',
    changelog: '',
    size: 1,
    compat: { xlyra: '', api_version: 1, host_api: 1 },
    hosts: [],
    permissions: [],
    compatible: true,
    revocation: { revoked: false },
    ...extra,
  }
}

function renderDetail(plugin: PluginMarketPlugin) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <MarketPluginDetail plugin={plugin} />
    </I18nextProvider>,
  )
}

function renderActions(plugin: PluginMarketPlugin, snapshot?: PluginMarketSnapshot) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return renderToStaticMarkup(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <I18nextProvider i18n={i18n}>
          <MarketInstallActions plugin={plugin} snapshot={snapshot} />
        </I18nextProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  )
}

function translate(key: string, options?: { version?: string }) {
  return i18n.t(key, { ns: 'settings', ...options })
}

function renderList(items: PluginMarketItem[], mobile = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return renderToStaticMarkup(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <I18nextProvider i18n={i18n}>
          <MarketPluginList plugins={items} mobile={mobile} onOpen={() => {}} />
        </I18nextProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  )
}

const plugins: PluginMarketItem[] = [
  {
    id: 'acme.one',
    name: 'One',
    summary: 'quota tool',
    kind: 'quota_probe',
    developer: { namespace: 'acme', display_name: 'Acme' },
    has_compatible_version: true,
    latest_compatible_version: '1.2.0',
    local_status: 'enabled',
    local_version: '1.0.0',
    update_available: true,
    has_revoked_version: false,
  },
  {
    id: 'acme.two',
    name: 'Two',
    summary: 'speaks protocol',
    kind: 'protocol',
    developer: { namespace: 'acme', display_name: 'Acme' },
    has_compatible_version: true,
    latest_compatible_version: '2.0.0',
    local_status: 'not_installed',
    update_available: false,
    has_revoked_version: false,
  },
  {
    id: 'acme.old',
    name: 'Old',
    summary: 'broken quota',
    kind: 'quota_probe',
    developer: { namespace: 'acme', display_name: 'Acme' },
    has_compatible_version: false,
    incompatible_reason: 'xlyra_version',
    local_status: 'not_installed',
    update_available: false,
    has_revoked_version: false,
  },
]

describe('filterMarketPlugins', () => {
  it('filters by name, id, and summary', () => {
    expect(filterMarketPlugins(plugins, { query: 'one', kind: 'all', showIncompatible: false }).map((item) => item.id)).toEqual(['acme.one'])
    expect(filterMarketPlugins(plugins, { query: 'ACME.TWO', kind: '', showIncompatible: false }).map((item) => item.id)).toEqual(['acme.two'])
    expect(filterMarketPlugins(plugins, { query: 'protocol', kind: 'all', showIncompatible: true }).map((item) => item.id)).toEqual(['acme.two'])
  })

  it('filters by kind and hides incompatible plugins until asked', () => {
    expect(filterMarketPlugins(plugins, { query: '', kind: 'quota_probe', showIncompatible: false }).map((item) => item.id)).toEqual(['acme.one'])
    expect(filterMarketPlugins(plugins, { query: '', kind: 'quota_probe', showIncompatible: true }).map((item) => item.id)).toEqual(['acme.one', 'acme.old'])
    expect(filterMarketPlugins(plugins, { query: '', kind: 'protocol', showIncompatible: true }).map((item) => item.id)).toEqual(['acme.two'])
  })
})

describe('plugin market tabs', () => {
  it('resolves the market tab from the query', () => {
    expect(resolvePluginsTab('market')).toBe('market')
    expect(resolvePluginsTab('installed')).toBe('installed')
    expect(resolvePluginsTab(null)).toBe('installed')
  })

  it('drops the market tab and keeps other params when opening enable', () => {
    const next = installedEnableParams(new URLSearchParams('tab=market&q=keep'), 'acme.two', '2.0.0')
    expect(next.get('tab')).toBeNull()
    expect(next.get('q')).toBe('keep')
    expect(next.get('enable')).toBe('acme.two@2.0.0')
  })

  it('renders both tabs', () => {
    const markup = renderToStaticMarkup(
      <PluginTabBar tab="installed" installedLabel="已安装" marketLabel="市场" onTab={() => {}} />,
    )
    expect(markup).toContain('已安装')
    expect(markup).toContain('市场')
  })

  it('selects the market tab from ?tab=market', () => {
    function Probe() {
      const { tab } = usePluginsTab()
      return (
        <PluginTabBar tab={tab} installedLabel="已安装" marketLabel="市场" onTab={() => {}} />
      )
    }
    const open = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/system/plugins?tab=market']}>
        <Probe />
      </MemoryRouter>,
    )
    expect(open).toContain('市场')
    expect(open).toMatch(/aria-selected="true"[^>]*>市场/)
    const installed = renderToStaticMarkup(
      <MemoryRouter initialEntries={['/system/plugins']}>
        <Probe />
      </MemoryRouter>,
    )
    expect(installed).toContain('市场')
    expect(installed).toMatch(/aria-selected="true"[^>]*>已安装/)
  })
})

describe('market plain text', () => {
  it('renders changelog markup as text and keeps line breaks', () => {
    const markup = renderToStaticMarkup(<PlainText text={'<b>x</b>\nnext'} preserveNewlines />)
    expect(markup).toContain('&lt;b&gt;x&lt;/b&gt;')
    expect(markup).not.toContain('<b>')
    expect(markup).toContain('whitespace-pre-wrap')
    expect(markup).toContain('\n')
  })

  it('renders a plugin changelog as text inside the detail', () => {
    const plugin: PluginMarketPlugin = {
      ...plugins[0],
      homepage: 'https://acme.example',
      versions: [
        {
          version: '1.2.0',
          published_at: '2026-10-01T00:00:00Z',
          changelog: '<b>x</b>',
          size: 4,
          compat: { xlyra: '', api_version: 1, host_api: 1 },
          hosts: [],
          permissions: [],
          compatible: true,
          revocation: { revoked: false },
        },
      ],
    }
    const markup = renderDetail(plugin)
    expect(markup).toContain('&lt;b&gt;x&lt;/b&gt;')
    expect(markup).not.toContain('<b>')
    expect(markup).toContain('无')
    expect(markup).toContain('rel="noopener noreferrer"')
    expect(markup).not.toContain('>更新<')
    expect(markup).not.toContain('>更新并启用<')
    expect(markup).not.toContain('安装功能即将推出')
  })

  it('shows an automatic-update block reason and a security revocation', () => {
    const text = updateReasonText(
      (key, options) => i18n.t(key, { ns: 'settings', ...options }),
      'hosts_added',
      'api2.foo.com',
    )
    expect(text).toBe('新版本新增主机 api2.foo.com，需要手动确认')
    const plugin: PluginMarketPlugin = {
      ...plugins[0],
      update_version: '1.2.0',
      auto_block_reason: 'hosts_added',
      auto_block_detail: 'api2.foo.com',
      versions: [marketVersion('1.2.0', { revocation: { revoked: true, level: 'security', reason: 'leaks headers' } })],
    }
    const detail = renderDetail(plugin)
    expect(detail).toContain('新版本新增主机 api2.foo.com，需要手动确认')
    expect(detail).toContain('已吊销')
    expect(detail).toContain('leaks headers')
    const list = renderList([plugin])
    expect(list).toContain('可更新 1.2.0')
    expect(list).toContain('api2.foo.com')
  })
})

describe('market status and filters', () => {
  it('hides source URLs and keeps search, kind, and incompatible controls', () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    client.setQueryData(pluginMarketQueryKeys.list(), {
      plugins,
      snapshot: {
        index_expired: false,
        revocations_expired: false,
        index_synced_at: '2026-10-01T00:00:00Z',
        last_error: 'Get "https://x.example/v1/root.json": EOF',
        source_url: 'https://market.example/v1',
        root_keys_configured: true,
        enabled: true,
      },
    })
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>
            <PluginMarketPanel />
          </I18nextProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    )
    expect(markup).not.toContain('市场源')
    expect(markup).not.toContain('https://')
    expect(markup).toContain('Get &quot;…&quot;: EOF')
    expect(markup).toContain('搜索名称、ID 或说明')
    expect(markup).toContain('aria-label="类型"')
    expect(markup).toContain('显示不兼容')
    expect(markup).toContain('whitespace-nowrap')
    expect(markup).toContain('One')
    expect(markup).not.toContain('>Old<')
  })

  it('renders a sanitized error on the status bar', () => {
    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <MarketStatusBar
          snapshot={{
            index_expired: false,
            revocations_expired: false,
            last_error: 'Get "https://x.example/v1/root.json": EOF',
            source_url: 'https://market.example/v1',
            root_keys_configured: true,
            enabled: true,
          }}
          language="zh"
          syncing={false}
          onSync={() => {}}
        />
      </I18nextProvider>,
    )
    expect(markup).not.toContain('https://')
    expect(markup).not.toContain('市场源')
    expect(markup).toContain('立即同步')
    expect(markup).toContain('Get &quot;…&quot;: EOF')
  })
})

describe('MarketPluginList', () => {
  it('shows the plugins the filter kept', () => {
    const visible = filterMarketPlugins(plugins, { query: 'quota', kind: 'all', showIncompatible: false })
    const markup = renderList(visible)
    expect(markup).toContain('One')
    expect(markup).not.toContain('Old')
    expect(markup).not.toContain('Two')
    expect(markup).toContain('>O</span>')
    expect(markup).not.toContain('<img')
    expect(markup).not.toContain('role="button"')
    expect(markup).not.toContain('cursor-pointer')
    expect(markup).toContain('查看详情')
    expect(markup).toContain('Acme')
    expect(markup).toContain('leading-5')
    expect(markup).toContain('leading-4')
    expect(markup).toContain('justify-between')
    expect(markup).not.toContain('命名空间')
  })

  it('renders the market icon and falls back to the initial', () => {
    const iconUrl = '/api/v1/plugin-market/icons/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    const markup = renderList([
      { ...plugins[0], icon_url: iconUrl },
      plugins[1],
    ])
    expect(markup).toContain(`src="${iconUrl}"`)
    expect(markup).toContain('alt=""')
    expect(markup).toContain('h-10 w-10')
    expect(markup).toContain('>T</span>')
    expect(markup).not.toContain('>O</span>')
    expect(markup).toContain('>安装<')
    expect(markup).toContain('aria-label="更多安装选项"')
  })

  it('keeps view and install on one row and splits them on a narrow card', () => {
    const markup = renderList([plugins[1]], true)
    expect(markup).toContain('grid-cols-2')
    expect(markup).toContain('gap-2')
    expect(markup).not.toContain('grid-cols-1')
    expect(markup).toContain('查看详情')
    expect(markup).toContain('>安装<')
  })

  it('shows a disabled installed label when the latest build is already enabled', () => {
    const markup = renderList([{
      ...plugins[1],
      local_status: 'enabled',
      local_version: '2.0.0',
      update_available: false,
    }])
    expect(markup).toContain('>已安装<')
    expect(markup).toContain('disabled=""')
    expect(markup).not.toContain('role="button"')
    expect(markup).not.toContain('aria-label="更多安装选项"')
  })
})

describe('MarketPluginDetail icon', () => {
  it('marks the installed version with a badge in the version list', () => {
    const markup = renderDetail({
      ...plugins[1],
      local_status: 'installed',
      local_version: '2.0.0',
      versions: [marketVersion('2.0.0'), marketVersion('1.0.0')],
    })
    expect(markup.match(/已安装/g)).toHaveLength(1)
    expect(markup.indexOf('2.0.0')).toBeLessThan(markup.indexOf('已安装'))
  })
  it('leaves the icon out of the detail body', () => {
    const iconUrl = '/api/v1/plugin-market/icons/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    const markup = renderDetail({ ...plugins[0], icon_url: iconUrl, versions: [marketVersion('1.2.0')] })
    expect(markup).not.toContain(iconUrl)
    expect(markup).not.toContain('<img')
    expect(markup).not.toContain('h-12 w-12')
    expect(markup).toContain('quota tool')
    expect(markup).toContain('作者：Acme')
    expect(markup).not.toContain('命名空间')
  })

  it('does not fall back to an avatar initial in the detail body', () => {
    const markup = renderDetail({ ...plugins[1], versions: [marketVersion('2.0.0')] })
    expect(markup).not.toContain('>T</span>')
    expect(markup).not.toContain('h-12 w-12')
    expect(markup).not.toContain('<img')
    expect(markup).toContain('作者：Acme')
    expect(markup).not.toContain('命名空间')
  })
})

describe('installControlModel', () => {
  it('offers install, and install-and-enable, when the plugin is not installed', () => {
    const model = installControlModel({ ...plugins[1], versions: [marketVersion('2.0.0')] }, freshSnapshot, translate)
    expect(model.mode).toBe('split')
    expect(model.primaryLabel).toBe('安装')
    expect(model.menuItem).toEqual({ key: 'install-enable', label: '安装并启用' })
    expect(model.disabled).toBe(false)
    expect(model.blocked).toBe(false)
    expect(model.reason).toBeNull()
  })

  it('offers update, and update-and-enable, when an update is available', () => {
    const model = installControlModel({ ...plugins[0], versions: [marketVersion('1.2.0')] }, freshSnapshot, translate)
    expect(model.mode).toBe('split')
    expect(model.primaryLabel).toBe('更新')
    expect(model.menuItem).toEqual({ key: 'update-enable', label: '更新并启用' })
    expect(model.disabled).toBe(false)
  })

  it('offers enable when the latest build is installed but not enabled', () => {
    const model = installControlModel({
      ...plugins[1],
      local_status: 'installed',
      local_version: '2.0.0',
      versions: [marketVersion('2.0.0')],
    }, freshSnapshot, translate)
    expect(model.mode).toBe('enable')
    expect(model.primaryLabel).toBe('启用')
    expect(model.menuItem).toBeNull()
    expect(model.disabled).toBe(false)
  })

  it('has no action when the latest build is already enabled', () => {
    const model = installControlModel({
      ...plugins[1],
      local_status: 'enabled',
      local_version: '2.0.0',
      update_available: false,
      versions: [marketVersion('2.0.0')],
    }, freshSnapshot, translate)
    expect(model.mode).toBe('none')
    expect(model.primaryLabel).toBe('已安装')
    expect(model.menuItem).toBeNull()
    expect(model.disabled).toBe(true)
    expect(model.reason).toBeNull()
  })

  it('disables install while the revocation list is expired', () => {
    const model = installControlModel(
      { ...plugins[1], versions: [marketVersion('2.0.0')] },
      { ...freshSnapshot, revocations_expired: true },
      translate,
    )
    expect(model.mode).toBe('split')
    expect(model.primaryLabel).toBe('安装')
    expect(model.menuItem?.label).toBe('安装并启用')
    expect(model.disabled).toBe(true)
    expect(model.blocked).toBe(true)
    expect(model.reason).toBe('已很久未同步，暂停从市场安装')
  })

  it('disables install and keeps the incompatible reason', () => {
    const model = installControlModel({
      ...plugins[2],
      versions: [marketVersion('0.1.0', { compatible: false, incompatible_reason: 'xlyra_version' })],
    }, freshSnapshot, translate)
    expect(model.disabled).toBe(true)
    expect(model.reason).toBe('xLyra 版本范围不满足')
    expect(model.primaryLabel).toBe('安装')
  })

  it('disables install when the latest compatible version is security-revoked', () => {
    const model = installControlModel({
      ...plugins[1],
      versions: [marketVersion('2.0.0', { revocation: { revoked: true, level: 'security', reason: 'bad' } })],
    }, freshSnapshot, translate)
    expect(model.disabled).toBe(true)
    expect(model.blocked).toBe(true)
    expect(model.reason).toBe('该版本已被吊销')
    expect(model.menuItem?.label).toBe('安装并启用')
  })
})

describe('market install buttons', () => {
  it('shows install actions when the plugin is not installed', () => {
    const markup = renderActions({ ...plugins[1], versions: [marketVersion('2.0.0')] }, freshSnapshot)
    expect(markup).toContain('>安装<')
    expect(markup).toContain('>安装并启用<')
    expect(markup).not.toContain('disabled=""')
  })

  it('shows update actions when an update is available', () => {
    const markup = renderActions({ ...plugins[0], versions: [marketVersion('1.2.0')] }, freshSnapshot)
    expect(markup).toContain('>更新<')
    expect(markup).toContain('>更新并启用<')
    expect(markup).not.toContain('>安装<')
  })

  it('shows an enable button when the latest build is installed but not enabled', () => {
    const markup = renderActions({
      ...plugins[1],
      local_status: 'installed',
      local_version: '2.0.0',
      versions: [marketVersion('2.0.0')],
    }, freshSnapshot)
    expect(markup).toContain('>启用<')
    expect(markup).not.toContain('>安装<')
    expect(markup).not.toContain('disabled=""')
  })

  it('shows no install actions when that version is already enabled', () => {
    const markup = renderActions({
      ...plugins[1],
      local_status: 'enabled',
      local_version: '2.0.0',
      update_available: false,
      versions: [marketVersion('2.0.0')],
    }, freshSnapshot)
    expect(markup).not.toContain('>启用<')
    expect(markup).not.toContain('>安装<')
    expect(markup).not.toContain('>更新<')
  })

  it('disables install while the revocation list is expired', () => {
    const markup = renderActions(
      { ...plugins[1], versions: [marketVersion('2.0.0')] },
      { ...freshSnapshot, revocations_expired: true },
    )
    expect(markup).toContain('>安装<')
    expect(markup).toContain('>安装并启用<')
    expect(markup).toContain('disabled=""')
    expect(markup).toContain('已很久未同步，暂停从市场安装')
    expect(markup).toContain('text-label-3')
  })

  it('disables install and shows the incompatible reason', () => {
    const markup = renderActions({ ...plugins[2], versions: [marketVersion('0.1.0', { compatible: false, incompatible_reason: 'xlyra_version' })] }, freshSnapshot)
    expect(markup).toContain('disabled=""')
    expect(markup).toContain('xLyra 版本范围不满足')
    expect(markup).toContain('text-label-3')
  })

  it('disables install when the latest compatible version is security-revoked', () => {
    const markup = renderActions({
      ...plugins[1],
      versions: [marketVersion('2.0.0', { revocation: { revoked: true, level: 'security', reason: 'bad' } })],
    }, freshSnapshot)
    expect(markup).toContain('disabled=""')
    expect(markup).toContain('text-label-3">该版本已被吊销')
  })

  it('puts the hint on its own row and splits close and install in a draw', () => {
    const markup = renderToStaticMarkup(
      <OverlayModeContext.Provider value="draw">
        <MemoryRouter>
          <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
            <I18nextProvider i18n={i18n}>
              <MarketInstallActions
                plugin={{ ...plugins[1], versions: [marketVersion('2.0.0')] }}
                snapshot={{ ...freshSnapshot, revocations_expired: true }}
                onClose={() => {}}
              />
            </I18nextProvider>
          </QueryClientProvider>
        </MemoryRouter>
      </OverlayModeContext.Provider>,
    )
    expect(markup).toContain('flex-col')
    expect(markup).toContain('flex-1')
    expect(markup.indexOf('已很久未同步，暂停从市场安装')).toBeLessThan(markup.indexOf('>关闭<'))
    expect(markup.indexOf('>关闭<')).toBeLessThan(markup.indexOf('>安装<'))
  })
})

describe('market install errors', () => {
  it.each([
    ['js_plugin_market_incompatible', '此版本与当前 xLyra 不兼容'],
    ['js_plugin_market_revoked', '该版本已被吊销'],
    ['js_plugin_market_revocations_expired', '已很久未同步，暂停从市场安装，请先立即同步'],
    ['js_plugin_market_developer_mismatch', '本地已有同名插件，且签名者与市场不一致，已拒绝安装'],
    ['js_plugin_market_download_failed', '下载失败，请稍后重试'],
    ['js_plugin_market_verify_failed', '校验未通过（包与上架证明不一致）'],
    ['js_plugin_market_unsigned', '插件包未签名'],
    ['js_plugin_market_selftest_failed', '插件自检未通过'],
    ['js_plugin_package_invalid', '插件包无效'],
    ['js_plugin_version_conflict', '已存在同版本但内容不同的插件'],
    ['js_plugin_market_not_found', '市场里找不到该插件或版本'],
    ['js_plugin_market_version_not_found', '市场里找不到该插件或版本'],
    ['js_plugin_market_unavailable', '市场暂不可用'],
  ])('maps %s', (code, message) => {
    const text = marketInstallErrorText(translate, new APIError(`see https://market.example/${code}`, 400, code))
    expect(text).toBe(message)
    expect(text).not.toContain('https://')
  })

  it('sanitizes an unmapped error and drops URLs', () => {
    const text = marketInstallErrorText(
      translate,
      new APIError('Get "https://market.example/pkg": EOF', 500, 'js_plugin_market_install_failed'),
    )
    expect(text).toBe('Get &quot;…&quot;: EOF'.replaceAll('&quot;', '"'))
    expect(text).not.toContain('https://')
  })
})

type MiniListener = (event: MiniEvent) => void

const miniListeners = new WeakMap<object, Map<string, Set<MiniListener>>>()

function trackListener(target: object, type: string, listener: MiniListener) {
  const byType = miniListeners.get(target) ?? new Map<string, Set<MiniListener>>()
  const set = byType.get(type) ?? new Set<MiniListener>()
  set.add(listener)
  byType.set(type, set)
  miniListeners.set(target, byType)
}

interface MiniEvent {
  type: string
  target: MiniNode | null
  currentTarget: MiniNode | MiniDocument | null
  bubbles: boolean
  cancelable: boolean
  defaultPrevented: boolean
  button: number
  eventPhase: number
  timeStamp: number
  preventDefault: () => void
  stopPropagation: () => void
  stopped: boolean
}

class MiniNode {
  nodeType = 1
  nodeName: string
  tagName: string
  namespaceURI: string | null = 'http://www.w3.org/1999/xhtml'
  ownerDocument: MiniDocument
  parentNode: MiniNode | null = null
  childNodes: MiniNode[] = []
  nodeValue: string | null = null
  attributes: Record<string, string> = {}
  options: MiniNode[] = []
  style: { setProperty: (name: string, value: string) => void } & Record<string, string>

  constructor(tag: string, ownerDocument: MiniDocument) {
    this.tagName = tag.toUpperCase()
    this.nodeName = this.tagName
    this.ownerDocument = ownerDocument
    const values: Record<string, string> = {}
    this.style = new Proxy(values, {
      get: (target, prop) => (prop === 'setProperty'
        ? (name: string, value: string) => { target[name] = value }
        : target[String(prop)]),
      set: (target, prop, value) => {
        target[String(prop)] = String(value)
        return true
      },
    }) as MiniNode['style']
  }

  get firstChild(): MiniNode | null {
    return this.childNodes[0] ?? null
  }

  get lastChild(): MiniNode | null {
    return this.childNodes[this.childNodes.length - 1] ?? null
  }

  get nextSibling(): MiniNode | null {
    if (!this.parentNode) return null
    const index = this.parentNode.childNodes.indexOf(this)
    return this.parentNode.childNodes[index + 1] ?? null
  }

  get textContent() {
    if (this.nodeType === 3) return this.nodeValue ?? ''
    return this.childNodes.map((child) => child.textContent).join('')
  }

  set textContent(value: string) {
    this.childNodes = []
    if (value) this.appendChild(this.ownerDocument.createTextNode(value))
  }

  set className(value: string) {
    this.attributes.class = String(value)
  }

  get className() {
    return this.attributes.class ?? ''
  }

  setAttribute(name: string, value: string) {
    this.attributes[name] = String(value)
  }

  getAttribute(name: string) {
    return this.attributes[name] ?? null
  }

  removeAttribute(name: string) {
    delete this.attributes[name]
  }

  hasAttribute(name: string) {
    return Object.prototype.hasOwnProperty.call(this.attributes, name)
  }

  setAttributeNS(_namespace: string, name: string, value: string) {
    this.setAttribute(name, value)
  }

  closest(selector: string) {
    return closestTag(this, selector)
  }

  appendChild<T extends MiniNode>(child: T) {
    return this.insertBefore(child, null)
  }

  insertBefore<T extends MiniNode>(child: T, before: MiniNode | null) {
    if (child.parentNode) child.parentNode.removeChild(child)
    child.parentNode = this
    if (!before) this.childNodes.push(child)
    else {
      const index = this.childNodes.indexOf(before)
      if (index < 0) throw new Error('insertBefore target is not a child')
      this.childNodes.splice(index, 0, child)
    }
    return child
  }

  removeChild<T extends MiniNode>(child: T) {
    const index = this.childNodes.indexOf(child)
    if (index < 0) throw new Error('removeChild target is not a child')
    this.childNodes.splice(index, 1)
    child.parentNode = null
    return child
  }

  addEventListener(type: string, listener: MiniListener, options?: boolean | { capture?: boolean }) {
    const capture = options === true || (typeof options === 'object' && options.capture === true)
    trackListener(this, `${type}:${capture ? 'capture' : 'bubble'}`, listener)
  }

  removeEventListener(type: string, listener: MiniListener, options?: boolean | { capture?: boolean }) {
    const capture = options === true || (typeof options === 'object' && options.capture === true)
    miniListeners.get(this)?.get(`${type}:${capture ? 'capture' : 'bubble'}`)?.delete(listener)
  }

  dispatchEvent(event: MiniEvent) {
    event.target = this
    const chain: MiniNode[] = [this]
    let parent = this.parentNode
    while (parent) {
      chain.push(parent)
      parent = parent.parentNode
    }
    for (const current of chain) {
      event.currentTarget = current
      for (const listener of miniListeners.get(current)?.get(`${event.type}:bubble`) ?? []) listener.call(current, event)
      if (event.stopped) break
    }
    return true
  }
}

class MiniDocument {
  nodeType = 9
  nodeName = '#document'
  documentElement: MiniNode
  body: MiniNode
  defaultView = globalThis

  constructor() {
    this.documentElement = new MiniNode('html', this)
    this.body = new MiniNode('body', this)
    this.documentElement.appendChild(this.body)
  }

  createElement(tag: string) {
    const node = new MiniNode(tag, this)
    if (tag === '#text') {
      node.nodeType = 3
      node.nodeName = '#text'
      node.tagName = '#text'
    }
    return node
  }

  createElementNS(_namespace: string, tag: string) {
    return this.createElement(tag)
  }

  createTextNode(text: string) {
    const node = this.createElement('#text')
    node.nodeValue = text
    return node
  }

  createComment(text: string) {
    const node = this.createElement('#comment')
    node.nodeType = 8
    node.nodeValue = text
    return node
  }

  addEventListener() {}

  removeEventListener() {}
}

function closestTag(node: MiniNode | null, selector: string) {
  const tag = selector.toUpperCase()
  let current = node
  while (current) {
    if (current.tagName === tag) return current
    current = current.parentNode
  }
  return null
}

function clickEvent(target: MiniNode): MiniEvent {
  return {
    type: 'click',
    target,
    currentTarget: target,
    bubbles: true,
    cancelable: true,
    defaultPrevented: false,
    button: 0,
    eventPhase: 2,
    timeStamp: 0,
    stopped: false,
    preventDefault() { this.defaultPrevented = true },
    stopPropagation() { this.stopped = true },
  }
}

function findButton(node: MiniNode, label: string): MiniNode | null {
  if (node.tagName === 'BUTTON' && node.textContent.trim() === label) return node
  for (const child of node.childNodes) {
    const found = findButton(child, label)
    if (found) return found
  }
  return null
}

function findRole(node: MiniNode, role: string, label: string): MiniNode | null {
  if (node.getAttribute('role') === role && node.textContent.includes(label)) return node
  for (const child of node.childNodes) {
    const found = findRole(child, role, label)
    if (found) return found
  }
  return null
}

function findTestId(node: MiniNode, testId: string): MiniNode | null {
  if (node.getAttribute?.('data-testid') === testId) return node
  for (const child of node.childNodes) {
    const found = findTestId(child, testId)
    if (found) return found
  }
  return null
}

function findTag(node: MiniNode, tag: string): MiniNode | null {
  if (node.tagName === tag) return node
  for (const child of node.childNodes) {
    const found = findTag(child, tag)
    if (found) return found
  }
  return null
}

function EnableSearch() {
  const [params] = useSearchParams()
  return <span>{`search:${params.toString()}`}</span>
}

async function mountMarket(node: ReactNode) {
  const doc = new MiniDocument()
  const container = doc.createElement('div')
  doc.body.appendChild(container)
  const globals = globalThis as unknown as {
    window: unknown
    document: unknown
    HTMLIFrameElement?: unknown
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  const previous = {
    window: globals.window,
    document: globals.document,
    HTMLIFrameElement: globals.HTMLIFrameElement,
  }
  class FrameElement {}
  globals.window = globalThis
  globals.document = doc
  globals.HTMLIFrameElement = FrameElement
  globals.IS_REACT_ACT_ENVIRONMENT = true
  const restore = () => {
    globals.window = previous.window
    globals.document = previous.document
    globals.HTMLIFrameElement = previous.HTMLIFrameElement
  }
  try {
    const { createRoot } = await import('react-dom/client')
    const root = createRoot(container as unknown as Element)
    await act(async () => {
      root.render(node)
    })
    return {
      container,
      async unmount() {
        await act(async () => {
          root.unmount()
        })
        restore()
      },
    }
  } catch (error) {
    restore()
    throw error
  }
}

const installedRow = {
  plugin_id: 'acme.two',
  version: '2.0.0',
  status: 'verified',
  trust: 'marketplace',
  origin: 'market',
}

describe('market install clicks', () => {
  const plugin: PluginMarketPlugin = { ...plugins[1], versions: [marketVersion('2.0.0')] }

  async function mount(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
    return mountMarket(
      <MemoryRouter initialEntries={['/system/plugins?tab=market&q=keep']}>
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>
            <MarketInstallActions plugin={plugin} snapshot={freshSnapshot} />
            <EnableSearch />
          </I18nextProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    )
  }

  it('calls install and shows the stay-on-market toast', async () => {
    installPluginMarketPlugin.mockResolvedValue(installedRow)
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const mounted = await mount(client)
    try {
      const button = findButton(mounted.container, '安装')
      expect(button).not.toBeNull()
      await act(async () => {
        button?.dispatchEvent(clickEvent(button))
      })
      expect(installPluginMarketPlugin).toHaveBeenCalledWith('acme.two', '2.0.0')
      expect(toast.success).toHaveBeenCalledWith('已安装，可在「已安装」中启用')
      expect(invalidate).toHaveBeenCalledWith({ queryKey: pluginMarketQueryKeys.list() })
      expect(invalidate).toHaveBeenCalledWith({ queryKey: pluginMarketQueryKeys.detail('acme.two') })
      expect(invalidate).toHaveBeenCalledWith({ queryKey: jsPluginQueryKeys.all })
      expect(mounted.container.textContent).toContain('search:tab=market')
      expect(mounted.container.textContent).not.toContain('enable=')
    } finally {
      await mounted.unmount()
    }
  })

  it('opens the installed tab with enable= and an empty tab param', async () => {
    installPluginMarketPlugin.mockResolvedValue(installedRow)
    const mounted = await mount()
    try {
      const button = findButton(mounted.container, '安装并启用')
      expect(button).not.toBeNull()
      await act(async () => {
        button?.dispatchEvent(clickEvent(button))
      })
      expect(installPluginMarketPlugin).toHaveBeenCalledWith('acme.two', '2.0.0')
      const search = mounted.container.textContent ?? ''
      expect(search).toContain('search:')
      expect(search).not.toMatch(/search:[^]*tab=/)
      expect(search).toContain('enable=acme.two%402.0.0')
      expect(search).toContain('q=keep')
    } finally {
      await mounted.unmount()
    }
  })

  it('toasts a mapped error without a URL', async () => {
    installPluginMarketPlugin.mockRejectedValue(new APIError('https://market.example/pkg failed', 409, 'js_plugin_market_revoked'))
    const mounted = await mount()
    try {
      const button = findButton(mounted.container, '安装')
      await act(async () => {
        button?.dispatchEvent(clickEvent(button!))
      })
      expect(toast.error).toHaveBeenCalledWith('该版本已被吊销')
      const message = vi.mocked(toast.error).mock.calls.map((call) => String(call[0])).join('\n')
      expect(message).not.toContain('https://')
    } finally {
      await mounted.unmount()
    }
  })
})

describe('market detail footer', () => {
  it('puts the install button in the dialog footer', async () => {
    const iconUrl = '/api/v1/plugin-market/icons/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
    const plugin: PluginMarketPlugin = { ...plugins[1], icon_url: iconUrl, versions: [marketVersion('2.0.0')] }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
    client.setQueryData(pluginMarketQueryKeys.list(), { plugins: [plugin], snapshot: freshSnapshot })
    client.setQueryData(pluginMarketQueryKeys.detail(plugin.id), plugin)
    const globals = globalThis as unknown as {
      matchMedia?: (query: string) => { matches: boolean; addEventListener: () => void; removeEventListener: () => void }
      HTMLSelectElement?: { prototype: object }
    }
    globals.matchMedia = () => ({
      matches: false,
      addEventListener() {},
      removeEventListener() {},
    })
    globals.HTMLSelectElement = class HTMLSelectElement {
      get value() { return '' }
      set value(_value: string) {}
    }
    const mounted = await mountMarket(
      <MemoryRouter>
        <QueryClientProvider client={client}>
          <I18nextProvider i18n={i18n}>
            <PluginMarketPanel />
          </I18nextProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    )
    try {
      expect(findRole(mounted.container, 'button', 'Two')).toBeNull()
      const openDetail = findButton(mounted.container, '查看详情')
      expect(openDetail).not.toBeNull()
      await act(async () => {
        openDetail?.dispatchEvent(clickEvent(openDetail!))
      })
      const footer = findTestId(mounted.container, 'market-detail-footer')
      expect(footer).not.toBeNull()
      expect(footer?.getAttribute('class')).toContain('border-t')
      expect(footer?.childNodes).toHaveLength(1)
      const shell = footer?.childNodes[0] as MiniNode
      expect(shell.getAttribute('class')).toContain('justify-end')
      expect(shell.getAttribute('class')).toContain('gap-2')
      expect(findButton(footer!, '关闭')).not.toBeNull()
      expect(findButton(footer!, '安装')).not.toBeNull()
      expect(findButton(footer!, '安装并启用')).not.toBeNull()
      expect(footer?.textContent).not.toContain('命名空间')
      const heading = findTestId(mounted.container, 'market-detail-heading')
      expect(findTag(heading!, 'IMG')?.getAttribute('src')).toBe(iconUrl)
      expect(findTag(heading!, 'IMG')?.parentNode?.getAttribute('class')).toContain('h-12')
      const body = findTestId(mounted.container, 'market-detail-body')
      expect(findTag(body!, 'IMG')).toBeNull()
      expect(body?.textContent).toContain('作者：Acme')
      expect(body?.textContent).not.toContain('命名空间')
    } finally {
      await mounted.unmount()
    }
  })
})
