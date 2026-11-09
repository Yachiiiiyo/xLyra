import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchAppearance, updateAppearance, useAppearanceQuery, useHasAppBackground, type AppearanceConfig } from '@/features/appearance/api/appearance'

const useQuery = vi.hoisted(() => vi.fn())
const apiFetch = vi.hoisted(() => vi.fn())

vi.mock('@tanstack/react-query', () => ({ useQuery }))
vi.mock('@/lib/http', () => ({ apiFetch }))

function Probe() {
  return <div data-has={useHasAppBackground() ? 'true' : 'false'} />
}

function AppearanceQueryProbe() {
  useAppearanceQuery()
  return null
}

function renderHas() {
  return renderToStaticMarkup(<Probe />)
}

beforeEach(() => {
  useQuery.mockReset()
  apiFetch.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useHasAppBackground', () => {
  it('加载中 / 失败按未配置处理', () => {
    useQuery.mockReturnValue({ data: undefined })
    expect(renderHas()).toContain('data-has="false"')
    useQuery.mockReturnValue({ data: undefined, error: new Error('boom') })
    expect(renderHas()).toContain('data-has="false"')
  })

  it('空串 / 纯空白视为未配置', () => {
    useQuery.mockReturnValue({ data: { background_image: '', custom_background_images: [] } })
    expect(renderHas()).toContain('data-has="false"')
    useQuery.mockReturnValue({ data: { background_image: '   ', custom_background_images: [] } })
    expect(renderHas()).toContain('data-has="false"')
  })

  it('配置了背景图时为 true', () => {
    useQuery.mockReturnValue({ data: { background_image: '/api/v1/appearance/backgrounds/bg.png', custom_background_images: [] } })
    expect(renderHas()).toContain('data-has="true"')
  })
})

describe('appearance cache integration', () => {
  const config: AppearanceConfig = {
    background_image: '/api/v1/appearance/backgrounds/bg.webp',
    custom_background_images: ['/api/v1/appearance/backgrounds/bg.webp'],
    hidden_menu_items: ['/analytics'],
    home_page: 'agent',
  }

  it('查询使用缓存首屏数据并立即后台刷新', () => {
    vi.stubGlobal('localStorage', { getItem: vi.fn(() => JSON.stringify(config)) })

    renderToStaticMarkup(<AppearanceQueryProbe />)

    expect(useQuery).toHaveBeenCalledWith(expect.objectContaining({
      initialData: config,
      initialDataUpdatedAt: 0,
    }))
  })

  it('GET 与 PUT 成功后写入本地缓存', async () => {
    const setItem = vi.fn()
    vi.stubGlobal('localStorage', { setItem })
    apiFetch.mockResolvedValue(config)

    await expect(fetchAppearance()).resolves.toEqual(config)
    await expect(updateAppearance(config)).resolves.toEqual(config)

    expect(setItem).toHaveBeenCalledTimes(2)
    expect(setItem).toHaveBeenCalledWith('xlyra:appearance', JSON.stringify(config))
  })
})
