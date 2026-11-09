import type { AgentRun, AgentRunChanges } from '@/features/agent/lib/agent-events'

export type AgentFileChangeKind = 'write' | 'edit'
export type AgentFileChange = {
  path: string
  kind: AgentFileChangeKind
  additions: number
  deletions: number | null
  patches: string[]
  content?: string
  contentTruncated?: boolean
  /** 以下字段只在后端清单差(source === 'server')的变更项上出现 */
  status?: 'added' | 'modified' | 'deleted'
  turnId?: string
  size?: number
  binary?: boolean
  hasDiff?: boolean
  truncated?: boolean
  source?: 'server'
}

export function serverFileChanges(changes: AgentRunChanges): AgentFileChange[] {
  return changes.files.map((file) => ({
    path: file.path,
    kind: file.kind === 'added' ? 'write' : 'edit',
    additions: file.kind === 'deleted' ? 0 : file.additions,
    deletions: file.deletions,
    patches: [],
    status: file.kind,
    turnId: changes.turnId,
    size: file.size,
    binary: file.binary === true,
    hasDiff: file.has_diff,
    truncated: file.truncated === true,
    source: 'server',
  }))
}

/** 变更区展示用:有后端 run_changes 时以它为准,否则回退到从 write/edit 步骤推导 */
export function runDisplayChanges(run: AgentRun): AgentFileChange[] {
  return run.changes ? serverFileChanges(run.changes) : runFileChanges(run)
}

type FileChangeState = {
  change: AgentFileChange
  writeAdditions: number
  editAdditions: number
  editDeletions: number
  hasEdit: boolean
}

export function runFileChanges(run: AgentRun): AgentFileChange[] {
  const changes: AgentFileChange[] = []
  const byPath = new Map<string, FileChangeState>()
  for (const step of run.steps) {
    if (step.kind !== 'tool' || step.status !== 'done') continue
    const tool = step.title.toLowerCase()
    if (tool !== 'write' && tool !== 'edit') continue
    let input: { path?: unknown; content?: unknown; edits?: unknown }
    try {
      input = JSON.parse(step.input ?? '') as { path?: unknown; content?: unknown; edits?: unknown }
    } catch {
      continue
    }
    if (!input || typeof input !== 'object' || typeof input.path !== 'string' || !input.path) continue
    let state = byPath.get(input.path)
    if (!state) {
      const change: AgentFileChange = {
        path: input.path,
        kind: tool,
        additions: 0,
        deletions: null,
        patches: [],
      }
      state = { change, writeAdditions: 0, editAdditions: 0, editDeletions: 0, hasEdit: false }
      byPath.set(input.path, state)
      changes.push(change)
    }
    if (tool === 'write') {
      const content = typeof input.content === 'string' ? input.content : ''
      state.writeAdditions = contentLineCount(content)
      // 后写入的整份内容覆盖此前的修改,之前的 edit 变更不再适用
      state.change.patches = []
      state.editAdditions = 0
      state.editDeletions = 0
      state.hasEdit = false
      if (content.length > 256 * 1024) {
        delete state.change.content
        state.change.contentTruncated = true
      } else {
        state.change.content = content
        delete state.change.contentTruncated
      }
    } else {
      state.hasEdit = true
      // 源码要反映最终内容:把本次 edit 套到已知内容上,套不上(内容未知或匹配不唯一)就不再显示源码
      if (state.change.content !== undefined) {
        const next = applyEdits(state.change.content, input.edits)
        if (next === null) delete state.change.content
        else state.change.content = next
      }
      if (step.patch) {
        state.change.patches.push(step.patch)
        const stats = patchStats(step.patch)
        state.editAdditions += stats.additions
        state.editDeletions += stats.deletions
      }
    }
    // 内容已知(write 后可能又被 edit)时,新增行数就是最终内容的行数;修改前内容未知,不给删除数
    if (state.change.content !== undefined && state.writeAdditions > 0) {
      state.change.additions = contentLineCount(state.change.content)
      state.change.deletions = null
    } else {
      state.change.additions = state.writeAdditions + state.editAdditions
      state.change.deletions = state.hasEdit ? state.editDeletions : null
    }
  }
  return changes
}

