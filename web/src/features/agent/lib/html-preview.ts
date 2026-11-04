export const PREVIEW_SANDBOX = 'allow-scripts'
export const HTML_PREVIEW_MAX_BYTES = 2 * 1024 * 1024

const CSP_META = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; script-src \'unsafe-inline\' \'unsafe-eval\'; style-src \'unsafe-inline\'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src \'none\'; frame-src \'none\'; base-uri \'none\'; form-action \'none\'">'

function firstTag(html: string, name: 'head' | 'html') {
  const pattern = /<!--[\s\S]*?-->|<(head|html)\b[^>]*>/gi
  for (const match of html.matchAll(pattern)) {
    if (match[1]?.toLowerCase() === name) return { index: match.index, end: match.index + match[0].length }
  }
  return null
}

export function buildPreviewDocument(html: string): string {
  const head = firstTag(html, 'head')
  if (head) return html.slice(0, head.end) + CSP_META + html.slice(head.end)
  const root = firstTag(html, 'html')
  if (root) return html.slice(0, root.end) + `<head>${CSP_META}</head>` + html.slice(root.end)
  return `<!DOCTYPE html><html><head>${CSP_META}</head><body>${html}</body></html>`
}
