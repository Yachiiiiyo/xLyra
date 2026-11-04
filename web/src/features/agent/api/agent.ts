import { apiFetch, apiFetchBlob, apiFetchResponse, apiURL, APIError } from '@/lib/http'
import { listAllSiteModels, listCanonicalModels, listSitesLite, type CanonicalModelItem, type Site, type SiteModel } from '@/features/sites/api/sites'

export type AgentSession = {
  session_id: string
  title?: string
  preview?: string
  updated_at?: string | number
  running?: boolean
  /** 已被「会话内允许」的工具名,空数组表示没有 */
  session_allow?: string[]
}

export type AgentHealth = {
  status?: 'healthy' | 'unhealthy' | 'missing' | string
  reason?: string
  isolation?: 'on' | 'degraded' | 'off' | 'failed'
  isolation_reason?: string
  write_tools_enabled?: boolean
  sandbox?: 'on' | 'off' | 'unavailable' | 'failed'
  sandbox_reason?: string
  bash_enabled?: boolean
  runner?: string
  agent?: string
}

export type AgentImageModelSelection = {
  model: string
  /** 该模型选中的站点;空数组 = 不限站点(含该模型的所有站点) */
  site_ids: string[]
}

export type AgentRuntimeSettings = {
  allowed_site_ids: string[]
  allowed_site_model_ids: string[]
  site_policy: 'allow_all' | 'allow_list'
  model_policy: 'allow_all' | 'allow_list'
  /** 生图模型,按模型名去重并排序;每个模型各自选定站点,site_ids 为空表示不限站点 */
  image_models: AgentImageModelSelection[]
  /** 后端过渡期可能仍返回的旧单值字段,仅用于兼容读取 */
  image_model?: { site_id: string; model: string } | null
  /** 后端仍返回该字段；前端外观配置已下线，仅保留契约，不再消费 */
  appearance?: AgentAppearanceSettings
}

export type AgentAppearanceSettings = {
  background_image: string
  custom_background_images: string[]
  side_transparency: number
  side_brightness: number
  side_thickness: number
  backdrop_blur: number
  backdrop_dim: number
}

export type AgentAvailableSite = {
  site_id: string
  site_name: string
  site_type: string
  enabled: boolean
  models: Array<Pick<SiteModel, 'id' | 'upstream_model_name' | 'display_name' | 'canonical_model_id'> & { model_key?: string; category?: string; reasoning_effort?: unknown }>
}

// Health keeps its JSON body on 503; only 404 means the built-in agent is absent.
export async function fetchAgentHealth(): Promise<AgentHealth | null> {
  try {
    const response = await fetch(apiURL('/api/v1/agent/health'), { credentials: 'include' })
    if (response.status === 404) return { status: 'missing' }
    const payload: unknown = await response.json().catch(() => null)
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
      return { status: response.ok ? 'healthy' : 'unhealthy' }
    }
    return payload as AgentHealth
  } catch {
    return null
  }
}

export async function fetchAgentRuntimeSettings() {
  const response = await apiFetch<{ data?: AgentRuntimeSettings }>('/api/v1/agent/settings')
  return {
    allowed_site_ids: response.data?.allowed_site_ids ?? [],
    allowed_site_model_ids: response.data?.allowed_site_model_ids ?? [],
    site_policy: response.data?.site_policy ?? 'allow_all',
    model_policy: response.data?.model_policy ?? 'allow_all',
    image_models: normalizeImageModels(response.data?.image_models, response.data?.image_model),
    appearance: response.data?.appearance,
  }
}

