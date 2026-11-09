import { describe, expect, it } from 'vitest'
import { buildPreviewDocument, HTML_PREVIEW_MAX_BYTES, PREVIEW_SANDBOX } from './html-preview'

function expectCspFirst(html: string) {
  const csp = html.indexOf('<meta http-equiv="Content-Security-Policy"')
  expect(csp).toBeGreaterThanOrEqual(0)
  expect(html.slice(0, csp).replace(/<!--[\s\S]*?-->/g, '')).not.toMatch(/<(?:script|link|style|meta)\b/i)
}

describe('buildPreviewDocument', () => {
  it('紧跟 head 开标签注入并保留 doctype', () => {
    const html = buildPreviewDocument('<!DOCTYPE html><html><head><title>A</title></head><body></body></html>')
    expect(html.startsWith('<!DOCTYPE html>')).toBe(true)
    expect(html).toContain('<head><meta http-equiv="Content-Security-Policy"')
    expectCspFirst(html)
  })

  it('没有 head 时在 html 开标签后创建 head，忽略大小写', () => {
    const html = buildPreviewDocument('<HTML lang="zh"><BODY>A</BODY></HTML>')
    expect(html).toContain('<HTML lang="zh"><head><meta http-equiv="Content-Security-Policy"')
    expectCspFirst(html)
  })

  it('片段包成完整文档', () => {
    const html = buildPreviewDocument('<p>A</p>')
    expect(html).toMatch(/^<!DOCTYPE html><html><head><meta /)
    expect(html).toContain('<body><p>A</p></body></html>')
    expectCspFirst(html)
  })

  it('忽略注释中的 head 与 html 标签', () => {
    const html = buildPreviewDocument('<!-- <head><meta name="x"></head> --><HTML><body>A</body></HTML>')
    expect(html).toContain('</HTML>')
    expect(html.indexOf('<head><meta http-equiv="Content-Security-Policy"')).toBeGreaterThan(html.indexOf('-->'))
    expectCspFirst(html)
  })
})

describe('HTML 沙箱常量', () => {
  it('仅允许脚本且预览上限为 2 MiB', () => {
    expect(PREVIEW_SANDBOX).toBe('allow-scripts')
    expect(HTML_PREVIEW_MAX_BYTES).toBe(2 * 1024 * 1024)
  })
})
