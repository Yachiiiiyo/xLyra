import type { AgentGeneratedImage, AgentQuestionAnswer, AgentQuestionItem, AgentQuestionOption, AgentRunChangeFile, AgentTranscriptEntry } from '@/features/agent/api/agent'

export type { AgentQuestionAnswer }
import type { ChatAttachment } from '@/lib/chat-types'
import type { AgentSessionReference } from '@/features/agent/lib/references'

export type AgentStepStatus = 'running' | 'done' | 'error'
export type AgentRunStatus = 'running' | 'done' | 'error' | 'cancelled'

export type AgentWorkStep = {
  id: string
  /** text 是 loop 中途的助手文本段，与工具/状态步骤按到达顺序交错排列 */
  kind: 'tool' | 'status' | 'text'
  title: string
  input?: string
  output?: string
  patch?: string
  images?: AgentGeneratedImage[]
  argsDone?: boolean
  detail?: string
  status: AgentStepStatus
}

export type AgentPermissionRequest = {
  id: string
  tool: string
  detail?: string
  note?: string
  /** Resolved path/capability key recorded by the grant, echoed back to the grant-access API. */
  resolvedPath?: string
  decision?: 'allowed' | 'denied'
  /** 决定的来源：'session' = 会话内允许；auto = 后端会话内自动放行 */
  scope?: 'session'
  auto?: boolean
  /** 权限请求到达时 run.steps 的长度，用于把它插回流程中的实际位置 */
  position?: number
}

export type AgentRun = {
  id: string
  status: AgentRunStatus
  steps: AgentWorkStep[]
  finalText: string
  permissions: AgentPermissionRequest[]
  /** question 工具的提问（pending 卡片与已回答摘要都由这里驱动） */
  questions?: AgentQuestion[]
  startedAt: number
  endedAt?: number
  elapsedMs?: number
  errorMessage?: string
  /** 最早一条未决权限请求的到达时间:等待用户审批期间「用时」暂停计时 */
  pendingSince?: number
  /** 已结束的审批等待累计时长(不计入用时) */
  pausedMs?: number
  /** 后端清单差得到的本轮变更(终态之后到达),优先于从工具步骤推导的变更 */
  changes?: AgentRunChanges
}

/** question 工具的一次提问：status 为 pending 时渲染可交互卡片 */
export type AgentQuestion = {
  id: string
  toolCallId: string
  items: AgentQuestionItem[]
  answers?: AgentQuestionAnswer[]
  status: 'pending' | 'answered' | 'skipped' | 'cancelled'
  /** 超时自动跳过（后端 AGENT_QUESTION_TIMEOUT 兜底） */
  auto?: boolean
  /** 提问到达时 run.steps 的长度，与审批的 position 概念一致 */
  position?: number
}

export type AgentRunChanges = { turnId: string; files: AgentRunChangeFile[]; total: number; incomplete: boolean }

export type AgentTimelineItem =
  | { kind: 'user'; id: string; text: string; messageId?: string; createdAt: number; attachments?: ChatAttachment[]; references?: AgentSessionReference[] }
  | { kind: 'run'; id: string; run: AgentRun }

export type AgentTimeline = AgentTimelineItem[]

export type AgentStreamEvent = { type: string; data: unknown }

type EventKind =
  | 'text'
  | 'tool_call'
  | 'tool_call_delta'
  | 'tool_result'
  | 'thinking'
  | 'notice'
  | 'compaction'
  | 'permission'
  | 'permission_result'
  | 'question'
  | 'question_result'
  | 'done'
  | 'error'
  | 'cancelled'
  | 'run_changes'
  | 'other'

let fallbackCounter = 0

function nextId(prefix: string) {
  fallbackCounter += 1
  return `${prefix}-${fallbackCounter}`
}

function asRecord(data: unknown): Record<string, unknown> | null {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return null
  return data as Record<string, unknown>
}

function pickString(record: Record<string, unknown> | null, keys: string[]): string {
  if (!record) return ''
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string' && value.trim()) return value
  }
  return ''
}

function pickId(record: Record<string, unknown> | null, keys: string[]): string {
  if (!record) return ''
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string' && value.trim()) return value
    if (typeof value === 'number') return String(value)
  }
  return ''
}

function pickDetail(record: Record<string, unknown> | null, keys: string[]): string {
  if (!record) return ''
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string' && value.trim()) return value
    if (value !== undefined && value !== null) {
      try {
        const encoded = JSON.stringify(value)
        return encoded.length > 400 ? `${encoded.slice(0, 400)}…` : encoded
      } catch {
        return ''
      }
    }
  }
  return ''
}

function pickDelta(record: Record<string, unknown> | null): string {
  if (!record) return ''
  return typeof record.delta === 'string' ? record.delta : ''
}

function parseTimestamp(value?: string): number {
  const parsed = value ? Date.parse(value) : NaN
  return Number.isFinite(parsed) ? parsed : Date.now()
}

/** 后端把引用放在 message 里(与 attachments 同级);兼容放在条目顶层的写法。run_error 的 message 是字符串。 */
function transcriptReferences(entry: AgentTranscriptEntry): AgentTranscriptEntry['references'] {
  const message = entry.message
  return (typeof message === 'object' && message !== null ? message.references : undefined) ?? entry.references
}

