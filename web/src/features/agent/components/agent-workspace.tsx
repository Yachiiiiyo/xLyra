import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router-dom'
import { Folder, Globe, Link2, Menu, ShieldCheck, ShieldQuestion, TriangleAlert, TerminalSquare, X } from 'lucide-react'
import { ThinkingOrb, type OrbState } from 'thinking-orbs'
import { Composer } from '@/components/chat/composer'
import { ChatAttachmentItem } from '@/components/chat/chat-attachment'
import { AttachmentMenu } from '@/components/chat/attachment-menu'
import { ReasoningSettingsPicker } from '@/components/chat/reasoning-settings-picker'
import { normalizeReasoningEffort, parseGatewayModelReasoning } from '@/lib/reasoning'
import { attachmentMimeType, normalizeAttachmentDataURL } from '@/lib/attachments'
import type { ChatAttachment, GatewayModel, ReasoningEffort } from '@/lib/chat-types'
import { APIError } from '@/lib/http'
import { newId } from '@/lib/id'
import { AgentSidebar } from '@/features/agent/components/agent-sidebar'
import { AgentSettingsDialog } from '@/features/agent/components/agent-settings-dialog'
import { AgentReferenceDialog } from '@/features/agent/components/agent-reference-dialog'
import { AgentTimeline } from '@/features/agent/components/agent-timeline'
import { Button } from '@/components/ui/button'
import { AppLogo } from '@/components/common/app-logo'
import { JumpToLatestButton } from '@/components/common/jump-to-latest-button'
import { useStickToBottom } from '@/hooks/use-stick-to-bottom'
import { useMobileLayout } from '@/hooks/use-media-query'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { LiquidGlass } from '@/components/glass/liquid-glass'
import { AgentBackdrop } from '@/features/agent/components/agent-backdrop'
import './agent-page.css'
import {
  appendUserMessage,
  reconcileTimeline,
  replaceFromUserMessage,
  reduceAgentEvent,
  decidePermission,
  settleRunningRuns,
  timelineFromTranscript,
  type AgentPermissionRequest,
  type AgentRun,
  type AgentTimeline as AgentTimelineData,
} from '@/features/agent/lib/agent-events'
import {
  AGENT_COMPACT_COMMAND,
  compactAgentSession,
  createAgentSession,
  deleteAgentSession,
  fetchAgentAvailableModels,
  fetchAgentHealth,
  fetchAgentModelMemory,
  fetchAgentRuntimeSettings,
  fetchAgentTranscript,
  followAgentEvents,
  forkAgentSession,
  grantAgentAccess,
  listAgentSessions,
  retryAgentSession,
  revokeAgentAccess,
  stopAgentSession,
} from '@/features/agent/api/agent'
import { agentSettingsKey } from '@/features/agent/lib/use-agent-availability'
import { MAX_SESSION_REFERENCES, referenceChipLabel, type AgentSessionReference } from '@/features/agent/lib/references'
import { dismissCapabilityNotice, loadCapabilityNoticeDismissed, loadLastSessionId, saveLastSessionId } from '@/features/agent/lib/agent-local-cache'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

const MAX_ATTACHMENTS = 4
const MAX_ATTACHMENT_BYTES = 5 * 1024 * 1024
const NON_CHAT_CATEGORIES = new Set(['image', 'audio', 'embedding'])
const PERMISSION_MODE_KEY = 'xlyra-agent-permission-mode'
/** transcript 走 React Query 缓存：staleTime 内切回已看过的会话直接命中缓存秒开 */
const TRANSCRIPT_STALE_TIME = 30_000
/** sessions 列表加载后后台预取 transcript 的最近会话数 */
const TRANSCRIPT_PREFETCH_COUNT = 5

const agentTranscriptKey = (sessionId: string) => ['agent', 'transcript', sessionId] as const

type PermissionMode = 'ask' | 'full'

type EditReplaySnapshot = {
  timeline: AgentTimelineData
  draft: string
  attachments: ChatAttachment[]
  attachmentError: string | null
  references: AgentSessionReference[]
  editingMessage: { messageId: string } | null
}

function loadPermissionMode(): PermissionMode {
  try {
    return window.localStorage.getItem(PERMISSION_MODE_KEY) === 'full' ? 'full' : 'ask'
  } catch {
    return 'ask'
  }
}

function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error ?? new Error('failed to read attachment'))
    reader.readAsDataURL(file)
  })
}

/** Placeholder shown while awaiting an agent reply: pulsing caret, label, live elapsed time. */
function orbStateForRun(run?: AgentRun): OrbState {
  const activeStep = run ? [...run.steps].reverse().find((step) => step.status === 'running') : undefined
  if (!activeStep) return 'breathing'
  if (activeStep.kind !== 'tool') return 'working'
  const name = activeStep.title.toLowerCase()
  if (name.includes('search') || name.includes('grep') || name.includes('find') || name.includes('query')) return 'searching'
  if (name.includes('fetch') || name.includes('http') || name.includes('web') || name.includes('browse') || name.includes('url') || name.includes('net')) return 'connecting'
  return 'working'
}

/** Placeholder shown while awaiting an agent reply: pulsing orb + label. 时长由 run 的「用时」行承担，这里不再重复计时。 */
function WaitingIndicator({ state }: { state: OrbState }) {
  const { t } = useTranslation(['agent', 'chat'])
  const statusLabel = state === 'breathing'
    ? t('agent:work.thinking')
    : state === 'searching'
      ? t('agent:work.searching')
      : state === 'connecting'
        ? t('agent:work.connecting')
        : t('agent:work.working')
  return (
    <div className="mt-4 flex items-center gap-2 text-xs text-faint">
      <ThinkingOrb state={state} size={20} theme="dark" aria-label={statusLabel} className="shrink-0" />
      <span>{statusLabel}</span>
    </div>
  )
}