// The settings endpoint is a partial update: absent fields stay unchanged.
// 兼容旧格式:元素是字符串(模型 key,不限站点),或后端过渡期返回的单个 image_model。
function normalizeImageModels(models: unknown, legacy: unknown): AgentImageModelSelection[] {
  const list: unknown[] = Array.isArray(models)
    ? models
    : legacy && typeof legacy === 'object' ? [(legacy as { model?: unknown }).model] : []
  return dedupeImageModels(list.flatMap((item): AgentImageModelSelection[] => {
    if (typeof item === 'string') return [{ model: item, site_ids: [] }]
    if (item && typeof item === 'object' && typeof (item as { model?: unknown }).model === 'string') {
      const sites = (item as { site_ids?: unknown }).site_ids
      return [{
        model: (item as { model: string }).model,
        site_ids: Array.isArray(sites) ? sites.filter((id): id is string => typeof id === 'string') : [],
      }]
    }
    return []
  }))
}

/** 按模型名去重保序:同名合并站点(任一为「不限站点」则整体不限),站点去重保序。 */
export function dedupeImageModels(models: AgentImageModelSelection[]): AgentImageModelSelection[] {
  const merged = new Map<string, AgentImageModelSelection>()
  for (const item of models) {
    const model = item.model.trim()
    if (!model) continue
    const sites = [...new Set(item.site_ids.map((id) => id.trim()).filter(Boolean))]
    const existing = merged.get(model)
    if (!existing) {
      merged.set(model, { model, site_ids: sites })
    } else if (existing.site_ids.length === 0 || sites.length === 0) {
      existing.site_ids = []
    } else {
      existing.site_ids = [...new Set([...existing.site_ids, ...sites])]
    }
  }
  return [...merged.values()]
}

// 只改生图模型列表,不动站点/模型白名单(部分更新);空数组表示清除。
export async function updateAgentImageModels(imageModels: AgentImageModelSelection[]) {
  const response = await apiFetch<{ data?: AgentRuntimeSettings }>('/api/v1/agent/settings', {
    method: 'PUT',
    body: { image_models: dedupeImageModels(imageModels) },
  })
  return response.data
}

export async function updateAgentScopeSettings(settings: { site_policy: 'allow_all' | 'allow_list'; model_policy: 'allow_all' | 'allow_list'; allowed_site_ids: string[]; allowed_site_model_ids: string[]; image_models?: AgentImageModelSelection[] }) {
  const response = await apiFetch<{ data?: AgentRuntimeSettings }>('/api/v1/agent/settings', {
    method: 'PUT',
    body: settings,
  })
  return response.data
}

export async function fetchAgentAvailableModels(): Promise<AgentAvailableSite[]> {
  const [sitesResponse, canonicalResponse, modelsBySite] = await Promise.all([listSitesLite(), listCanonicalModels(), listAllSiteModels()])
  return buildAgentAvailableModels(sitesResponse.items ?? [], canonicalResponse.items ?? [], modelsBySite)
}

export function buildAgentAvailableModels(sites: Site[], canonicalModels: CanonicalModelItem[], modelsBySite: Record<string, SiteModel[]>): AgentAvailableSite[] {
  const canonicalById = new Map(canonicalModels.map((model) => [model.id, model]))
  const rows = sites.map((site) => ({
    site_id: site.id,
    site_name: site.name,
    site_type: site.site_type,
    enabled: site.enabled,
    models: site.enabled
      ? (modelsBySite[site.id] ?? []).filter((model) => model.enabled !== false && model.status === 'active').map(({ id, upstream_model_name, display_name, canonical_model_id }) => {
          const canonical = canonical_model_id ? canonicalById.get(canonical_model_id) : undefined
          return { id, upstream_model_name, display_name, canonical_model_id, model_key: canonical?.model_key, category: canonical?.category, reasoning_effort: canonical?.reasoning_effort }
        })
      : [],
  }))
  return rows.filter((site) => !site.enabled || site.models.length > 0)
}

// ---- Capabilities: Skills / AGENTS.md ----

export type AgentSkill = {
  name: string
  description: string
  scope?: 'project' | 'managed' | 'user' | 'extra'
  enabled: boolean
  path?: string
  license?: string
  ownership?: 'agent' | 'external'
  editable?: boolean
  deletable?: boolean
}

