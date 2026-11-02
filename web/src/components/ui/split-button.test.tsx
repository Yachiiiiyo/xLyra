import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { SplitButton, type SplitButtonItem } from './split-button'

vi.mock('@radix-ui/react-popover', async () => {
  const React = await import('react')
  const pass = ({ children }: { children?: React.ReactNode }) => React.createElement(React.Fragment, null, children)
  return {
    Root: pass,
    Trigger: ({ children }: { children?: React.ReactNode }) => children,
    Portal: pass,
    Content: ({
      children,
      side,
      align,
      collisionPadding,
    }: {
      children?: React.ReactNode
      side?: string
      align?: string
      collisionPadding?: number
    }) => React.createElement('div', {
      'data-side': side,
      'data-align': align,
      'data-collision-padding': collisionPadding == null ? undefined : String(collisionPadding),
    }, children),
  }
})

const items: SplitButtonItem[] = [
  { key: 'install-enable', label: '安装并启用', onSelect: () => {} },
  { key: 'hold', label: '稍后', onSelect: () => {}, disabled: true },
]

function renderSplit(props: Partial<Parameters<typeof SplitButton>[0]> = {}) {
  return renderToStaticMarkup(
    <SplitButton menuLabel="更多安装选项" items={items} onClick={() => {}} {...props}>
      安装
    </SplitButton>,
  )
}

function buttonTags(html: string) {
  return [...html.matchAll(/<button\b[^>]*>/g)].map((match) => match[0])
}

describe('SplitButton', () => {
  it('renders the main action and the menu button', () => {
    const html = renderSplit()
    const tags = buttonTags(html)
    expect(tags.some((tag) => !tag.includes('aria-label'))).toBe(true)
    expect(html).toContain('>安装<')
    expect(html).toContain('aria-label="更多安装选项"')
    expect(html).toContain('aria-haspopup="menu"')
    expect(tags.filter((tag) => tag.includes('disabled=""')).length).toBe(1)
  })

  it('maps menu items onto buttons, including a disabled item', () => {
    const html = renderSplit({ side: 'top', align: 'end' })
    expect(html).toContain('>安装并启用<')
    expect(html).toContain('>稍后<')
    expect(html).toContain('data-side="top"')
    expect(html).toContain('data-align="end"')
    expect(html).toContain('data-collision-padding="16"')
    const held = html.match(/<button\b[^>]*>稍后<\/button>/)
    expect(held?.[0]).toContain('disabled=""')
    const enabled = html.match(/<button\b[^>]*>安装并启用<\/button>/)
    expect(enabled?.[0]).not.toContain('disabled=""')
  })

  it('disables both segments when disabled', () => {
    const html = renderSplit({ disabled: true })
    const tags = buttonTags(html)
    const menu = tags.find((tag) => tag.includes('aria-label="更多安装选项"'))
    expect(tags[0]).not.toContain('aria-label')
    expect(tags[0]).toContain('disabled=""')
    expect(menu).toContain('disabled=""')
  })

  it('disables both segments while loading', () => {
    const html = renderSplit({ loading: true })
    const tags = buttonTags(html)
    const main = tags.find((tag) => !tag.includes('aria-label'))
    const menu = tags.find((tag) => tag.includes('aria-label="更多安装选项"'))
    expect(main).toContain('disabled=""')
    expect(menu).toContain('disabled=""')
    expect(html).toContain('animate-spin')
    expect(html).toContain('aria-busy="true"')
  })
})
