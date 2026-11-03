import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AgentBackdrop } from './agent-backdrop'

describe('AgentBackdrop', () => {
  it('renders nothing without an image', () => {
    expect(renderToStaticMarkup(<AgentBackdrop />)).toBe('')
    expect(renderToStaticMarkup(<AgentBackdrop image={null} />)).toBe('')
    expect(renderToStaticMarkup(<AgentBackdrop image="" />)).toBe('')
  })

  it('covers the viewport with the image, dim overlay, and optional blur', () => {
    const html = renderToStaticMarkup(<AgentBackdrop image="/bg.jpg" dim={0.4} blur={2} />)
    expect(html).toContain('data-agent-backdrop="true"')
    expect(html).toContain('background-image:url(')
    expect(html).toContain('/bg.jpg')
    expect(html).toContain('background-size:cover')
    expect(html).toContain('background-position:center')
    expect(html).toContain('filter:blur(2px)')
    expect(html).toContain('opacity:0.4')
    expect(html).toContain('pointer-events-none')
    expect(html).toContain('fixed inset-0')
  })

  it('omits the dim layer when dim is 0 and skips blur when 0', () => {
    const html = renderToStaticMarkup(<AgentBackdrop image="/bg.jpg" dim={0} />)
    expect(html).not.toContain('bg-black')
    expect(html).not.toContain('filter')
  })
})