export type AgentSkillsInfo = {
  /** Global toggle (agent enable_skills). */
  enabled: boolean
  skills: AgentSkill[]
  warnings?: string[]
}

// Older agents may lack this endpoint (404) or be unreachable; null degrades the UI.
export async function listAgentSkills(): Promise<AgentSkillsInfo | null> {
  try {
    const response = await apiFetch<{ data?: AgentSkillsInfo }>('/api/v1/agent/skills')
    return response.data ?? { enabled: true, skills: [] }
  } catch {
    return null
  }
}

/** Reads a workspace file; null when the file does not exist. */
export async function fetchWorkspaceFile(path: string): Promise<string | null> {
  try {
    const response = await apiFetch<{ data?: { content?: string } }>(`/api/v1/agent/workspace/file?path=${encodeURIComponent(path)}`)
    return response.data?.content ?? ''
  } catch (error) {
    if (error instanceof APIError && error.status === 404) return null
    throw error
  }
}

export async function putWorkspaceFile(path: string, content: string) {
  return apiFetch<{ success?: boolean }>('/api/v1/agent/workspace/file', { method: 'PUT', body: { path, content } })
}

export async function deleteWorkspaceFile(path: string) {
  return apiFetch<{ success?: boolean }>(`/api/v1/agent/workspace/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' })
}

export type AgentSkillDetail = AgentSkill & {
  metadata?: Record<string, string>
  /** Full SKILL.md including frontmatter. */
  content: string
  contentTruncated?: boolean
  /** Resource file paths relative to the skill root. */
  resources: string[]
}

export async function updateAgentSkill(name: string, content: string) {
  const response = await apiFetch<{ data?: { name: string; changed: boolean } }>(`/api/v1/agent/skills/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: { content },
  })
  return response.data
}