/** 转录里用户消息的 references 映射为时间线 user 项的引用；缺 session_id 的条目丢弃 */
function parseTranscriptReferences(references: AgentTranscriptEntry['references']): AgentSessionReference[] | undefined {
  if (!Array.isArray(references) || references.length === 0) return undefined
  const mapped = references.flatMap((item) => {
    const sessionId = typeof item?.session_id === 'string' ? item.session_id : ''
    return sessionId ? [{ sessionId, title: typeof item?.title === 'string' ? item.title : '' }] : []
  })
  return mapped.length ? mapped : undefined
}

export function extractEventText(data: unknown): string {
  if (typeof data === 'string') return data
  const record = asRecord(data)
  if (!record) return ''
  return pickString(record, ['text', 'content', 'message', 'delta'])
}

/**
 * 正文流式增量:纯空白的非空字符串也要保留(段落间的 "\n\n"、行首缩进),
 * 丢掉会让 Markdown 的换行和空行消失,实时渲染成一坨,刷新读转录才正常。
 */
export function extractTextChunk(data: unknown): string {
  const text = extractEventText(data)
  if (text) return text
  const record = asRecord(data)
  if (!record) return ''
  for (const key of ['text', 'content', 'message', 'delta']) {
    const value = record[key]
    if (typeof value === 'string' && value.length > 0) return value
  }
  return ''
}

function classifyEvent(type: string, data: unknown): EventKind {
  const normalized = type.toLowerCase()
  if (normalized === 'run_changes') return 'run_changes'
  // question 工具的请求/应答事件（在 escalation/permission 判定之前，避免被吞）
  if (normalized === 'question_request') return 'question'
  if (normalized === 'question_answered') return 'question_result'
  // Escalation negotiation events are permission requests (payload nested under data.escalation).
  if (normalized.includes('escalation')) return 'permission'
  // 停滞/预算等软提醒：正文在 notice 字段，渲染为状态步骤而不是正文
  if (normalized.includes('stall') || normalized.includes('budget') || typeof asRecord(data)?.notice === 'string') return 'notice'
  if (normalized.includes('compact')) return 'compaction'
  if (normalized === 'tool_call_delta' || normalized === 'toolcall_delta') return 'tool_call_delta'
  if (normalized.includes('permission') || normalized.includes('approval')) {
    if (normalized.includes('result') || normalized.includes('grant') || normalized.includes('decision') || normalized.includes('resolved')) {
      return 'permission_result'
    }
    return 'permission'
  }
  if (normalized.includes('tool')) {
    if (normalized.includes('result') || normalized.includes('output') || normalized.includes('end') || normalized.includes('done')) {
      return 'tool_result'
    }
    return 'tool_call'
  }
  if (normalized.includes('think') || normalized.includes('reason')) return 'thinking'
  if (normalized.includes('cancel')) return 'cancelled'
  if (normalized.includes('error') || normalized.includes('fail')) return 'error'
  if (normalized.includes('done') || normalized.includes('complete') || normalized.includes('finish')) return 'done'
  if (normalized.includes('message') || normalized.includes('text') || normalized.includes('delta') || normalized.includes('content') || normalized.includes('assistant')) {
    return 'text'
  }
  return extractEventText(data) ? 'text' : 'other'
}

function lastRun(timeline: AgentTimeline): { index: number; run: AgentRun } | null {
  const last = timeline[timeline.length - 1]
  if (last?.kind !== 'run') return null
  return { index: timeline.length - 1, run: last.run }
}

/** question 的 questions 数组解析（SSE 事件与转录条目共用）：非法条目丢弃 */
function parseQuestionItems(raw: unknown): AgentQuestionItem[] {
  const list = Array.isArray(raw) ? raw : []
  return list.flatMap((entry): AgentQuestionItem[] => {
    const item = asRecord(entry as Record<string, unknown>)
    if (!item) return []
    const question = typeof item.question === 'string' ? item.question : ''
    if (!question) return []
    const options = Array.isArray(item.options)
      ? (item.options as unknown[]).flatMap((optionRaw): AgentQuestionOption[] => {
          const option = asRecord(optionRaw as Record<string, unknown>)
          const label = option && typeof option.label === 'string' ? option.label : ''
          return label ? [{ label, description: option && typeof option.description === 'string' ? option.description : undefined }] : []
        })
      : []
    if (options.length === 0) return []
    return [{ header: typeof item.header === 'string' ? item.header : '', question, multiSelect: item.multiSelect === true, options }]
  })
}

/** question_request 事件：载荷嵌在 data.question 下 */
function parseQuestionRequest(record: Record<string, unknown> | null): AgentQuestion | null {
  const payload = asRecord(record?.question) ?? record
  if (!payload) return null
  const items = parseQuestionItems(payload.questions)
  if (items.length === 0) return null
  return {
    id: pickId(payload, ['question_id', 'request_id', 'id']) || nextId('question'),
    toolCallId: pickId(payload, ['tool_call_id', 'call_id']) || '',
    items,
    status: 'pending',
  }
}