/** Confirmation shown before enabling full-access permission mode. */
function FullAccessConfirmDialog({
  open,
  onCancel,
  onConfirm,
}: {
  open: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation('agent')
  const items = [
    { icon: Folder, title: t('composer.fullConfirmFiles'), description: t('composer.fullConfirmFilesDesc') },
    { icon: TerminalSquare, title: t('composer.fullConfirmCommands'), description: t('composer.fullConfirmCommandsDesc') },
    { icon: Globe, title: t('composer.fullConfirmNetwork'), description: t('composer.fullConfirmNetworkDesc') },
  ]
  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="sm" presentation="center" className="w-[min(92vw,480px)]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <TriangleAlert className="h-4 w-4 text-amber-500" />
            {t('composer.fullConfirmTitle')}
          </DialogTitle>
          <DialogDescription>{t('composer.fullConfirmDescription')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div className="space-y-1 rounded-xl border border-[hsl(var(--glass-border))] bg-[hsl(var(--surface-subtle))]/60 p-2">
            {items.map((item) => (
              <div key={item.title} className="flex items-start gap-3 rounded-lg px-3 py-2.5">
                <item.icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-soft" />
                <div className="min-w-0">
                  <p className="text-sm font-medium text-foreground">{item.title}</p>
                  <p className="mt-0.5 text-xs leading-5 text-muted-soft">{item.description}</p>
                </div>
              </div>
            ))}
          </div>
          <p className="text-xs leading-5 text-muted-soft">{t('composer.fullConfirmRisk')}</p>
        </DialogBody>
        <DialogFooter
          cancel={<Button variant="outline" onClick={onCancel}>{t('composer.fullConfirmCancel')}</Button>}
          confirm={<Button variant="destructive" onClick={onConfirm}>{t('composer.fullConfirmAccept')}</Button>}
        />
      </DialogContent>
    </Dialog>
  )
}

function EditReplayConfirmDialog({
  open,
  onCancel,
  onConfirm,
}: {
  open: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation('agent')
  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onCancel() }}>
      <DialogContent size="sm" presentation="center" className="w-[min(92vw,440px)]">
        <DialogHeader>
          <DialogTitle>{t('chat.editReplayTitle')}</DialogTitle>
          <DialogDescription>{t('chat.editReplayDescription')}</DialogDescription>
        </DialogHeader>
        <DialogFooter
          cancel={<Button variant="outline" onClick={onCancel}>{t('chat.editReplayCancel')}</Button>}
          confirm={<Button onClick={onConfirm}>{t('chat.editReplayConfirm')}</Button>}
        />
      </DialogContent>
    </Dialog>
  )
}

