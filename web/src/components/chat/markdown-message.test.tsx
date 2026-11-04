import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { MarkdownMessage } from '@/components/chat/markdown-message'

describe('MarkdownMessage 链接扩展', () => {
  it('未传 renderLink 时保持默认链接行为', () => {
    expect(renderToStaticMarkup(<MarkdownMessage content="[site](https://example.com)" />)).toContain('<a href="https://example.com">site</a>')
  })

  it('把链接交给 renderLink', () => {
    const html = renderToStaticMarkup(<MarkdownMessage content="[file](a.md)" renderLink={({ href, children }) => <button data-path={href}>{children}</button>} />)
    expect(html).toContain('<button data-path="a.md">file</button>')
  })
})

describe('MarkdownMessage 图片扩展', () => {
  it('未传 renderImage 时保持默认图片行为', () => {
    const html = renderToStaticMarkup(<MarkdownMessage content="![cat](images/a.png)" />)
    expect(html).toContain('<div class="chat-markdown text-body leading-6 text-label"><p><img src="images/a.png" alt="cat"/></p></div>')
  })

  it('把原样相对 src 交给 renderImage', () => {
    const renderImage = vi.fn(({ src, alt }: { src?: string; alt?: string }) => <span data-image={src}>{alt}</span>)
    const html = renderToStaticMarkup(<MarkdownMessage content="![cat](images/a.png)" renderImage={renderImage} />)
    expect(renderImage).toHaveBeenCalledExactlyOnceWith({ src: 'images/a.png', alt: 'cat' })
    expect(html).toContain('<span data-image="images/a.png">cat</span>')
  })
})