/** question_answered 事件的载荷：{ question_id, answers?, skipped?, cancelled?, auto? } */
function parseQuestionAnswers(data: unknown): AgentQuestionAnswer[] {
  const record = asRecord(data)
  if (!record || !Array.isArray(record.answers)) return []
  return (record.answers as unknown[]).map((raw) => {
    const answer = asRecord(raw as Record<string, unknown>)
    const selected = Array.isArray(answer?.selected) ? answer.selected.filter((item): item is string => typeof item === 'string') : []
    return { selected, other: typeof answer?.other === 'string' && answer.other.trim() ? answer.other : undefined }
  })
}

function parseRunChanges(record: Record<string, unknown> | null): AgentRunChanges | null {
  if (!record || !Array.isArray(record.files)) return null
  const files = record.files as AgentRunChangeFile[]
  return {
    turnId: typeof record.turn_id === 'string' ? record.turn_id : '',
    files,
    total: typeof record.total === 'number' ? Math.max(record.total, files.length) : files.length,
    incomplete: record.incomplete === true,
  }
}

/** run_changes 在终态之后到达:挂到时间线最后一个 run(无论状态),不新开 run、不改状态 */
function attachRunChanges(timeline: AgentTimeline, changes: AgentRunChanges | null): AgentTimeline {
  if (!changes) return timeline
  for (let index = timeline.length - 1; index >= 0; index -= 1) {
    const item = timeline[index]
    if (item?.kind !== 'run') continue
    const next = timeline.slice()
    next[index] = { ...item, run: { ...item.run, changes } }
    return next
  }
  return timeline
}

function ensureRun(timeline: AgentTimeline): AgentTimeline {
  const current = lastRun(timeline)
  if (current && current.run.status === 'running') return timeline
  const runSettled = (run: AgentRun) => run.permissions.every((request) => request.decision !== undefined)
    && (run.questions ?? []).every((question) => question.status !== 'pending')
  const resumedAfterEscalation = current?.run.status === 'cancelled' && runSettled(current.run)
    && (current.run.permissions.length > 0 || (current.run.questions ?? []).length > 0)
  if (resumedAfterEscalation) {
    return updateLastRun(timeline, (run) => ({
      ...run,
      status: 'running',
      startedAt: Date.now(),
      endedAt: undefined,
    }))
  }
  const lastItem = timeline[timeline.length - 1]
  const startedAt = lastItem?.kind === 'user' ? lastItem.createdAt : Date.now()
  const run: AgentRun = { id: nextId('run'), status: 'running', steps: [], finalText: '', permissions: [], startedAt }
  return [...timeline, { kind: 'run', id: run.id, run }]
}

/**
 * 手动压缩等「必须独占一个 run」的操作用：先落定任何残留的 running run
 * （正常流程下提交被 running 状态拦截，残留只可能来自丢失的终态事件），
 * 再开新 run，避免压缩状态混进上一轮回复的执行过程。
 */
function ensureFreshRun(timeline: AgentTimeline): AgentTimeline {
  const current = lastRun(timeline)
  if (current && current.run.status === 'running') {
    const closed = updateLastRun(timeline, (run) => closeRun(run, 'done'))
    return ensureRun(closed)
  }
  return ensureRun(timeline)
}

function updateLastRun(timeline: AgentTimeline, updater: (run: AgentRun) => AgentRun): AgentTimeline {
  const current = lastRun(timeline)
  if (!current) return timeline
  const next = timeline.slice()
  next[current.index] = { kind: 'run', id: current.run.id, run: updater(current.run) }
  return next
}

function markRunEndedAt(timeline: AgentTimeline, timestamp?: string): AgentTimeline {
  const endedAt = timestamp ? Date.parse(timestamp) : NaN
  return Number.isFinite(endedAt)
    ? updateLastRun(timeline, (run) => ({ ...run, endedAt }))
    : timeline
}

function finishRunAt(timeline: AgentTimeline, timestamp?: string): AgentTimeline {
  const endedAt = timestamp ? Date.parse(timestamp) : NaN
  if (!Number.isFinite(endedAt)) return timeline
  return updateLastRun(timeline, (run) => run.status === 'running'
    // 已有 endedAt（每个条目到达时标记的最后活动时间）优先：用下一条用户消息的
    // 时间收尾会把两轮之间的空闲间隔也算进用时
    ? closeRun({ ...run, endedAt: run.endedAt ?? endedAt }, 'done')
    : run)
}

function closeRun(run: AgentRun, status: AgentRunStatus, elapsedMs?: number, errorMessage?: string, keepPendingQuestions?: boolean): AgentRun {
  const endedAt = run.endedAt ?? Date.now()
  // 审批等待(含收尾时仍未决的那段)不计入用时
  const pausedMs = (run.pausedMs ?? 0) + (run.pendingSince !== undefined ? Math.max(0, endedAt - run.pendingSince) : 0)
  const phaseElapsedMs = elapsedMs ?? Math.max(0, endedAt - run.startedAt - pausedMs)
  return {
    ...run,
    pendingSince: undefined,
    pausedMs: 0,
    endedAt,
    elapsedMs: (run.elapsedMs ?? 0) + phaseElapsedMs,
    status,
    errorMessage: errorMessage || run.errorMessage,
    permissions: status === 'error' || status === 'cancelled'
      ? run.permissions.map((request) => request.decision ? request : { ...request, decision: 'denied' as const })
      : run.permissions,
    questions: !keepPendingQuestions && (status === 'error' || status === 'cancelled')
      ? (run.questions ?? []).map((question) => (question.status === 'pending' ? { ...question, status: 'cancelled' as const } : question))
      : run.questions,
    steps: run.steps.map((step) => (step.status === 'running' ? { ...step, status: status === 'error' ? 'error' : 'done' } : step)),
  }
}

