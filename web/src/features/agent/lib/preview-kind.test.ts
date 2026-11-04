import { describe, expect, it } from 'vitest'
import { previewKindForPath } from './preview-kind'

describe('previewKindForPath', () => {
  it('按扩展名与特殊文件名分类，扩展名不区分大小写', () => {
    expect(previewKindForPath('docs/A.MD')).toBe('markdown')
    expect(previewKindForPath('page.htm')).toBe('html')
    expect(previewKindForPath('a.jpeg')).toBe('image')
    expect(previewKindForPath('a.svg')).toBe('svg')
    expect(previewKindForPath('a.tsv')).toBe('csv')
    expect(previewKindForPath('src/a.TSX')).toBe('text')
    expect(previewKindForPath('Dockerfile')).toBe('text')
    expect(previewKindForPath('README')).toBe('text')
    expect(previewKindForPath('a.zip')).toBe('binary')
  })
})
