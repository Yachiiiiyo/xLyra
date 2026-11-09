/**
 * Skill 新建的纯函数：名称校验（与后端 capabilities.validSkillName 一致）、
 * SKILL.md 模板与「名称输入 ↔ frontmatter name 行」联动。
 */

/** 与后端一致：1–64 个小写字母/数字/连字符，不以连字符开头或结尾、不含连续连字符。 */
export function isValidSkillName(name: string): boolean {
  if (name === '' || name.length > 64) return false
  if (!/^[a-z0-9-]+$/.test(name)) return false
  return !name.startsWith('-') && !name.endsWith('-') && !name.includes('--')
}

function skillNameLine(name: string): string {
  return name ? `name: ${name}` : 'name:'
}

/** SKILL.md 模板：frontmatter 含后端必填的 name/description，正文为占位说明。 */
export function buildSkillTemplate(name: string, description: string, body: string): string {
  return `---\n${skillNameLine(name)}\ndescription: ${description}\n---\n\n${body}`
}

function unquote(value: string): string {
  if (value.length >= 2) {
    const first = value[0]
    const last = value[value.length - 1]
    if ((first === '"' && last === '"') || (first === '\'' && last === '\'')) return value.slice(1, -1)
  }
  return value
}

/** 找到 frontmatter 块内第一个顶层 `name:` 行：返回行号与当前值；无 frontmatter 或无该行返回 null。 */
function locateFrontmatterName(content: string): { index: number; value: string } | null {
  const lines = content.split('\n')
  if (lines.length === 0 || lines[0]!.trim() !== '---') return null
  for (let index = 1; index < lines.length; index++) {
    const line = lines[index]!
    if (/^\s/.test(line)) continue
    if (line.trim() === '---') return null
    const match = /^name:[ \t]*(.*)$/.exec(line)
    if (match) return { index, value: unquote(match[1]!.trim()) }
  }
  return null
}

/**
 * 名称输入变化时同步改写正文第一个 frontmatter 的 `name:` 行；
 * 用户手动改过该行（当前值不再等于上一次的名称输入）或没有该行时原样返回，断开联动。
 */
export function syncSkillNameInContent(content: string, prevName: string, nextName: string): string {
  const located = locateFrontmatterName(content)
  if (!located || located.value !== prevName) return content
  const lines = content.split('\n')
  lines[located.index] = skillNameLine(nextName)
  return lines.join('\n')
}