/** 记录一条权限决定;最后一条未决请求被处理后,结束这段审批等待,用时恢复计时。 */
function applyPermissionDecision(run: AgentRun, requestId: string, decision: 'allowed' | 'denied', options?: { scope?: 'session'; auto?: boolean }): AgentRun {
  const permissions = run.permissions.map((request) => (request.id === requestId
    ? { ...request, decision, ...(options?.scope && decision === 'allowed' ? { scope: options.scope } : {}), ...(options?.auto ? { auto: true } : {}) }
    : request))
  if (run.pendingSince === undefined || permissions.some((request) => !request.decision)) return { ...run, permissions }
  return {
    ...run,
    permissions,
    pausedMs: (run.pausedMs ?? 0) + Math.max(0, Date.now() - run.pendingSince),
    pendingSince: undefined,
  }
}

/** 用户在审批卡片上点了允许/拒绝:乐观更新本地时间线(与服务端的 permission_result 幂等)。 */
export function decidePermission(timeline: AgentTimeline, requestId: string, decision: 'allowed' | 'denied', options?: { scope?: 'session' }): AgentTimeline {
  return timeline.map((item) => (item.kind === 'run' ? { ...item, run: applyPermissionDecision(item.run, requestId, decision, options) } : item))
}

/** 用户在提问卡片上提交/跳过：乐观更新本地时间线（与服务端的 question_answered 幂等）。 */
export function decideQuestion(timeline: AgentTimeline, questionId: string, answers: AgentQuestionAnswer[], skipped?: boolean): AgentTimeline {
  const status = skipped ? 'skipped' : 'answered'
  return timeline.map((item) => (item.kind === 'run' ? { ...item, run: applyQuestionAnswer(item.run, questionId, status, answers) } : item))
}

/** 记录一条提问回答：最后一个未决请求处理完后结束等待，恢复计时（与审批同机制）。 */
function applyQuestionAnswer(run: AgentRun, questionId: string, status: 'answered' | 'skipped' | 'cancelled', answers?: AgentQuestionAnswer[], auto?: boolean): AgentRun {
  const questions = (run.questions ?? []).map((question) => (question.id === questionId
    ? { ...question, status, ...(answers && answers.length ? { answers } : {}), ...(auto ? { auto: true } : {}) }
    : question))
  if (run.pendingSince === undefined || questions.some((question) => question.status === 'pending')) {
    return { ...run, questions }
  }
  return {
    ...run,
    questions,
    pausedMs: (run.pausedMs ?? 0) + Math.max(0, Date.now() - run.pendingSince),
    pendingSince: undefined,
  }
}

/** 运行中的「用时」:等待审批/提问期间冻结，处理后继续累加。 */
export function runElapsedMs(run: AgentRun, now: number): number {
  const paused = (run.pausedMs ?? 0) + (run.pendingSince !== undefined ? Math.max(0, now - run.pendingSince) : 0)
  return (run.elapsedMs ?? 0) + Math.max(0, now - run.startedAt - paused)
}

/** run 是否存在未决的审批或提问（「已暂停等待」判定共用） */
export function pausedForUser(run: AgentRun): boolean {
  return run.permissions.some((request) => request.decision === undefined)
    || (run.questions ?? []).some((question) => question.status === 'pending')
}

export function appendUserMessage(timeline: AgentTimeline, text: string, messageId?: string, createdAt = Date.now(), attachments?: ChatAttachment[], references?: AgentSessionReference[]): AgentTimeline {
  // 上一轮若因丢失终态事件仍停留在 running，先就地落定：否则它的计时会跨轮
  // 继续走，看起来像「上一条消息的用时被算到了新消息的时间」
  const settled = updateLastRun(timeline, (run) => (run.status === 'running' ? closeRun(run, 'done') : run))
  return [...settled, { kind: 'user', id: messageId ?? nextId('user'), messageId, text, createdAt, ...(attachments?.length ? { attachments } : {}), ...(references?.length ? { references } : {}) }]
}

export function replaceFromUserMessage(timeline: AgentTimeline, messageId: string, text: string, attachments?: ChatAttachment[], references?: AgentSessionReference[]): AgentTimeline {
  const index = timeline.findIndex((item) => item.kind === 'user' && (item.messageId === messageId || item.id === messageId))
  if (index < 0) return appendUserMessage(timeline, text, messageId, Date.now(), attachments, references)
  const previous = timeline[index]
  const preservedAttachments = attachments ?? (previous.kind === 'user' ? previous.attachments : undefined)
  const preservedReferences = references ?? (previous.kind === 'user' ? previous.references : undefined)
  return [...timeline.slice(0, index), { kind: 'user', id: messageId, messageId, text, createdAt: Date.now(), ...(preservedAttachments?.length ? { attachments: preservedAttachments } : {}), ...(preservedReferences?.length ? { references: preservedReferences } : {}) }]
}

