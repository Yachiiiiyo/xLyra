import { describe, expect, it } from 'vitest'
import { resolveRelativePath } from './relative-path'

describe('resolveRelativePath', () => {
  it('相对当前文件目录归一化', () => {
    expect(resolveRelativePath('docs/guide/a.md', '../images/a.png')).toBe('docs/images/a.png')
    expect(resolveRelativePath('a.md', './images/a.png')).toBe('images/a.png')
  })

  it('拒绝越界、空结果、绝对路径与带协议路径', () => {
    expect(resolveRelativePath('a.md', '../a.png')).toBeNull()
    expect(resolveRelativePath('docs/a.md', '..')).toBeNull()
    expect(resolveRelativePath('a.md', '/a.png')).toBeNull()
    expect(resolveRelativePath('a.md', 'https://example.com/a.png')).toBeNull()
    expect(resolveRelativePath('a.md', '')).toBeNull()
  })
})
