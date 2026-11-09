import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { AppBackground } from '@/components/layout/app-background'
import { decodeAppBackgroundImage } from '@/components/layout/app-background-decode'

const useAppearanceQuery = vi.hoisted(() => vi.fn())

vi.mock('@/features/appearance/api/appearance', () => ({ useAppearanceQuery }))

vi.mock('@/components/layout/ambient-background', async () => {
  const React = await import('react')
  return {
    AmbientBackground: () => React.createElement('div', { 'data-ambient-background': 'true' }),
  }
})

function renderAt(path: string, mainContentOverlay = false) {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[path]}>
      <AppBackground mainContentOverlay={mainContentOverlay} />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  useAppearanceQuery.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AppBackground', () => {
  it('renders the starfield while loading or on failure', () => {
    useAppearanceQuery.mockReturnValue({ data: undefined })
    expect(renderAt('/dashboard')).toContain('data-ambient-background')
    useAppearanceQuery.mockReturnValue({ data: undefined, error: new Error('boom') })
    expect(renderAt('/dashboard')).toContain('data-ambient-background')
  })

  it('falls back to the starfield when no background image is configured', () => {
    useAppearanceQuery.mockReturnValue({ data: { background_image: '', custom_background_images: [] } })
    const html = renderAt('/dashboard')
    expect(html).toContain('data-ambient-background')
    expect(html).not.toContain('app-background-image')
  })

  it('keeps the starfield below a loading image layer during SSR', () => {
    useAppearanceQuery.mockReturnValue({ data: { background_image: '/api/v1/appearance/backgrounds/appearance-bg-abcd.png', custom_background_images: [] } })
    const html = renderAt('/agent')
    expect(html).toContain('data-ambient-background')
    expect(html).toContain('app-background-image app-background-image-loading')
    expect(html).toContain('background-image:url(')
    expect(html).toContain('appearance-bg-abcd.png')
    expect(html).toContain('data-liquid-glass-background="true"')
    expect(html).toContain('aria-hidden="true"')
  })

  it('shows an already decoded image immediately without the loading class', async () => {
    class ImageStub {
      src = ''
      decode = vi.fn().mockResolvedValue(undefined)
    }
    vi.stubGlobal('Image', ImageStub)
    const image = '/api/v1/appearance/backgrounds/already-decoded.webp'
    await decodeAppBackgroundImage(image)
    useAppearanceQuery.mockReturnValue({ data: { background_image: image, custom_background_images: [] } })

    const html = renderAt('/agent')

    expect(html).toContain('class="app-background-image"')
    expect(html).not.toContain('app-background-image-loading')
    expect(html).toContain('data-ambient-background')
  })

  it('treats a decode failure as loaded', async () => {
    class ImageStub {
      src = ''
      decode = vi.fn().mockRejectedValue(new Error('decode failed'))
    }
    vi.stubGlobal('Image', ImageStub)
    const image = '/api/v1/appearance/backgrounds/decode-failed.webp'
    await decodeAppBackgroundImage(image)
    useAppearanceQuery.mockReturnValue({ data: { background_image: image, custom_background_images: [] } })

    expect(renderAt('/agent')).not.toContain('app-background-image-loading')
  })

  it('adds the blur overlay outside /agent and keeps /agent clean', () => {
    useAppearanceQuery.mockReturnValue({ data: { background_image: '/bg.png', custom_background_images: [] } })
    expect(renderAt('/agent')).not.toContain('app-background-overlay')
    expect(renderAt('/agent/session/1')).not.toContain('app-background-overlay')
    expect(renderAt('/dashboard')).toContain('app-background-overlay')
    expect(renderAt('/sites')).toContain('app-background-overlay')
  })

  it('only marks the desktop main overlay when requested', () => {
    useAppearanceQuery.mockReturnValue({ data: { background_image: '/bg.png', custom_background_images: [] } })
    expect(renderAt('/dashboard')).not.toContain('app-background-overlay-main')
    expect(renderAt('/dashboard', true)).toContain('app-background-overlay app-background-overlay-main')
    expect(renderAt('/agent', true)).not.toContain('app-background-overlay')
  })
})