/** 回复底部「从这里分叉」按钮的可见性：需要这一轮对应的用户消息 id，且会话不在运行中 */
export function canForkFromMessage(messageId: string | undefined | null, running: boolean): boolean {
  return Boolean(messageId) && !running
}

export function canReconcileTimeline(current: AgentTimeline, transcript: AgentTimeline): boolean {
  const currentUsers = current.filter((item): item is Extract<AgentTimeline[number], { kind: 'user' }> => item.kind === 'user')
  const transcriptUsers = transcript.filter((item): item is Extract<AgentTimeline[number], { kind: 'user' }> => item.kind === 'user')
  if (transcriptUsers.length < currentUsers.length) return false
  return currentUsers.every((user, index) => {
    const transcriptUser = transcriptUsers[index]
    if (!transcriptUser || transcriptUser.text !== user.text) return false
    return !user.messageId || !transcriptUser.messageId || user.messageId === transcriptUser.messageId
  })
}

export function reconcileTimeline(current: AgentTimeline, transcript: AgentTimeline): AgentTimeline {
  if (transcript.length < current.length || !canReconcileTimeline(current, transcript)) return current
  const currentRuns = current.filter((item): item is Extract<AgentTimeline[number], { kind: 'run' }> => item.kind === 'run')
  let runIndex = 0
  return transcript.map((item) => {
    if (item.kind !== 'run') return item
    const active = currentRuns[runIndex]
    runIndex += 1
    if (!active) return item
    // 旧服务端在转录里不写 run_error：实时 run 已失败而转录丢失了错误时，保留实时的错误状态与文案
    const keepError = active.run.status === 'error' && Boolean(active.run.errorMessage) && item.run.status !== 'error'
    // 终态时拉到的转录可能还没写入 run_changes:保留实时收到的变更
    const keepChanges = !item.run.changes && Boolean(active.run.changes)
    const liveError = {
      ...(keepError ? { status: active.run.status, errorMessage: active.run.errorMessage } : {}),
      ...(keepChanges ? { changes: active.run.changes } : {}),
    }
    if (active.run.elapsedMs === undefined) return keepError || keepChanges ? { ...item, run: { ...item.run, ...liveError } } : item
    return {
      ...item,
      run: {
        ...item.run,
        startedAt: active.run.startedAt,
        endedAt: active.run.endedAt,
        elapsedMs: active.run.elapsedMs,
        ...liveError,
      },
    }
  })
}

/**
 * 事件流结束但没有收到终态事件（连接被提前关闭/中断）时的兜底：
 * 把仍在 running 的 run 就地落定，让计时停在当前值而不是无限走下去。
 */
export function settleRunningRuns(timeline: AgentTimeline): AgentTimeline {
  return timeline.map((item) => item.kind === 'run' && item.run.status === 'running'
    ? { ...item, run: closeRun(item.run, 'done') }
    : item)
}

