export function resolveRelativePath(baseFile: string, src: string): string | null {
  if (!src || src.startsWith('/') || /^[a-z][a-z\d+.-]*:/i.test(src)) return null
  const directory = baseFile.split('/').slice(0, -1)
  const segments: string[] = []
  for (const segment of [...directory, ...src.split('/')]) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      if (segments.length === 0) return null
      segments.pop()
    } else segments.push(segment)
  }
  return segments.length ? segments.join('/') : null
}