export async function deleteAgentSkill(name: string) {
  const response = await apiFetch<{ data?: { name: string; deleted: boolean } }>(`/api/v1/agent/skills/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    body: {},
  })
  return response.data
}

// Older agents may lack this endpoint; returns null.
export async function fetchAgentSkillDetail(name: string): Promise<AgentSkillDetail | null> {
  try {
    const response = await apiFetch<{ data?: AgentSkillDetail }>(`/api/v1/agent/skills/${encodeURIComponent(name)}`)
    return response.data ?? null
  } catch {
    return null
  }
}

/** Reads a single resource file inside a skill directory (doc/script preview). */
export async function fetchAgentSkillFile(name: string, path: string): Promise<{ content: string; truncated?: boolean } | null> {
  try {
    const response = await apiFetch<{ data?: { content: string; truncated?: boolean } }>(
      `/api/v1/agent/skills/${encodeURIComponent(name)}/file?path=${encodeURIComponent(path)}`,
    )
    return response.data ?? null
  } catch {
    return null
  }
}

export type AgentCapabilitiesConfig = {
  enable_agents_md?: boolean
  enable_skills?: boolean
  disabled_skills?: string[]
  /** 自动压缩水位线 0~1：上下文占用达到窗口的该比例时触发压缩（默认 0.9） */
  compact_trigger_ratio?: number
  /** 全局上下文窗口回退（token）：模型未在端点 models 声明时使用；再缺省按 200k 兜底 */
  context_window?: number
}

export type AgentEndpointConfig = {
  name: string
  models?: Record<string, { context_window?: number }>
} & Record<string, unknown>

type AgentConfigEnvelope = {
  endpoints?: AgentEndpointConfig[]
  agent?: AgentCapabilitiesConfig & Record<string, unknown>
  /** server 段必须原样回带：agent /config 是整体保存，漏掉会丢 server 配置 */
  server?: Record<string, unknown>
}

export async function fetchAgentCapabilities(): Promise<AgentCapabilitiesConfig | null> {
  try {
    const response = await apiFetch<{ data?: AgentConfigEnvelope }>('/api/v1/agent/config')
    return response.data?.agent ?? {}
  } catch {
    return null
  }
}

export async function fetchAgentConfigEnvelope(): Promise<AgentConfigEnvelope | null> {
  try {
    const response = await apiFetch<{ data?: AgentConfigEnvelope }>('/api/v1/agent/config')
    return response.data ?? null
  } catch {
    return null
  }
}

// agent /config saves as a whole: read the full config, merge the agent section, write back (masked credentials are preserved server-side).
export async function updateAgentCapabilities(patch: AgentCapabilitiesConfig) {
  const current = await apiFetch<{ data?: AgentConfigEnvelope }>('/api/v1/agent/config')
  const data = current.data ?? {}
  const agent = { ...(data.agent ?? {}), ...patch }
  await apiFetch('/api/v1/agent/config', { method: 'PUT', body: { endpoints: data.endpoints ?? [], agent, ...(data.server ? { server: data.server } : {}) } })
}

/** 上下文设置：压缩水位线 + 全局上下文窗口（undefined 表示删除回退，恢复默认 200k 兜底） */
export async function updateAgentContextSettings(settings: {
  compactTriggerRatio: number
  contextWindow?: number
}) {
  const current = await apiFetch<{ data?: AgentConfigEnvelope }>('/api/v1/agent/config')
  const data = current.data ?? {}
  const agent: AgentCapabilitiesConfig & Record<string, unknown> = { ...(data.agent ?? {}), compact_trigger_ratio: settings.compactTriggerRatio }
  if (settings.contextWindow !== undefined && Number.isFinite(settings.contextWindow) && settings.contextWindow > 0) {
    agent.context_window = Math.floor(settings.contextWindow)
  } else {
    delete agent.context_window
  }
  await apiFetch('/api/v1/agent/config', { method: 'PUT', body: { endpoints: data.endpoints ?? [], agent, ...(data.server ? { server: data.server } : {}) } })
}

export async function listAgentSessions() {
  const response = await apiFetch<{ data?: AgentSession[] }>('/api/v1/agent/sessions')
  return response.data ?? []
}

export type AgentAttachment = {
  name: string
  mime_type: string
  data_url?: string
}

export type AgentModelMemory = {
  default_model: string
  sessions: Record<string, string>
}

export async function fetchAgentModelMemory(): Promise<AgentModelMemory> {
  const response = await apiFetch<{ data?: AgentModelMemory }>('/api/v1/agent/model-memory')
  return response.data ?? { default_model: '', sessions: {} }
}

export async function createAgentSession(input: { content: string; model?: string; session_id?: string; reasoning_effort?: string; permission_mode?: 'ask' | 'full'; attachments?: AgentAttachment[]; references?: string[] }) {
  const response = await apiFetch<{ data: { session_id: string; message_id?: string; run_id: string } }>('/api/v1/agent/sessions', {
    method: 'POST',
    body: input,
  })
  return response.data
}

export async function retryAgentSession(sessionId: string, input: { message_id: string; content?: string; model?: string; reasoning_effort?: string; permission_mode?: 'ask' | 'full'; attachments?: AgentAttachment[]; references?: string[] }) {
  const response = await apiFetch<{ data: { session_id: string; message_id?: string; run_id: string } }>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/retry`, {
    method: 'POST',
    body: input,
  })
  return response.data
}

export async function fetchSessionFile(sessionId: string, path: string): Promise<{ kind: 'text'; text: string } | { kind: 'image'; blob: Blob }> {
  const { blob } = await apiFetchBlob(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(path)}`)
  if (blob.type.startsWith('image/')) return { kind: 'image', blob }
  return { kind: 'text', text: await blob.text() }
}

export function sessionFileErrorKind(error: unknown): 'missing' | 'tooLarge' | 'unsupported' | 'failed' {
  if (!(error instanceof APIError)) return 'failed'
  if (error.status === 404) return 'missing'
  if (error.status === 413) return 'tooLarge'
  if (error.status === 415) return 'unsupported'
  return 'failed'
}

export async function downloadSessionFile(sessionId: string, path: string) {
  const { blob } = await apiFetchBlob(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(path)}&download=1`)
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = path.slice(path.lastIndexOf('/') + 1)
  anchor.click()
  URL.revokeObjectURL(url)
}