export function reduceAgentEvent(timeline: AgentTimeline, event: AgentStreamEvent): AgentTimeline {
  const kind = classifyEvent(event.type, event.data)
  const record = asRecord(event.data)

  switch (kind) {
    case 'other': {
      if (event.type.toLowerCase() === 'agent_start') return ensureRun(timeline)
      return timeline
    }
    case 'text': {
      const text = extractTextChunk(event.data)
      if (!text) return timeline
      const withRun = ensureRun(timeline)
      // finalText 保留全量拼接（复制/重试判断用）；text step 记录与工具步骤的交错顺序
      return updateLastRun(withRun, (run) => {
        const steps = run.steps.slice()
        const last = steps[steps.length - 1]
        if (last?.kind === 'text') {
          steps[steps.length - 1] = { ...last, detail: `${last.detail ?? ''}${text}` }
        } else {
          steps.push({ id: nextId('text'), kind: 'text', title: 'text', detail: text, status: 'done' })
        }
        return { ...run, finalText: run.finalText + text, steps }
      })
    }
    case 'tool_call': {
      // Runner events nest the payload under data.tool_call; flat tool_* events use data directly.
      const payload = asRecord(record?.tool_call) ?? record
      const callId = pickId(payload, ['id', 'tool_call_id', 'call_id'])
      const normalizedType = event.type.toLowerCase()
      const argsDone = !normalizedType.includes('start')
      const input = pickDetail(payload, ['raw_arguments', 'arguments', 'args', 'input', 'command']) || undefined
      if (lastRun(timeline)?.run.steps.some((step) => step.id === callId && callId)) {
        // Completion event (tool_call): backfill the argument detail.
        return updateLastRun(timeline, (run) => ({
          ...run,
          steps: run.steps.map((step) => (step.id === callId
            ? { ...step, input: input ?? step.input, argsDone: argsDone || step.argsDone }
            : step)),
        }))
      }
      const withRun = ensureRun(timeline)
      const step: AgentWorkStep = {
        id: callId || nextId('step'),
        kind: 'tool',
        title: pickString(payload, ['name', 'tool', 'tool_name', 'title']) || 'tool',
        input,
        argsDone,
        status: 'running',
      }
      return updateLastRun(withRun, (run) => ({ ...run, steps: [...run.steps, step] }))
    }
    case 'tool_call_delta': {
      const delta = pickDelta(record)
      if (!delta) return timeline
      const toolId = pickId(record, ['tool_call_id', 'call_id', 'id'])
      return updateLastRun(timeline, (run) => {
        const index = toolId
          ? run.steps.findIndex((step) => step.kind === 'tool' && step.id === toolId)
          : run.steps.map((step, position) => (step.kind === 'tool' && step.argsDone === false ? position : -1)).filter((position) => position >= 0).pop() ?? -1
        if (index < 0) return run
        const steps = run.steps.slice()
        const step = steps[index]!
        steps[index] = { ...step, input: `${step.input ?? ''}${delta}`, argsDone: false }
        return { ...run, steps }
      })
    }
    case 'tool_result': {
      // Runner events nest the payload under data.tool_result.
      const payload = asRecord(record?.tool_result) ?? record
      const withRun = ensureRun(timeline)
      const stepId = pickId(payload, ['tool_call_id', 'call_id', 'step_id', 'id'])
      const detail = pickDetail(payload, ['output', 'result', 'content', 'detail']) || extractEventText(payload) || undefined
      const isError = /error|fail/i.test(event.type) || payload?.is_error === true || payload?.ok === false
      const images = Array.isArray(payload?.images) && payload.images.length ? payload.images as AgentGeneratedImage[] : undefined
      const patch = typeof payload?.details_patch === 'string' ? payload.details_patch : undefined
      return updateLastRun(withRun, (run) => {
        const index = stepId
          ? run.steps.findIndex((step) => step.id === stepId)
          : run.steps.map((step, position) => (step.status === 'running' ? position : -1)).filter((position) => position >= 0).pop() ?? -1
        if (index < 0) {
          const step: AgentWorkStep = { id: stepId || nextId('step'), kind: 'tool', title: pickString(payload, ['name', 'tool', 'tool_name']) || 'tool', output: detail, images, patch, detail, argsDone: true, status: isError ? 'error' : 'done' }
          return { ...run, steps: [...run.steps, step] }
        }
        const steps = run.steps.slice()
        steps[index] = { ...steps[index], output: detail ?? steps[index].output, images: images ?? steps[index].images, patch: patch ?? steps[index].patch, detail: detail ?? steps[index].detail, argsDone: true, status: isError ? 'error' : 'done' }
        return { ...run, steps }
      })
    }
    case 'thinking': {
      // 思考内容不进时间线：只展示正文与工具调用，thinking 是过程噪音
      return timeline
    }
    case 'notice': {
      // 停滞/预算等运行提醒属于内部机制，不展示给用户（与转录回放对 agent_notice 的处理一致）
      return timeline
    }
    case 'compaction': {
      const normalized = event.type.toLowerCase()
      // 压缩开始：放一个 running 状态步骤（manual 标记来自前端手动 /compact 流程）
      if (normalized.includes('compacting')) {
        const manual = record?.manual === true
        const withRun = manual ? ensureFreshRun(timeline) : ensureRun(timeline)
        const step: AgentWorkStep = {
          id: nextId('status'),
          kind: 'status',
          title: manual ? 'compacting_manual' : 'compacting',
          status: 'running',
        }
        return updateLastRun(withRun, (run) => ({ ...run, steps: [...run.steps, step] }))
      }
      // 压缩收尾：回填最近一个 running 的压缩步骤；失败（无 compaction 载荷）也落定。
      // 只展示状态文本本身，不带 token 明细
      const finished: Omit<AgentWorkStep, 'id'> = {
        kind: 'status',
        title: asRecord(record?.compaction) ? 'compacted' : 'compaction_skipped',
        status: 'done',
      }
      const withRun = ensureRun(timeline)
      return updateLastRun(withRun, (run) => {
        const index = run.steps.map((step, position) => (step.kind === 'status' && (step.title === 'compacting' || step.title === 'compacting_manual') && step.status === 'running' ? position : -1)).filter((position) => position >= 0).pop() ?? -1
        const completed: AgentWorkStep = { id: index >= 0 ? run.steps[index]!.id : nextId('status'), ...finished }
        if (index < 0) return { ...run, steps: [...run.steps, completed] }
        const steps = run.steps.slice()
        steps[index] = completed
        return { ...run, steps }
      })
    }
    case 'permission': {
      const withRun = ensureRun(timeline)
      // escalation_request nests its payload under data.escalation; flat permission_* events use data directly.
      const payload = asRecord(record?.escalation) ?? record
      const command = payload?.requested_command
      return updateLastRun(withRun, (run) => {
        const request: AgentPermissionRequest = {
          id: pickId(payload, ['escalation_id', 'request_id', 'permission_id', 'id']) || nextId('permission'),
          tool: pickString(payload, ['tool_name', 'tool', 'name', 'title']) || 'tool',
          detail: Array.isArray(command)
            ? command.map(String).join(' ')
            : pickDetail(payload, ['requested_command', 'requested_path', 'arguments', 'args', 'input', 'command', 'description', 'message']) || undefined,
          note: pickString(payload, ['note']) || undefined,
          resolvedPath: pickString(payload, ['resolved_path']) || undefined,
          position: run.steps.length,
        }
        return { ...run, permissions: [...run.permissions, request], pendingSince: run.pendingSince ?? Date.now() }
      })
    }
    case 'question': {
      const question = parseQuestionRequest(record)
      if (!question) return timeline
      const withRun = ensureRun(timeline)
      return updateLastRun(withRun, (run) => {
        // 按 question_id 去重：SSE 重放会再推一次已有的 pending 提问
        if (run.questions?.some((existing) => existing.id === question.id)) return run
        return {
          ...run,
          questions: [...(run.questions ?? []), { ...question, position: run.steps.length }],
          pendingSince: run.pendingSince ?? Date.now(),
        }
      })
    }
    case 'question_result': {
      const questionId = pickId(record, ['question_id', 'request_id', 'id'])
      if (!questionId) return timeline
      const answers = parseQuestionAnswers(record)
      const status = record?.cancelled === true
        ? 'cancelled'
        : record?.skipped === true
          ? 'skipped'
          : 'answered'
      const auto = record?.auto === true
      return updateLastRun(timeline, (run) => applyQuestionAnswer(run, questionId, status, answers, auto))
    }
    case 'permission_result': {
      const requestId = pickId(record, ['request_id', 'permission_id', 'id'])
      const decision = record?.allowed === true || record?.decision === 'allow' || record?.decision === 'allowed' ? 'allowed' : 'denied'
      // 会话内允许自动放行的 permission_result 带 auto:true，展示为「已自动允许(会话内)」
      const auto = record?.auto === true
      return updateLastRun(timeline, (run) => applyPermissionDecision(run, requestId, decision, auto ? { auto: true, scope: 'session' } : undefined))
    }
    case 'done':
      // 用墙钟收束（用户发出消息 → 最后输出结束），不用 runner 的 elapsed_ms：
      // closeRun 会按 endedAt??now 计算，并对暂停/恢复的分段累加
      return updateLastRun(timeline, (run) => closeRun(run, 'done'))
    case 'error':
      return updateLastRun(timeline, (run) => closeRun(run, 'error', undefined, pickString(record, ['message'])))
    case 'cancelled':
      return updateLastRun(timeline, (run) => closeRun(run, 'cancelled', undefined, pickString(record, ['message'])))
    case 'run_changes':
      return attachRunChanges(timeline, parseRunChanges(record))
    default:
      return timeline
  }
}

