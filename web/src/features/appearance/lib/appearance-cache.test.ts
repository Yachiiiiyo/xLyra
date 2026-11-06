import { afterEach, describe, expect, it, vi } from 'vitest'
import { loadCachedAppearance, saveCachedAppearance } from '@/features/appearance/lib/appearance-cache'
import type { AppearanceConfig } from '@/features/appearance/api/appearance'

const config: AppearanceConfig = {
  background_image: '/api/v1/appearance/backgrounds/bg.webp',
  custom_background_images: ['/api/v1/appearance/backgrounds/bg.webp'],
  hidden_menu_items: ['/analytics'],
  home_page: 'agent',
}

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubStorage(initial?: string) {
  let value = initial
  const storage = {
    getItem: vi.fn(() => value ?? null),
    setItem: vi.fn((_key: string, next: string) => {
      value = next
    }),
  }
  vi.stubGlobal('localStorage', storage)
  return storage
}

describe('appearance cache', () => {
  it('写入后可读取完整配置', () => {
    const storage = stubStorage()

    saveCachedAppearance(config)

    expect(storage.setItem).toHaveBeenCalledWith('xlyra:appearance', JSON.stringify(config))
    expect(loadCachedAppearance()).toEqual(config)
  })

  it('缺失或非法字段使用默认值补齐', () => {
    stubStorage(JSON.stringify({ background_image: '/bg.webp', home_page: 'invalid' }))

    expect(loadCachedAppearance()).toEqual({
      background_image: '/bg.webp',
      custom_background_images: [],
      hidden_menu_items: [],
      home_page: 'dashboard',
    })
  })

  it.each(['{broken', 'null', '[]', '42'])('损坏数据 %s 返回 undefined', (cached) => {
    stubStorage(cached)

    expect(loadCachedAppearance()).toBeUndefined()
  })

  it('丢弃 data URL，只保留服务端图片路径', () => {
    stubStorage(JSON.stringify({
      ...config,
      background_image: 'data:image/png;base64,AAAA',
      custom_background_images: ['data:image/png;base64,AAAA', '/bg.webp'],
    }))

    expect(loadCachedAppearance()).toEqual({
      ...config,
      background_image: '',
      custom_background_images: ['/bg.webp'],
    })
  })

  it('保存时也不会写入 data URL', () => {
    const storage = stubStorage()

    saveCachedAppearance({ ...config, background_image: 'data:image/webp;base64,AAAA' })

    expect(storage.setItem.mock.calls[0][1]).not.toContain('data:image')
  })

  it('读取存储抛错时静默返回 undefined', () => {
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => { throw new Error('blocked') }),
    })

    expect(loadCachedAppearance()).toBeUndefined()
  })

  it('写入存储抛错时静默忽略', () => {
    vi.stubGlobal('localStorage', {
      setItem: vi.fn(() => { throw new Error('quota exceeded') }),
    })

    expect(() => saveCachedAppearance(config)).not.toThrow()
  })
})