/** 与 Pi edit 一致:每处 oldText 都对原内容匹配且必须唯一,不重叠;任何一处对不上返回 null */
function applyEdits(content: string, edits: unknown): string | null {
  if (!Array.isArray(edits) || edits.length === 0) return null
  const spans: { start: number; end: number; text: string }[] = []
  for (const edit of edits) {
    const { oldText, newText } = (edit ?? {}) as { oldText?: unknown; newText?: unknown }
    if (typeof oldText !== 'string' || typeof newText !== 'string' || !oldText) return null
    const start = content.indexOf(oldText)
    if (start < 0 || content.indexOf(oldText, start + 1) >= 0) return null
    spans.push({ start, end: start + oldText.length, text: newText })
  }
  spans.sort((a, b) => a.start - b.start)
  let result = ''
  let cursor = 0
  for (const span of spans) {
    if (span.start < cursor) return null
    result += content.slice(cursor, span.start) + span.text
    cursor = span.end
  }
  return result + content.slice(cursor)
}

/** 变更标签展示用的 patch:write 起头且内容已知的文件,整份内容相对空文件都是新增(含后续 edit 的结果),合成一份全新增 diff;否则用 edit 的 patch */
export function changePatches(change: AgentFileChange): string[] {
  if (change.kind !== 'write' || change.content === undefined) return change.patches
  const lines = change.content === '' ? [] : change.content.replace(/\n$/, '').split('\n')
  if (lines.length === 0) return []
  return [`--- ${change.path}\n+++ ${change.path}\n@@ -0,0 +1,${lines.length} @@\n${lines.map((line) => `+${line}`).join('\n')}\n`]
}

function contentLineCount(content: string): number {
  if (!content) return 0
  return content.split('\n').length - (content.endsWith('\n') ? 1 : 0)
}

export function patchStats(patch: string): { additions: number; deletions: number } {
  let additions = 0
  let deletions = 0
  for (const hunk of parseUnifiedPatch(patch)) {
    for (const line of hunk.lines) {
      if (line.kind === 'add') additions += 1
      if (line.kind === 'del') deletions += 1
    }
  }
  return { additions, deletions }
}

export type DiffLine = { kind: 'context' | 'add' | 'del'; text: string; oldNo?: number; newNo?: number }
export type DiffHunk = { header: string; oldStart: number; newStart: number; lines: DiffLine[] }

export function parseUnifiedPatch(patch: string): DiffHunk[] {
  const hunks: DiffHunk[] = []
  let hunk: DiffHunk | undefined
  let oldNo = 0
  let newNo = 0
  for (const line of patch.split('\n')) {
    // 文件头只出现在第一个 @@ 之前;块内以 "--- "/"+++ " 开头的是内容本身以 "-- "/"++ " 开头的增删行
    if (!hunk && (line.startsWith('--- ') || line.startsWith('+++ ') || line.startsWith('Index:') || line.startsWith('==='))) continue
    const header = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
    if (header) {
      oldNo = Number(header[1])
      newNo = Number(header[2])
      hunk = { header: line, oldStart: oldNo, newStart: newNo, lines: [] }
      hunks.push(hunk)
      continue
    }
    if (!hunk || line.startsWith('\\')) continue
    if (line.startsWith(' ')) {
      hunk.lines.push({ kind: 'context', text: line.slice(1), oldNo: oldNo++, newNo: newNo++ })
    } else if (line.startsWith('-')) {
      hunk.lines.push({ kind: 'del', text: line.slice(1), oldNo: oldNo++ })
    } else if (line.startsWith('+')) {
      hunk.lines.push({ kind: 'add', text: line.slice(1), newNo: newNo++ })
    }
  }
  return hunks
}
