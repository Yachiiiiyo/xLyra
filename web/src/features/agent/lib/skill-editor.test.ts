import { describe, expect, it } from 'vitest'
import { buildSkillTemplate, isValidSkillName, syncSkillNameInContent } from './skill-editor'

describe('isValidSkillName', () => {
  it('accepts lowercase letters, digits and single dashes within 1–64 chars', () => {
    expect(isValidSkillName('a')).toBe(true)
    expect(isValidSkillName('pdf-tools')).toBe(true)
    expect(isValidSkillName('t2-regex-replace')).toBe(true)
    expect(isValidSkillName('a'.repeat(64))).toBe(true)
  })

  it('rejects empty, overlong and illegal characters', () => {
    expect(isValidSkillName('')).toBe(false)
    expect(isValidSkillName('a'.repeat(65))).toBe(false)
    expect(isValidSkillName('Pdf-Tools')).toBe(false)
    expect(isValidSkillName('pdf_tools')).toBe(false)
    expect(isValidSkillName('pdf tools')).toBe(false)
    expect(isValidSkillName('pdf.tools')).toBe(false)
    expect(isValidSkillName('pdf工具')).toBe(false)
  })

  it('rejects leading/trailing dashes and consecutive dashes', () => {
    expect(isValidSkillName('-pdf')).toBe(false)
    expect(isValidSkillName('pdf-')).toBe(false)
    expect(isValidSkillName('pdf--tools')).toBe(false)
  })
})

describe('buildSkillTemplate', () => {
  it('builds frontmatter with required name/description and the body placeholder', () => {
    const template = buildSkillTemplate('pdf-tools', 'PDF 处理工具', '# 操作说明\n\n写下这个 skill 的执行步骤…')
    expect(template).toBe(
      '---\n'
      + 'name: pdf-tools\n'
      + 'description: PDF 处理工具\n'
      + '---\n'
      + '\n'
      + '# 操作说明\n'
      + '\n'
      + '写下这个 skill 的执行步骤…',
    )
  })

  it('leaves the name value empty when no name is typed yet', () => {
    expect(buildSkillTemplate('', '描述', '正文')).toBe('---\nname:\ndescription: 描述\n---\n\n正文')
  })
})

describe('syncSkillNameInContent', () => {
  const template = buildSkillTemplate('pdf-tools', '描述', '# 操作说明')

  it('rewrites the first frontmatter name line when it still matches the previous input', () => {
    const next = syncSkillNameInContent(template, 'pdf-tools', 'ocr-tools')
    expect(next).toBe(template.replace('name: pdf-tools', 'name: ocr-tools'))
  })

  it('syncs from an empty name line as the user starts typing', () => {
    const empty = buildSkillTemplate('', '描述', '正文')
    expect(syncSkillNameInContent(empty, '', 'pdf')).toBe(empty.replace('name:', 'name: pdf'))
  })

  it('keeps the rest of the content untouched', () => {
    const content = '---\nname: old\ndescription: d\n---\n\nbody line\nname: not-this'
    const next = syncSkillNameInContent(content, 'old', 'new')
    expect(next).toBe('---\nname: new\ndescription: d\n---\n\nbody line\nname: not-this')
  })

  it('stops syncing once the user manually edited the name line', () => {
    const manual = template.replace('name: pdf-tools', 'name: hand-written')
    expect(syncSkillNameInContent(manual, 'pdf-tools', 'ocr-tools')).toBe(manual)
  })

  it('returns content untouched without frontmatter or a name line', () => {
    expect(syncSkillNameInContent('no frontmatter', 'old', 'new')).toBe('no frontmatter')
    const noName = '---\ndescription: d\n---\n\nbody'
    expect(syncSkillNameInContent(noName, 'old', 'new')).toBe(noName)
  })

  it('reads quoted name values like the backend parser', () => {
    const quoted = template.replace('name: pdf-tools', 'name: "pdf-tools"')
    expect(syncSkillNameInContent(quoted, 'pdf-tools', 'ocr-tools')).toBe(quoted.replace('"pdf-tools"', 'ocr-tools'))
  })
})