export function timelineFromTranscript(entries: AgentTranscriptEntry[]): AgentTimeline {
  // Fixed receipt text written on interruption teardown (xlyra-agent sealPendingToolCalls).
  const INTERRUPTED_SEAL = '操作已被中断，工具未执行完成。'
  const AWAITING_SEAL = '等待用户授权，工具暂停执行。'
  let timeline: AgentTimeline = []
  // When the turn contains an escalation, the "interrupted" receipt is actually a pause; downgrade it to neutral.
  let escalationInTurn = false
  const reduceAt = (event: AgentStreamEvent, timestamp?: string) => {
    timeline = markRunEndedAt(reduceAgentEvent(timeline, event), timestamp)
  }
  for (const entry of entries) {
    if (entry.type === 'compaction') {
      // 先收束前一个 run（assistant 回复），再开独立的压缩 run：
      // 压缩是顶层事件，不混进上一轮回复的执行过程
      timeline = updateLastRun(timeline, (run) => (run.status === 'running' ? closeRun(run, 'done') : run))
      timeline = markRunEndedAt(appendStatusStep(timeline, 'compacted'), entry.timestamp)
      continue
    }
    if (entry.type === 'run_error') {
      // 失败的 run 在转录里单独落一条；若这一轮还没写入任何内容（没有 run），补一个空 run 承载错误
      const withRun = markRunEndedAt(ensureRun(timeline), entry.timestamp)
      const errorMessage = typeof entry.message === 'string' ? entry.message : undefined
      timeline = updateLastRun(withRun, (run) => closeRun(run, 'error', undefined, errorMessage))
      continue
    }
    if (entry.type === 'run_changes') {
      timeline = attachRunChanges(timeline, parseRunChanges(entry as Record<string, unknown>))
      continue
    }
    if (entry.type === 'escalation') {
      // Escalations render as permission entries; granted null means undecided (stays interactive).
      const withRun = ensureRun(timeline)
      const command = entry.requested_command
      timeline = markRunEndedAt(updateLastRun(withRun, (run) => {
        const request: AgentPermissionRequest = {
          id: entry.escalation_id || nextId('permission'),
          tool: entry.tool_name || 'tool',
          detail: Array.isArray(command) && command.length
            ? command.join(' ')
            : entry.requested_path || undefined,
          note: entry.note || undefined,
          resolvedPath: entry.resolved_path || undefined,
          decision: entry.granted === true ? 'allowed' : entry.granted === false ? 'denied' : undefined,
          ...(entry.granted === true && entry.scope === 'session' ? { scope: 'session' as const } : {}),
          ...(entry.auto === true ? { auto: true } : {}),
          position: run.steps.length,
        }
        return { ...run, permissions: [...run.permissions, request] }
      }), entry.timestamp)
      escalationInTurn = true
      continue
    }
    if (entry.type === 'question') {
      // 转录里的 question 条目：未回答(无 answers/skipped/cancelled) → pending，仍可交互
      const items = parseQuestionItems(entry.questions)
      if (items.length === 0) continue
      const withRun = ensureRun(timeline)
      const answered = entry.answers !== undefined || entry.skipped === true || entry.cancelled === true
      const status: AgentQuestion['status'] = entry.cancelled === true
        ? 'cancelled'
        : entry.skipped === true
          ? 'skipped'
          : answered
            ? 'answered'
            : 'pending'
      timeline = markRunEndedAt(updateLastRun(withRun, (run) => ({
        ...run,
        questions: [...(run.questions ?? []), {
          id: entry.question_id || nextId('question'),
          toolCallId: entry.tool_call_id ?? '',
          items,
          ...(Array.isArray(entry.answers) ? { answers: entry.answers } : {}),
          status,
          ...(entry.auto === true ? { auto: true } : {}),
          position: run.steps.length,
        }],
        ...(status === 'pending' && run.pendingSince === undefined ? { pendingSince: parseTimestamp(entry.timestamp) } : {}),
      })), entry.timestamp)
      continue
    }
    const message = entry.message
    if (message && typeof message === 'object') {
      // The runner's transcript format: { type: 'message', message: ChatMessage, ... }
      if (message.role === 'user') {
        // agent 注入的停滞/预算提醒属于内部机制，前端不展示；也不结束当前 run
        if (message.name === 'agent_notice') continue
        timeline = finishRunAt(timeline, entry.timestamp)
        if (message.content || message.attachments?.length) {
          const attachments = message.attachments?.map((attachment, index) => ({
            id: `${entry.message_id ?? entry.timestamp ?? 'attachment'}-${index}`,
            name: attachment.name,
            mimeType: attachment.mime_type,
            size: attachment.data_url ? Math.max(0, Math.floor((attachment.data_url.length * 3) / 4)) : 0,
            ...(attachment.data_url ? { dataURL: attachment.data_url } : {}),
          }))
          timeline = appendUserMessage(timeline, message.content ?? '', entry.message_id, parseTimestamp(entry.timestamp), attachments, parseTranscriptReferences(transcriptReferences(entry)))
        }
        escalationInTurn = false
        continue
      }
      if (message.role === 'system') continue
      if (message.role === 'tool') {
        // Backward compat: "interrupted" receipts written before an escalation pause downgrade to neutral (not failed).
        const pausedByEscalation = escalationInTurn && message.is_error === true && message.content === INTERRUPTED_SEAL
        reduceAt({
          type: 'tool_result',
          data: {
            tool_call_id: message.tool_call_id,
            name: message.name,
            content: pausedByEscalation ? AWAITING_SEAL : message.content,
            is_error: pausedByEscalation ? false : message.is_error,
            images: message.images,
            details_patch: message.details_patch,
          },
        }, entry.timestamp)
        continue
      }
      // Assistant: text → tool calls，按消息内的自然顺序（文本在工具调用之前）恢复；thinking 不展示
      if (message.content) reduceAt({ type: 'message', data: message.content }, entry.timestamp)
      for (const call of message.tool_calls ?? []) {
        reduceAt({
          type: 'tool_call',
          data: { tool_call_id: call.id, name: call.name, arguments: call.raw_arguments },
        }, entry.timestamp)
        // Tool calls in a historical transcript are necessarily finished; a synthetic tool_result closes the step.
        reduceAt({ type: 'tool_result', data: { tool_call_id: call.id, name: call.name } }, entry.timestamp)
      }
      continue
    }
    // Compat with the early flat format: { role, content / payload }.
    const role = entry.role ?? ''
    const type = entry.type ?? ''
    const data = typeof entry.content === 'string' && entry.content
      ? entry.content
      : entry.payload ?? entry.content
    if (role === 'user') {
      timeline = finishRunAt(timeline, entry.timestamp)
      const text = typeof data === 'string' ? data : extractEventText(data)
      if (text) {
        const timestamp = parseTimestamp(entry.timestamp)
        timeline = appendUserMessage(timeline, text, entry.message_id, Number.isFinite(timestamp) ? timestamp : undefined, undefined, parseTranscriptReferences(transcriptReferences(entry)))
      }
      continue
    }
    const eventType = type || (role === 'tool' ? 'tool_result' : 'message')
    reduceAt({ type: eventType, data }, entry.timestamp)
  }
  return timeline.map((item) => {
    if (item.kind !== 'run' || item.run.status !== 'running') return item
    // 转录里未回答的提问仍可交互（刷新恢复），不能因收尾被标成 cancelled
    const status = item.run.permissions.some((request) => request.decision === undefined) ? 'cancelled' : 'done'
    return { ...item, run: closeRun(item.run, status, undefined, undefined, true) }
  })
}

function appendStatusStep(timeline: AgentTimeline, title: string, detail?: string): AgentTimeline {
  const withRun = ensureRun(timeline)
  const step: AgentWorkStep = { id: nextId('status'), kind: 'status', title, detail, status: 'done' }
  return updateLastRun(withRun, (run) => ({ ...run, steps: [...run.steps, step] }))
}
