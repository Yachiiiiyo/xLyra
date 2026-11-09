import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// 直接读样式源码，防止以后误删移动端覆盖或 iOS 防缩放规则
const css = readFileSync(new URL('../index.css', import.meta.url), 'utf8')
const mobileRoot = css.match(/@media \(max-width: 1023px\) \{\s*:root \{([\s\S]*?)\}\s*\}/)?.[1] ?? ''

describe('index.css 移动端字号', () => {
  it('移动端 Body 与 Tailwind 标准档各降一档', () => {
    expect(mobileRoot).toContain('--text-body: 15px;')
    expect(mobileRoot).toContain('--text-xs: 11px;')
    expect(mobileRoot).toContain('--text-sm: 13px;')
  })

  it('移动端大标题与 KPI 字号再收一档', () => {
    expect(mobileRoot).toContain('--text-large-title: 26px;')
    expect(mobileRoot).toContain('--text-kpi: 22px;')
  })

  it('移动端控件比桌面只大一档', () => {
    expect(mobileRoot).toContain('--control-h: 40px;')
    expect(mobileRoot).toContain('--switch-h: 28px;')
    expect(mobileRoot).toContain('--check-size: 20px;')
  })

  it('输入类控件保留 16px 防缩放规则', () => {
    expect(css).toMatch(/input:not\(\[type="checkbox"\]\)[\s\S]{0,200}font-size: 16px !important;/)
  })
})
