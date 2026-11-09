import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AgentBackdrop } from './agent-backdrop'

describe('AgentBackdrop', () => {
  it('renders the dull overlay below the panels', () => {
    const html = renderToStaticMarkup(<AgentBackdrop />)
    expect(html).toContain('data-agent-backdrop="true"')
    expect(html).toContain('agent-backdrop-dull')
    expect(html).toContain('pointer-events-none')
    expect(html).toContain('absolute inset-0')
    expect(html).toContain('aria-hidden="true"')
  })

  it('does not render an image layer of its own', () => {
    const html = renderToStaticMarkup(<AgentBackdrop />)
    expect(html).not.toContain('background-image')
    expect(html).not.toContain('<img')
  })
})