export function AgentWorkspace() {
  const { t } = useTranslation(['agent', 'chat'])
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const sessionsQuery = useQuery({ queryKey: ['agent', 'sessions'], queryFn: listAgentSessions, retry: false })
  // Model memory lives server-side: a fresh session defaults to the last globally used model, an existing session to its own last model.
  const modelMemoryQuery = useQuery({ queryKey: ['agent', 'model-memory'], queryFn: fetchAgentModelMemory, retry: false })
  const availableModelsQuery = useQuery({
    queryKey: [...agentSettingsKey, 'workspace-available-models'],
    queryFn: async () => {
      const [{ allowed_site_ids: allowedSites, allowed_site_model_ids: allowedModels }, availableSites] = await Promise.all([fetchAgentRuntimeSettings(), fetchAgentAvailableModels()])
      return { allowedSites, allowedModels, availableSites }
    },
    retry: false,
    refetchOnMount: 'always',
    refetchOnWindowFocus: 'always',
  })
  const healthQuery = useQuery({ queryKey: [...agentSettingsKey, 'health'], queryFn: fetchAgentHealth, retry: false, staleTime: 30_000, refetchInterval: 30_000 })

  // 刷新后恢复上次会话：transcript 请求与 sessions/settings 并行发出，不等页面渲染完
  const [activeId, setActiveId] = useState<string | null>(() => loadLastSessionId())
  const [newSession, setNewSession] = useState(false)
  const [timeline, setTimeline] = useState<AgentTimelineData>([])
  const [draft, setDraft] = useState('')
  const [editingMessage, setEditingMessage] = useState<{ messageId: string } | null>(null)
  const [editReplayConfirmation, setEditReplayConfirmation] = useState<{ messageId: string; content: string } | null>(null)
  const [model, setModel] = useState<string | null>(null)
  const [effort, setEffort] = useState<ReasoningEffort>('high')
  const [permissionMode, setPermissionMode] = useState<PermissionMode>(loadPermissionMode)
  const [confirmFullAccess, setConfirmFullAccess] = useState(false)
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])
  const [attachmentError, setAttachmentError] = useState<string | null>(null)
  const [references, setReferences] = useState<AgentSessionReference[]>([])
  const [referenceOpen, setReferenceOpen] = useState(false)
  const [forking, setForking] = useState(false)
  const [running, setRunning] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [capabilityNoticeDismissed, setCapabilityNoticeDismissed] = useState(loadCapabilityNoticeDismissed)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const eventAbort = useRef<AbortController | null>(null)
  const retryTimelineBeforeEdit = useRef<EditReplaySnapshot | null>(null)
  // A just-created session usually has no transcript persisted on the runner yet;
  // skip the reload that follows so the optimistically appended timeline survives.
  const skipTranscriptLoadFor = useRef<string | null>(null)
  // 流式贴底跟随：贴底时内容增长自动滚到底，用户滚离阅读时静默不打扰；
  // 打开历史会话 / 发送 / 重放编辑时调用 scrollToBottom 强制跳到底部看最新
  const { scrollRef, scrollToBottom, showJump } = useStickToBottom()
  const isMobileLayout = useMobileLayout()

  const sessions = sessionsQuery.data ?? []
  // 恢复的会话 id 可能已被删除：sessions 列表返回后校验，失效则回新聊天页
  const restoredIdInvalid = Boolean(sessionsQuery.data && activeId && !sessionsQuery.data.some((session) => session.session_id === activeId))
  // Stay on the new-chat screen by default; only an explicit selection enters a session.
  const selectedId = newSession || restoredIdInvalid ? null : activeId
  useEffect(() => {
    if (restoredIdInvalid) saveLastSessionId(null)
  }, [restoredIdInvalid])
  // sessions 列表就绪后后台预取最近会话的 transcript，点开即命中缓存
  const sessionsData = sessionsQuery.data
  useEffect(() => {
    for (const session of (sessionsData ?? []).slice(0, TRANSCRIPT_PREFETCH_COUNT)) {
      void queryClient.prefetchQuery({
        queryKey: agentTranscriptKey(session.session_id),
        queryFn: () => fetchAgentTranscript(session.session_id),
        staleTime: TRANSCRIPT_STALE_TIME,
      })
    }
  }, [sessionsData, queryClient])
  const activeSession = selectedId ? sessions.find((session) => session.session_id === selectedId) : undefined
  const hasMessages = timeline.length > 0
  // 自定义背景预留：将来由接口层提供背景图 URL；当前恒为 null，页面露出全局星点背景
  const backdropImage: string | null = null

  const gatewayModels = useMemo<GatewayModel[]>(() => {
    const data = availableModelsQuery.data
    if (!data) return []
    const models = new Map<string, GatewayModel>()
    for (const site of data.availableSites) {
      if (!site.enabled || (data.allowedSites.length > 0 && !data.allowedSites.includes(site.site_id))) continue
      for (const item of site.models) {
        if ((data.allowedModels.length > 0 && !data.allowedModels.includes(item.id)) || NON_CHAT_CATEGORIES.has(item.category ?? '')) continue
        const id = (item.model_key || item.upstream_model_name).trim()
        if (!id) continue
        if (!models.has(id)) {
          models.set(id, {
            id,
            displayName: item.display_name || id,
            category: item.category ?? 'chat',
            endpointTypes: [],
            reasoning: parseGatewayModelReasoning(item.reasoning_effort),
          })
        }
      }
    }
    return [...models.values()].sort((left, right) => left.id.localeCompare(right.id, undefined, { sensitivity: 'base' }))
  }, [availableModelsQuery.data])

  const effectiveModel = useMemo(() => {
    const available = (id: string | null | undefined) => Boolean(id && gatewayModels.some((item) => item.id === id))
    // Manual pick > session last used > global last used > first available.
    if (available(model)) return model
    const remembered = selectedId ? modelMemoryQuery.data?.sessions[selectedId] : undefined
    if (available(remembered)) return remembered ?? null
    if (available(modelMemoryQuery.data?.default_model)) return modelMemoryQuery.data?.default_model ?? null
    return gatewayModels[0]?.id ?? null
  }, [gatewayModels, model, modelMemoryQuery.data, selectedId])

  const followSession = useCallback(async (sessionId: string) => {
    eventAbort.current?.abort()
    const controller = new AbortController()
    eventAbort.current = controller
    // 流式 delta 以 markdown 全量重解析实现：同一渲染帧内密集到达的多个 delta
    // 合并成一次 setTimeline，避免每 token 触发一次组件重渲染 + 一次 ReactMarkdown
    // 解析造成卡顿。delta 按到达顺序在 buffer 里累积，flush 时一次性 reduce，语义不变。
    const pending: { type: string; data: unknown }[] = []
    let frame: number | null = null
    let fallback: number | null = null
    const flush = () => {
      if (frame !== null) {
        cancelAnimationFrame(frame)
        frame = null
      }
      if (fallback !== null) {
        window.clearTimeout(fallback)
        fallback = null
      }
      if (pending.length === 0) return
      const events = pending.splice(0, pending.length)
      setTimeline((current) => events.reduce(reduceAgentEvent, current))
    }
    const enqueue = (event: { type: string; data: unknown }, flushNow: boolean) => {
      pending.push(event)
      if (frame !== null || fallback !== null) return
      if (flushNow) {
        flush()
        return
      }
      // rAF 在标签页隐藏/窗口被完全遮挡时不再触发，delta 会全程积压在 buffer 里，
      // 回到前台时「一次性吐出来」。并挂一个超时兜底：可见时 rAF 先触发（兜底被取消），
      // 不可见时由节流后的定时器推进，界面始终按节奏增量渲染
      frame = requestAnimationFrame(flush)
      fallback = window.setTimeout(flush, 100)
    }
    try {
      await followAgentEvents(sessionId, controller.signal, (event) => {
        const terminal = ['agent_done', 'agent_error', 'agent_cancelled'].includes(event.type)
        // 终态事件立即落盘，避免收尾逻辑被 frame 调度延迟；delta 交给下一帧合并
        enqueue(event, terminal)
        if (terminal) {
          setRunning(false)
          void queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
          // On terminal status, reconcile the timeline against the authoritative
          // transcript (covers events missed before the SSE connection); keep the
          // current timeline when the transcript lags behind.
          void fetchAgentTranscript(sessionId).then((entries) => {
            // 终态 transcript 是权威结果，回写缓存：之后切回该会话直接命中
            queryClient.setQueryData(agentTranscriptKey(sessionId), entries)
            setTimeline((current) => {
              const fromTranscript = timelineFromTranscript(entries)
              return reconcileTimeline(current, fromTranscript)
            })
          }).catch(() => undefined)
        }
      })
    } catch {
      if (!controller.signal.aborted) {
        setRunning(false)
        // 流异常中断且没有终态事件：先落掉在途 delta，再收尾残留 running 的 run
        flush()
        setTimeline((current) => settleRunningRuns(current))
      }
      return
    }
    // 流正常结束但没有终态事件（连接被服务端提前关闭等）：同样落定，防止计时无限走
    flush()
    setTimeline((current) => settleRunningRuns(current))
  }, [queryClient])

  useEffect(() => {
    if (!selectedId) return
    if (skipTranscriptLoadFor.current) {
      const skip = skipTranscriptLoadFor.current === selectedId
      skipTranscriptLoadFor.current = null
      if (skip) return
    }
    let cancelled = false
    // 走 queryClient 缓存：staleTime 内切回已看过的会话无需等网络；点击时的 prefetch
    // 与这里的 fetchQuery 按 key 去重，同一时刻最多一个在途请求
    void Promise.all([
      queryClient.fetchQuery({
        queryKey: agentTranscriptKey(selectedId),
        queryFn: () => fetchAgentTranscript(selectedId),
        staleTime: TRANSCRIPT_STALE_TIME,
      }),
      // running 判断依赖 sessions 列表：刷新恢复会话时 transcript 可能先于列表返回，
      // 用空列表判断会漏掉 SSE 恢复。ensureQueryData 与 sessionsQuery 的在途请求
      // 按 key 去重；列表已缓存时零延迟，失败时回退当前闭包里的列表
      queryClient.ensureQueryData({ queryKey: ['agent', 'sessions'], queryFn: listAgentSessions, retry: false }).catch(() => sessions),
    ]).then(([entries, sessionList]) => {
      if (cancelled) return
      const built = timelineFromTranscript(entries)
      // Selecting a still-running session resumes the SSE follow. The event
      // registry is per-run, so reconnecting replays the current run from its
      // start; trim the timeline to the latest user message to avoid duplicating
      // what the transcript already persisted (user messages are not in the
      // event stream, so that entry is kept).
      const isRunning = sessionList.find((item) => item.session_id === selectedId)?.running
      if (isRunning) {
        let lastUserIndex = -1
        built.forEach((item, index) => { if (item.kind === 'user') lastUserIndex = index })
        setTimeline(lastUserIndex >= 0 ? built.slice(0, lastUserIndex + 1) : [])
        setRunning(true)
        void followSession(selectedId)
        return
      }
      setTimeline(built)
    }).catch(() => { if (!cancelled) setTimeline([]) })
    return () => { cancelled = true }
  }, [selectedId, followSession, queryClient]) // eslint-disable-line react-hooks/exhaustive-deps -- sessions only gates the running check; following is idempotent

  useEffect(() => () => eventAbort.current?.abort(), [])

  /** 手动压缩：识别 /compact 指令——不冒泡为用户消息，直接展示压缩状态并调用压缩接口 */
  const compactMutation = useMutation({
    mutationFn: (sessionId: string) => compactAgentSession(sessionId, effectiveModel ?? undefined),
    onSuccess: async (data) => {
      setTimeline((current) => reduceAgentEvent(reduceAgentEvent(current, {
        type: 'context_compacted',
        data: { compaction: { summary: data.summary, tokens_before: data.tokens_before, tokens_after: data.tokens_after } },
      }), { type: 'agent_done', data: {} }))
      await queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
    },
    onError: (error) => {
      setTimeline((current) => reduceAgentEvent(current, { type: 'agent_error', data: {} }))
      toast.error(t('agent:chat.compactFailed'), { description: error.message })
    },
  })

  const sendMutation = useMutation({
    mutationFn: (input: { content: string; files: ChatAttachment[]; references: string[] }) => createAgentSession({
      content: input.content,
      model: effectiveModel ?? undefined,
      session_id: selectedId ?? undefined,
      reasoning_effort: normalizeReasoningEffort(gatewayModels.find((item) => item.id === effectiveModel) ?? null, effort),
      permission_mode: permissionMode,
      attachments: input.files.length
        ? input.files.map((file) => ({ name: file.name, mime_type: file.mimeType, data_url: file.dataURL }))
        : undefined,
      references: input.references.length ? input.references : undefined,
    }),
    onSuccess: async ({ session_id, message_id }) => {
      skipTranscriptLoadFor.current = session_id
      saveLastSessionId(session_id)
      setActiveId(session_id)
      setNewSession(false)
      setRunning(true)
      setReferences([])
      if (message_id) {
        setTimeline((current) => {
          const next = current.slice()
          for (let index = next.length - 1; index >= 0; index -= 1) {
            const item = next[index]
            if (item?.kind !== 'user') continue
            next[index] = { ...item, id: message_id, messageId: message_id }
            break
          }
          return next
        })
      }
      await queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
      void followSession(session_id)
    },
    onError: (error) => {
      setRunning(false)
      toast.error(t('agent:chat.sendFailed'), { description: error.message })
    },
  })

  const retryMutation = useMutation({
    mutationFn: (input: { messageId: string; content: string; files: ChatAttachment[]; references: string[] }) => {
      if (!selectedId) throw new Error('会话不存在')
      return retryAgentSession(selectedId, {
        message_id: input.messageId,
        content: input.content,
        model: effectiveModel ?? undefined,
        reasoning_effort: normalizeReasoningEffort(gatewayModels.find((item) => item.id === effectiveModel) ?? null, effort),
        permission_mode: permissionMode,
        attachments: input.files.length
          ? input.files.map((file) => ({ name: file.name, mime_type: file.mimeType, data_url: file.dataURL }))
          : undefined,
        references: input.references.length ? input.references : undefined,
      })
    },
    onSuccess: async ({ session_id, message_id }, variables) => {
      retryTimelineBeforeEdit.current = null
      setEditingMessage(null)
      setRunning(true)
      setReferences([])
      if (message_id) {
        setTimeline((current) => current.map((item) => item.kind === 'user' && (item.messageId === variables.messageId || item.id === variables.messageId)
          ? { ...item, id: message_id, messageId: message_id }
          : item))
      }
      await queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
      void followSession(session_id)
    },
    onError: (error) => {
      if (retryTimelineBeforeEdit.current) {
        const snapshot = retryTimelineBeforeEdit.current
        setTimeline(snapshot.timeline)
        setDraft(snapshot.draft)
        setAttachments(snapshot.attachments)
        setAttachmentError(snapshot.attachmentError)
        setReferences(snapshot.references)
        setEditingMessage(snapshot.editingMessage)
        retryTimelineBeforeEdit.current = null
      }
      setRunning(false)
      toast.error(t('agent:chat.sendFailed'), { description: error.message })
    },
  })

  const agentUnavailable = healthQuery.data?.status === 'unhealthy'
  const agentUnavailableReason = agentUnavailable
    ? healthQuery.data?.reason || t('agent:chat.unavailable')
    : undefined
  const canSubmit = Boolean((draft.trim() || attachments.length > 0) && !running && !agentUnavailable && !sendMutation.isPending && !retryMutation.isPending && !compactMutation.isPending)
  // Busy = session creation pending or a run in flight; drives the send/stop button state.
  const awaiting = sendMutation.isPending || retryMutation.isPending || running
  // Waiting indicator shows while busy before the final response, including active tool calls.
  const lastItem = timeline[timeline.length - 1]
  const runNeedsIndicator = lastItem?.kind === 'run'
    && lastItem.run.status === 'running'
    && (!lastItem.run.finalText
      || lastItem.run.steps.some((step) => step.status === 'running')
      || lastItem.run.permissions.some((request) => request.decision !== undefined))
  const waitingReply = awaiting && (!lastItem || lastItem.kind === 'user' || Boolean(runNeedsIndicator))
  const waitingOrbState = lastItem?.kind === 'run' ? orbStateForRun(lastItem.run) : 'breathing'

  // 会话进入 / 发送 / 重放时在各自动作处调用 scrollToBottom() 强制贴底；
  // 运行中的流式追加由 useStickToBottom 的 ResizeObserver 贴底跟随：
  // 用户滚离阅读时不打扰，贴底时内容增长自动滚到底

  function createNew() {
    eventAbort.current?.abort()
    skipTranscriptLoadFor.current = null
    saveLastSessionId(null)
    setActiveId(null)
    setNewSession(true)
    setTimeline([])
    setDraft('')
    setEditingMessage(null)
    setEditReplayConfirmation(null)
    retryTimelineBeforeEdit.current = null
    setAttachments([])
    setReferences([])
    setReferenceOpen(false)
    setRunning(false)
    setModel(null) // fall back to the globally last-used model from server memory
  }

  function handleSelect(sessionId: string) {
    // 点击的同一 tick 发起 transcript 预取，不等渲染后的 effect；与 effect 里的 fetchQuery 按 key 去重
    void queryClient.prefetchQuery({
      queryKey: agentTranscriptKey(sessionId),
      queryFn: () => fetchAgentTranscript(sessionId),
      staleTime: TRANSCRIPT_STALE_TIME,
    })
    saveLastSessionId(sessionId)
    eventAbort.current?.abort()
    skipTranscriptLoadFor.current = null
    scrollToBottom()
    setRunning(false)
    setEditingMessage(null)
    setEditReplayConfirmation(null)
    retryTimelineBeforeEdit.current = null
    setNewSession(false)
    setActiveId(sessionId)
    setModel(null) // fall back to the session's last-used model
  }

  function handleDelete(sessionId: string) {
    void deleteAgentSession(sessionId).catch(() => undefined)
    queryClient.removeQueries({ queryKey: agentTranscriptKey(sessionId) })
    void queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
    if (sessionId === selectedId) createNew()
  }

  /** 分叉会话：整会话（无 messageId）或到某一轮为止；成功后切换到新会话 */
  async function handleFork(sessionId: string, messageId?: string) {
    if (forking) return
    setForking(true)
    try {
      const forked = await forkAgentSession(sessionId, messageId)
      await queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
      handleSelect(forked.session_id)
      toast.success(t('agent:chat.forkCreated'), { description: t('agent:chat.forkCreatedDesc') })
    } catch (error) {
      if (error instanceof APIError && error.status === 409) {
        toast.error(t('agent:chat.forkConflict'))
      } else {
        toast.error(t('agent:chat.forkFailed'), { description: error instanceof Error ? error.message : String(error) })
      }
    } finally {
      setForking(false)
    }
  }

  function handleRevokeAccess(sessionId: string) {
    void revokeAgentAccess(sessionId).then(() => {
      toast.success(t('agent:sidebar.revoked'))
      void queryClient.invalidateQueries({ queryKey: ['agent', 'sessions'] })
    }).catch((error: unknown) => {
      toast.error(t('agent:permission.failed'), { description: error instanceof Error ? error.message : String(error) })
    })
  }

  function submit() {
    const content = draft.trim() || attachments[0]?.name || ''
    if (!canSubmit || !content) return
    // /compact 指令：不冒泡为用户消息，直接在时间线展示压缩状态
    if (content === AGENT_COMPACT_COMMAND && attachments.length === 0) {
      if (!selectedId) {
        toast.error(t('agent:chat.compactNoSession'))
        return
      }
      setDraft('')
      setTimeline((current) => reduceAgentEvent(current, { type: 'context_compacting', data: { manual: true } }))
      compactMutation.mutate(selectedId)
      return
    }
    if (editingMessage && selectedId) {
      setEditReplayConfirmation({ messageId: editingMessage.messageId, content })
      return
    }
    const files = attachments
    const messageReferences = references
    scrollToBottom()
    setTimeline((current) => appendUserMessage(current, content, undefined, Date.now(), files, messageReferences))
    setDraft('')
    setAttachments([])
    setAttachmentError(null)
    sendMutation.mutate({ content, files, references: messageReferences.map((reference) => reference.sessionId) })
  }

  function confirmEditReplay() {
    if (!editReplayConfirmation || !selectedId) return
    const { messageId, content } = editReplayConfirmation
    const original = timeline.find((item) => item.kind === 'user' && (item.messageId === messageId || item.id === messageId))
    const files = original?.kind === 'user' ? original.attachments ?? [] : attachments
    const messageReferences = references
    retryTimelineBeforeEdit.current = {
      timeline,
      draft,
      attachments,
      attachmentError,
      references,
      editingMessage,
    }
    scrollToBottom()
    setTimeline((current) => replaceFromUserMessage(current, messageId, content, files, messageReferences))
    setEditReplayConfirmation(null)
    setEditingMessage(null)
    setDraft('')
    setAttachments([])
    setAttachmentError(null)
    retryMutation.mutate({ messageId, content, files, references: messageReferences.map((reference) => reference.sessionId) })
  }

  function handleUserEdit(messageId: string, text: string) {
    if (awaiting) return
    const original = timeline.find((item) => item.kind === 'user' && (item.messageId === messageId || item.id === messageId))
    setEditingMessage({ messageId })
    setDraft(text)
    setAttachments([])
    setAttachmentError(null)
    // 编辑重发沿用原消息的引用，编辑态可在 chip 区移除
    setReferences(original?.kind === 'user' ? original.references ?? [] : [])
  }

  function handlePermissionDecision(request: AgentPermissionRequest, decision: 'allow' | 'deny' | 'allow_session') {
    // bash 审批只有命令、没有路径(resolvedPath 为空),后端也不要求该字段
    if (!selectedId) return
    const allowed = decision !== 'deny'
    const sessionScope = decision === 'allow_session'
    setTimeline((current) => decidePermission(current, request.id, allowed ? 'allowed' : 'denied', sessionScope ? { scope: 'session' } : undefined))
    // 同一个 run 在审批后继续,原来的事件流仍然开着,会推送 permission_result 与后续输出;
    // 不能再 followSession:重新订阅会从 run 开头重放,把已有的卡片与文本再追加一遍。
    void grantAgentAccess(selectedId, {
      escalation_id: request.id,
      granted: allowed,
      resolved_path: request.resolvedPath ?? '',
      ...(sessionScope ? { scope: 'session' as const } : {}),
    }).catch((error: unknown) => {
      toast.error(t('agent:permission.failed'), { description: error instanceof Error ? error.message : String(error) })
    })
  }

  function applyPermissionMode(next: PermissionMode) {
    setPermissionMode(next)
    try {
      window.localStorage.setItem(PERMISSION_MODE_KEY, next)
    } catch { /* ignore */ }
  }

  function togglePermissionMode() {
    if (permissionMode === 'ask') {
      // Full access requires an explicit confirmation.
      setConfirmFullAccess(true)
      return
    }
    applyPermissionMode('ask')
  }

  const addAttachments = async (files: FileList | File[] | null) => {
    if (!files) return
    const remaining = MAX_ATTACHMENTS - attachments.length
    const selected = Array.from(files).slice(0, Math.max(remaining, 0))
    if (selected.length === 0) {
      setAttachmentError(t('chat:chat.attachmentLimit', { count: MAX_ATTACHMENTS }))
      return
    }
    const oversized = selected.find((file) => file.size > MAX_ATTACHMENT_BYTES)
    if (oversized) {
      setAttachmentError(t('chat:chat.attachmentTooLarge', { name: oversized.name }))
      return
    }
    try {
      const next = await Promise.all(selected.map(async (file): Promise<ChatAttachment> => {
        const mimeType = attachmentMimeType(file)
        const dataURL = await fileToDataURL(file)
        return {
          id: newId(),
          name: file.name,
          mimeType,
          size: file.size,
          dataURL: normalizeAttachmentDataURL(dataURL, mimeType),
        }
      }))
      setAttachments((current) => [...current, ...next].slice(0, MAX_ATTACHMENTS))
      setAttachmentError(files.length > remaining ? t('chat:chat.attachmentLimit', { count: MAX_ATTACHMENTS }) : null)
    } catch {
      setAttachmentError(t('chat:chat.attachmentReadFailed'))
    }
  }

  const composerContent = (
    <Composer
      value={draft}
      onChange={setDraft}
      onSubmit={submit}
      onStop={() => {
        if (selectedId) {
          void stopAgentSession(selectedId)
          setRunning(false)
        }
      }}
      streaming={awaiting}
      canSubmit={canSubmit}
      sendButtonTitle={agentUnavailableReason}
      placeholder={t('agent:chat.inputPlaceholder')}
      minRows={isMobileLayout ? 2 : 3}
      onPasteFiles={(files) => {
        if (running || editingMessage) return false
        void addAttachments(files)
        return true
      }}
      controls={
        <>
          <AttachmentMenu
            kinds={['photo', 'camera', 'file']}
            onFiles={(files) => void addAttachments(files)}
            disabled={running || Boolean(editingMessage) || attachments.length >= MAX_ATTACHMENTS}
            label={t('chat:chat.attach')}
          />
          <button
            type="button"
            onClick={() => setReferenceOpen(true)}
            disabled={references.length >= MAX_SESSION_REFERENCES || Boolean(editingMessage) || running}
            className="flex h-11 items-center gap-1.5 rounded-full px-2.5 text-footnote font-medium text-label-2 transition-colors active:bg-fill-3 active:text-label disabled:cursor-not-allowed disabled:opacity-40 md:h-8 md:hover:bg-fill-3 md:hover:text-label"
            aria-label={t('agent:reference.action')}
            title={t('agent:reference.hint')}
          >
            <Link2 className="h-4 w-4" />
            <span className="hidden sm:inline">{t('agent:reference.action')}</span>
          </button>
          <button
            type="button"
            onClick={togglePermissionMode}
            className="flex h-11 items-center gap-1.5 rounded-full px-2.5 text-footnote font-medium text-label-2 transition-colors active:bg-fill-3 active:text-label md:h-8 md:hover:bg-fill-3 md:hover:text-label"
            aria-label={t('agent:composer.permissionMode')}
            title={permissionMode === 'ask' ? t('agent:composer.permissionAskHint') : t('agent:composer.permissionFullHint')}
          >
            {permissionMode === 'ask' ? <ShieldQuestion className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />}
            <span className="hidden sm:inline">{permissionMode === 'ask' ? t('agent:composer.permissionAsk') : t('agent:composer.permissionFull')}</span>
          </button>
        </>
      }
      attachments={attachments.length > 0 || attachmentError || references.length > 0 ? (
        <div className="space-y-2">
          {references.length > 0 ? (
            <div className="flex flex-wrap gap-2">
              {references.map((reference) => (
                <span
                  key={reference.sessionId}
                  className="flex items-center gap-1 rounded-full border border-separator bg-fill-3 py-0.5 pl-2.5 pr-1 text-footnote text-label-2"
                >
                  <span className="max-w-56 truncate" title={reference.sessionId}>{referenceChipLabel(reference)}</span>
                  <button
                    type="button"
                    onClick={() => setReferences((current) => current.filter((item) => item.sessionId !== reference.sessionId))}
                    className="rounded-full p-0.5 text-label-3 transition-colors hover:bg-fill-2 hover:text-label"
                    aria-label={t('agent:reference.remove')}
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
            </div>
          ) : null}
          {attachments.length > 0 ? (
            <div className="flex flex-wrap gap-2">
              {attachments.map((attachment) => (
                <ChatAttachmentItem
                  key={attachment.id}
                  attachment={attachment}
                  removeLabel={t('chat:chat.removeAttachment')}
                  onRemove={() => setAttachments((current) => current.filter((item) => item.id !== attachment.id))}
                />
              ))}
            </div>
          ) : null}
          {attachmentError ? <p className="px-1 text-footnote text-danger">{attachmentError}</p> : null}
        </div>
      ) : undefined}
      trailingControls={
        <ReasoningSettingsPicker
          models={gatewayModels}
          model={effectiveModel}
          onModelChange={(id) => {
            setModel(id)
            setEffort((current) => normalizeReasoningEffort(gatewayModels.find((item) => item.id === id) ?? null, current))
          }}
          effort={effort}
          onEffortChange={setEffort}
          disabled={gatewayModels.length === 0}
          triggerClassName="max-md:h-11"
        />
      }
    />
  )

  const readOnlyMode = healthQuery.data?.isolation === 'degraded'
  const noBashMode = healthQuery.data?.bash_enabled === false
    && (healthQuery.data.sandbox === 'failed' || healthQuery.data.sandbox === 'unavailable')
  const healthNotices = agentUnavailableReason || (!capabilityNoticeDismissed && (readOnlyMode || noBashMode)) ? (
    <div className="mb-2 space-y-2">
      {agentUnavailableReason ? (
        <div role="alert" className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs leading-5 text-red-500">
          <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span className="whitespace-pre-wrap break-words">{agentUnavailableReason}</span>
        </div>
      ) : null}
      {!capabilityNoticeDismissed && (readOnlyMode || noBashMode) ? (
        <div className="flex items-start gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-2 text-xs leading-5 text-muted-soft">
          <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1">
            {readOnlyMode ? t('agent:chat.readOnlyNotice') : null}
            {readOnlyMode && noBashMode ? ' · ' : null}
            {noBashMode ? t('agent:chat.noBashNotice') : null}
          </span>
          <button
            type="button"
            aria-label={t('agent:chat.dismissNotice')}
            title={t('agent:chat.dismissNotice')}
            className="shrink-0 text-faint hover:text-foreground"
            onClick={() => {
              dismissCapabilityNotice()
              setCapabilityNoticeDismissed(true)
            }}
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      ) : null}
    </div>
  ) : null

  const composer = (
    <LiquidGlass variant="regular" className="agent-composer rounded-[26px]">
      {composerContent}
    </LiquidGlass>
  )

  const sidebarContent = <AgentSidebar
    sessions={sessions}
    activeId={selectedId}
    onBack={() => navigate(-1)}
    onSelect={handleSelect}
    onNew={createNew}
    onDelete={handleDelete}
    onFork={(id) => void handleFork(id)}
    onRevokeAccess={handleRevokeAccess}
    onOpenSettings={() => setSettingsOpen(true)}
  />

  return (
    <div className="relative h-full min-h-0 overflow-hidden">
      {/* 自定义背景预留层：image 为空时什么都不渲染，露出全局星点背景 */}
      <AgentBackdrop image={backdropImage} />
      <div className="relative z-10 flex h-full min-h-0 overflow-hidden">
        <AgentMobileHeader
          title={selectedId ? (activeSession?.title || activeSession?.preview || t('sidebar.untitled')) : undefined}
          onOpenMenu={() => setMobileSidebarOpen(true)}
        />
        <AgentMobileDrawer
          open={mobileSidebarOpen}
          onClose={() => setMobileSidebarOpen(false)}
        >
          <AgentSidebar
            sessions={sessions}
            activeId={selectedId}
            onBack={() => {
              setMobileSidebarOpen(false)
              navigate(-1)
            }}
            onSelect={(id) => {
              setMobileSidebarOpen(false)
              handleSelect(id)
            }}
            onNew={() => {
              setMobileSidebarOpen(false)
              createNew()
            }}
            onDelete={handleDelete}
            onFork={(id) => void handleFork(id)}
            onRevokeAccess={handleRevokeAccess}
            onOpenSettings={() => {
              setMobileSidebarOpen(false)
              setSettingsOpen(true)
            }}
          />
        </AgentMobileDrawer>
        <FullAccessConfirmDialog
          open={confirmFullAccess}
          onCancel={() => setConfirmFullAccess(false)}
          onConfirm={() => {
            applyPermissionMode('full')
            setConfirmFullAccess(false)
          }}
        />
        <EditReplayConfirmDialog
          open={Boolean(editReplayConfirmation)}
          onCancel={() => setEditReplayConfirmation(null)}
          onConfirm={confirmEditReplay}
        />
        {referenceOpen ? (
          <AgentReferenceDialog
            open
            onOpenChange={(next) => { if (!next) setReferenceOpen(false) }}
            sessions={sessions}
            currentId={selectedId}
            selectedIds={references.map((reference) => reference.sessionId)}
            onSelect={(reference) => {
              setReferences((current) =>
                current.some((item) => item.sessionId === reference.sessionId)
                  ? current
                  : [...current, reference].slice(0, MAX_SESSION_REFERENCES))
              setReferenceOpen(false)
            }}
          />
        ) : null}
        {/* 侧栏与主程序同款：浮动 Liquid Glass 面板，280 宽、距窗口 16、圆角 24 */}
        <LiquidGlass
          as="aside"
          className="z-20 my-4 ml-4 hidden h-[calc(100%-2rem)] w-[280px] shrink-0 overflow-hidden rounded-shell lg:flex lg:flex-col"
        >
          {sidebarContent}
        </LiquidGlass>
        <AgentSettingsDialog
          open={settingsOpen}
          onOpenChange={setSettingsOpen}
        />

      <div className="relative flex min-w-0 flex-1 flex-col">
        {hasMessages ? (
          <>
            <div className="relative min-h-0 flex-1">
              <div ref={scrollRef} className="h-full overflow-y-auto">
                <div className="mx-auto max-w-3xl px-4 pb-6 pt-16">
                  <AgentTimeline
                    items={timeline}
                    running={running}
                    sessions={sessions}
                    onPermissionDecision={handlePermissionDecision}
                    onUserEdit={handleUserEdit}
                    onFork={selectedId ? (messageId) => void handleFork(selectedId, messageId) : undefined}
                    onOpenReference={handleSelect}
                  />
                  {waitingReply ? <WaitingIndicator state={waitingOrbState} /> : null}
                </div>
              </div>
              {showJump ? <JumpToLatestButton label={t('agent:chat.scrollToLatest')} onClick={scrollToBottom} /> : null}
            </div>
            <div className="shrink-0 px-4 pb-4">
              <div className="mx-auto max-w-3xl">
                {healthNotices}
                {composer}
                <p className="mt-2 text-center text-xs text-faint">{t('agent:chat.hint')}</p>
              </div>
            </div>
          </>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center px-4">
            <div className="w-full max-w-2xl">
              <div className="mb-6 flex h-20 items-center justify-center text-center">
                <h2 className="text-title-1 text-label">{t('agent:chat.emptyTitle')}</h2>
              </div>
              {healthNotices}
              {composer}
              <p className="mt-3 text-center text-xs text-faint">{t('agent:chat.hint')}</p>
            </div>
          </div>
        )}
      </div>
      </div>
    </div>
  )
}

function AgentMobileHeader({ title, onOpenMenu }: { title?: string; onOpenMenu: () => void }) {
  const { t } = useTranslation('agent')
  // 有会话标题才是一进入会话的顶栏，给它半透明玻璃底；首页展示 logo 时不加背景
  const hasTitle = Boolean(title)

  return (
    <header
      className={cn(
        'mobile-safe-top pointer-events-none absolute inset-x-0 top-0 z-30 flex items-center gap-2 px-4 pb-2 lg:hidden',
        hasTitle && 'border-b border-[hsl(var(--glass-divider))] bg-black/45 backdrop-blur-md',
      )}
      style={{ '--mobile-safe-top-extra': '0.75rem' } as React.CSSProperties}
    >
      <div className="pointer-events-auto flex items-center gap-2">
        <Button
          variant="ghost"
          size="icon"
          className="-ml-2 rounded-full"
          aria-label={t('sidebar.menu')}
          onClick={onOpenMenu}
        >
          <Menu className="size-5" />
        </Button>
        {title ? (
          <p className="max-w-[min(70vw,20rem)] truncate text-title-3 text-label">{title}</p>
        ) : (
          <Link to="/dashboard" aria-label={t('header.back')} className="grid size-8 shrink-0 place-items-center">
            <AppLogo className="size-full !bg-transparent" decorative />
          </Link>
        )}
      </div>
    </header>
  )
}

function AgentMobileDrawer({
  open,
  onClose,
  children,
}: {
  open: boolean
  onClose: () => void
  children: React.ReactNode
}) {
  const { t } = useTranslation('agent')

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [onClose, open])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 lg:hidden">
      <button
        type="button"
        aria-label={t('settings.close')}
        className="absolute inset-0 bg-black/45"
        onClick={onClose}
      />
      <aside
        className="lg-thick absolute inset-y-0 left-0 w-[min(84vw,320px)] overflow-hidden rounded-r-[24px] pb-[env(safe-area-inset-bottom,0px)] pt-[calc(env(safe-area-inset-top,0px)+0.75rem)] shadow-[18px_0_54px_rgb(0_0_0/0.28)] animate-in slide-in-from-left-full duration-300 ease-out"
        aria-label={t('sidebar.recent')}
      >
        {children}
      </aside>
    </div>
  )
}
