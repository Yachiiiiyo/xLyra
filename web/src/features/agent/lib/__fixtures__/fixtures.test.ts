import { describe, expect, it } from 'vitest'

// Vite 的 ?raw glob 在类型上返回 unknown,运行时是逐文件的字符串内容
const rawFixtures = import.meta.glob('./translated-*.jsonl', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('translated-* 事件夹具', () => {
  it('样本数量只增不减', () => {
    expect(Object.keys(rawFixtures).length).toBeGreaterThanOrEqual(13)
  })

  it('每个非空行都是带字符串 type 字段的合法 JSON', () => {
    for (const [path, content] of Object.entries(rawFixtures)) {
      const lines = content.split('\n')
      lines.forEach((line, index) => {
        if (!line.trim()) return
        let parsed: unknown
        try {
          parsed = JSON.parse(line)
        } catch (error) {
          throw new Error(`${path}:${index + 1} 不是合法 JSON: ${error}`)
        }
        const type = (parsed as Record<string, unknown>).type
        if (typeof type !== 'string' || type === '') {
          throw new Error(`${path}:${index + 1} 缺少非空字符串字段 type`)
        }
      })
    }
  })
})
