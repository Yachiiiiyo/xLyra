import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useHasAppBackground } from '@/features/appearance/api/appearance'

const useQuery = vi.hoisted(() => vi.fn())

vi.mock('@tanstack/react-query', () => ({ useQuery }))

function Probe() {
  return <div data-has={useHasAppBackground() ? 'true' : 'false'} />
}

function renderHas() {
  return renderToStaticMarkup(<Probe />)
}

beforeEach(() => {
  useQuery.mockReset()
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