export async function fetchSessionDiff(sessionId: string, turnId: string, path: string): Promise<string> {
  const { blob } = await apiFetchBlob(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/changes/${encodeURIComponent(turnId)}/diff?path=${encodeURIComponent(path)}`)
  return blob.text()
}

export function sessionDiffErrorKind(error: unknown): 'gone' | 'missing' | 'failed' {
  if (!(error instanceof APIError)) return 'failed'
  if (error.status === 410) return 'gone'
  if (error.status === 404) return 'missing'
  return 'failed'
}

export type AgentArtifactFile = {
  path: string
  size: number
  mtime: string
  kind: 'text' | 'image' | 'binary'
  agent_generated: boolean
}

export type AgentArtifacts = { files: AgentArtifactFile[]; truncated: boolean }

/** 会话工作区文件清单;代理引擎不提供该接口(404/405)时返回 null */
export async function fetchSessionArtifacts(sessionId: string): Promise<AgentArtifacts | null> {
  try {
    const response = await apiFetch<{ data?: Partial<AgentArtifacts> }>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/artifacts`)
    return { files: response.data?.files ?? [], truncated: response.data?.truncated === true }
  } catch (error) {
    if (error instanceof APIError && (error.status === 404 || error.status === 405)) return null
    throw error
  }
}

export async function downloadSessionArtifactsZip(sessionId: string) {
  const { blob } = await apiFetchBlob(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/artifacts.zip`)
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `workspace-${sessionId.slice(0, 8)}.zip`
  anchor.click()
  URL.revokeObjectURL(url)
}

export async function fetchAgentTranscript(sessionId: string) {
  const response = await apiFetch<{ data?: { entries?: AgentTranscriptEntry[] } }>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/transcript`)
  return response.data?.entries ?? []
}

export type AgentGeneratedImage = {
  path: string
  mime: string
  bytes: number
  model?: string
  site?: string
  alt?: string
}

export type AgentRunChangeFile = {
  path: string
  kind: 'added' | 'modified' | 'deleted'
  size: number
  additions: number
  deletions: number
  binary?: boolean
  truncated?: boolean
  has_diff: boolean
}

export type AgentTranscriptReference = {
  session_id?: string
  title?: string
}

export type AgentTranscriptEntry = {
  type?: string
  message_id?: string
  /** 用户消息条目引用的其他会话 */
  references?: AgentTranscriptReference[]
  /** Early flat-format fields (compat). */
  role?: string
  content?: string
  payload?: unknown
  timestamp?: string
  /** Transcript envelope when type === 'message'; type === 'run_error' 时为错误文案。 */
  message?: {
    role?: 'system' | 'user' | 'assistant' | 'tool'
    content?: string
    attachments?: Array<{ name: string; mime_type: string; data_url?: string }>
    /** 用户消息引用的其他会话(后端放在 message 里,与 attachments 同级) */
    references?: AgentTranscriptReference[]
    tool_calls?: Array<{ id: string; name: string; raw_arguments: string }>
    tool_call_id?: string
    name?: string
    is_error?: boolean
    images?: AgentGeneratedImage[]
    details_patch?: string
    thinking?: string
  } | string  /** type === 'compaction' */
  summary?: string
  tokens_before?: number
  tokens_after?: number
  /** type === 'escalation' */
  escalation_id?: string
  tool_name?: string
  requested_path?: string
  resolved_path?: string
  requested_command?: string[]
  note?: string
  granted?: boolean | null
  /** type === 'escalation'：'session' 表示本会话内不再询问 */
  scope?: 'once' | 'session'
  /** type === 'escalation'：会话内允许自动放行 */
  auto?: boolean
  /** type === 'run_changes'：本轮(一条用户消息)的工作区变更,由后端清单差得出 */
  turn_id?: string
  run_ids?: string[]
  incomplete?: boolean
  total?: number
  files?: AgentRunChangeFile[]
}

