export type PreviewKind = 'markdown' | 'html' | 'image' | 'svg' | 'csv' | 'text' | 'binary'

const kinds: Record<string, PreviewKind> = {
  md: 'markdown', markdown: 'markdown',
  html: 'html', htm: 'html',
  png: 'image', jpg: 'image', jpeg: 'image', webp: 'image', gif: 'image',
  svg: 'svg',
  csv: 'csv', tsv: 'csv',
}

const textExtensions = new Set(['txt', 'log', 'json', 'yaml', 'yml', 'toml', 'css', 'js', 'mjs', 'ts', 'tsx', 'jsx', 'py', 'go', 'rs', 'java', 'c', 'h', 'cpp', 'sh', 'sql', 'xml', 'ini', 'conf', 'env'])
const textNames = new Set(['makefile', 'dockerfile', 'readme', 'license'])

export function previewKindForPath(path: string): PreviewKind {
  const name = path.split('/').pop()?.toLowerCase() ?? ''
  if (textNames.has(name)) return 'text'
  const extension = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : ''
  return kinds[extension] ?? (textExtensions.has(extension) ? 'text' : 'binary')
}