export type AgentForkedSession = {
  session_id: string
  title?: string
  workspace_copied?: boolean
}

/** 分叉会话：message_id 为空表示整段会话，否则复制到该用户消息所在一轮结束为止 */
export async function forkAgentSession(sessionId: string, messageId?: string) {
  const response = await apiFetch<{ data: AgentForkedSession }>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/fork`, {
    method: 'POST',
    body: messageId ? { message_id: messageId } : {},
  })
  return response.data
}

export async function stopAgentSession(sessionId: string) {
  return apiFetch<Record<string, unknown>>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/stop`, { method: 'POST' })
}

/** 手动压缩指令：会话输入框发送的原文，前端识别后走压缩接口而不是普通消息 */
export const AGENT_COMPACT_COMMAND = '/compact'

/** 手动压缩：经 /sessions 命令路径，同步返回摘要与 token 变化 */
export async function compactAgentSession(sessionId: string, model?: string) {
  const response = await apiFetch<{ data?: { summary?: string; tokens_before?: number; tokens_after?: number; compaction_id?: string } }>('/api/v1/agent/sessions', {
    method: 'POST',
    body: { content: AGENT_COMPACT_COMMAND, session_id: sessionId, ...(model ? { model } : {}) },
  })
  return response.data ?? {}
}

export async function deleteAgentSession(sessionId: string) {
  return apiFetch<Record<string, unknown>>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}`, { method: 'DELETE' })
}

export async function grantAgentAccess(sessionId: string, input: { escalation_id: string; granted: boolean; resolved_path: string; scope?: 'once' | 'session' }) {
  return apiFetch<Record<string, unknown>>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/grant-access`, {
    method: 'POST',
    body: input,
  })
}

/** 撤销会话内允许：tool 为空表示清空全部 */
export async function revokeAgentAccess(sessionId: string, tool?: string) {
  const response = await apiFetch<{ success?: boolean; session_allow?: string[] }>(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/revoke-access`, {
    method: 'POST',
    body: { tool: tool ?? '' },
  })
  return response.session_allow ?? []
}

export async function followAgentEvents(sessionId: string, signal: AbortSignal, onEvent: (event: { type: string; data: unknown }) => void) {
  const response = await apiFetchResponse(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/events`, { signal })
  if (!response.body) throw new APIError('Agent event stream is unavailable', response.status)
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  const flush = (raw: string) => {
    let type = 'message'
    const data: string[] = []
    for (const line of raw.split(/\r\n|\r|\n/)) {
      if (line.startsWith('event:')) type = line.slice(6).trim()
      if (line.startsWith('data:')) data.push(line.slice(5).trim())
    }
    if (!data.length) return
    let parsed: unknown = data.join('\n')
    try { parsed = JSON.parse(data.join('\n')) as unknown } catch { parsed = data.join('\n') }
    if (type === 'rollout' && parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      const envelope = parsed as { type?: unknown; payload?: unknown }
      if (typeof envelope.type === 'string') {
        onEvent({ type: envelope.type, data: envelope.payload })
        return
      }
    }
    onEvent({ type, data: parsed })
  }
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    let boundary = /\r\n\r\n|\n\n|\r\r/.exec(buffer)
    while (boundary) {
      flush(buffer.slice(0, boundary.index))
      buffer = buffer.slice(boundary.index + boundary[0].length)
      boundary = /\r\n\r\n|\n\n|\r\r/.exec(buffer)
    }
  }
  buffer += decoder.decode()
  if (buffer.trim()) flush(buffer)
}
