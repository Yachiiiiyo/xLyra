import { apiFetch } from '@/lib/http'

export type JSPluginKind =
  | 'quota_probe'
  | 'protocol'
  | 'model_list'
  | 'credential_check'
  | 'site_detect'
  | 'error_classifier'
  | 'model_metadata'
  | 'pricing_parse'
  | 'automation'
  | 'site_provider'
  | string

/** site: bind to sites; endpoint: gets a downstream path; global: applies everywhere; site_type: declares a site type; gateway_pipeline: binds to API keys on the gateway request/response pipeline. */
export type JSPluginScope = 'site' | 'endpoint' | 'global' | 'subject' | 'site_type' | 'gateway_pipeline'

export type JSPluginMetrics24h = {
  calls: number
  errors: number
  error_rate: number
  window_ends?: string
}

export type JSPluginListItem = {
  id: string
  source: string
  enabled_version: string
  version_count: number
  name?: string
  icon_url?: string
  kind?: JSPluginKind
  /** Where the kind takes effect; the server decides, so new kinds need no UI change. */
  scope?: JSPluginScope
  /** Hooks the selected version exports. A kindless plugin is described by these. */
  exports?: string[]
  metrics_24h?: JSPluginMetrics24h
  update_available?: boolean
  update_version?: string
  update_policy?: 'off' | 'notify' | 'auto'
  paused_reason?: string
  auto_block_reason?: string
  auto_block_detail?: string
  signer_revoked?: boolean
  auto_rolled_back?: boolean
  revocation_level?: string
  revocation_reason?: string
  deprecated?: boolean
  owned?: boolean
  market_ahead?: boolean
  market_version?: string
  local_version?: string
  trust?: JSPluginTrust
  submission?: {
    id: string
    plugin_id?: string
    version?: string
    status: string
    check_summary?: string
    review_note?: string
    submitted_at?: string
  }
}

export type JSPluginTrust = 'trusted' | 'untrusted_signer' | 'unsigned' | 'marketplace'

export type JSPluginVersion = {
  plugin_id: string
  version: string
  status: string
  package_sha256?: string
  signed?: boolean
  signer?: string
  trust?: JSPluginTrust
  origin?: 'manual' | 'market'
  revocation?: { revoked: boolean; level?: string; reason?: string }
  signer_revoked?: boolean
  signer_revoked_reason?: string
  auto_rolled_back?: boolean
  manifest?: Record<string, unknown>
  /** Automation actions an admin allowed this version to return. */
  granted_permissions?: string[]
  selftest?: { ok?: boolean; error?: string }
  created_at?: string
}

export type JSPluginTrustedKey = {
  id: string
  name: string
  public_key: string
  fingerprint: string
  created_at?: string
}

export type JSPluginDetail = {
  id: string
  versions: JSPluginVersion[]
}

export type JSPluginTryResult = {
  plugin_id: string
  version: string
  ok: boolean
  /** Microseconds; a fixture run is usually well under a millisecond. */
  duration_us?: number
  error?: string
}

export const JS_PLUGIN_MAX_PACKAGE_BYTES = 2 << 20

export const jsPluginQueryKeys = {
  all: ['settings', 'js-plugins'] as const,
  uploaded: () => [...jsPluginQueryKeys.all, 'uploaded'] as const,
  detail: (id: string) => [...jsPluginQueryKeys.all, 'detail', id] as const,
  settings: (id: string) => [...jsPluginQueryKeys.detail(id), 'settings'] as const,
  schedule: (id: string) => [...jsPluginQueryKeys.detail(id), 'schedule'] as const,
  trustedKeys: () => [...jsPluginQueryKeys.all, 'trusted-keys'] as const,
}

export async function listUploadedJSPlugins(signal?: AbortSignal) {
  const result = await apiFetch<{ items: JSPluginListItem[] }>('/api/v1/js-plugins', { signal })
  return result.items ?? []
}

export async function getJSPlugin(id: string, signal?: AbortSignal) {
  return apiFetch<JSPluginDetail>(`/api/v1/js-plugins/${encodeURIComponent(id)}`, { signal })
}

export async function uploadJSPlugin(file: File) {
  return apiFetch<JSPluginVersion>('/api/v1/js-plugins', {
    method: 'POST',
    body: file,
    headers: {
      'Content-Type': 'application/octet-stream',
    },
  })
}

export async function listJSPluginTrustedKeys(signal?: AbortSignal) {
  const result = await apiFetch<{ items: JSPluginTrustedKey[] }>('/api/v1/js-plugin-trusted-keys', { signal })
  return result.items ?? []
}

export async function createJSPluginTrustedKey(body: { name: string; public_key: string }) {
  return apiFetch<JSPluginTrustedKey>('/api/v1/js-plugin-trusted-keys', { method: 'POST', body })
}

export async function deleteJSPluginTrustedKey(id: string) {
  return apiFetch<{ ok: boolean }>(`/api/v1/js-plugin-trusted-keys/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

export function enableJSPluginRequestBody(options?: {
  confirm_untrusted?: boolean
  grant_permissions?: string[]
  confirm_hosts?: string[]
}) {
  const body: {
    confirm_untrusted?: boolean
    grant_permissions?: string[]
    confirm_hosts?: string[]
  } = {}
  if (options?.confirm_untrusted) body.confirm_untrusted = true
  if (options?.grant_permissions && options.grant_permissions.length > 0) {
    body.grant_permissions = options.grant_permissions
  }
  if (Array.isArray(options?.confirm_hosts)) body.confirm_hosts = options.confirm_hosts
  return body
}

export async function enableJSPluginVersion(
  pluginId: string,
  version: string,
  options?: { confirm_untrusted?: boolean; grant_permissions?: string[]; confirm_hosts?: string[] },
) {
  return apiFetch<{ ok: boolean; generation?: number }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/versions/${encodeURIComponent(version)}/enable`,
    { method: 'POST', body: enableJSPluginRequestBody(options) },
  )
}

export async function updateJSPluginSubscription(pluginId: string, updatePolicy: 'off' | 'notify' | 'auto') {
  return apiFetch<{ plugin_id: string; update_policy: 'off' | 'notify' | 'auto'; paused_reason: string }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/subscription`,
    { method: 'PUT', body: { update_policy: updatePolicy } },
  )
}

export async function disableJSPlugin(pluginId: string) {
  return apiFetch<{ ok: boolean; generation?: number }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/disable`,
    { method: 'POST' },
  )
}

/** Uninstalls every version of a plugin that is not enabled. */
export async function deleteJSPlugin(pluginId: string) {
  return apiFetch<{ ok: boolean }>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}`, { method: 'DELETE' })
}

export async function deleteJSPluginVersion(pluginId: string, version: string) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/versions/${encodeURIComponent(version)}`,
    { method: 'DELETE' },
  )
}

export async function tryJSPlugin(pluginId: string, version?: string) {
  return apiFetch<JSPluginTryResult>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}/try`, {
    method: 'POST',
    body: version ? { version } : {},
  })
}

export async function bindJSPluginProtocolSlug(pluginId: string, version: string, slug: string) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/versions/${encodeURIComponent(version)}/bind-protocol`,
    { method: 'POST', body: { slug } },
  )
}

export type JSPluginPricingPreview = {
  groups: unknown[] | null
  items: unknown[] | null
}

export async function bindJSPluginSite(
  pluginId: string,
  version: string,
  siteId: string,
  options?: { confirmPricingReviewed?: boolean },
) {
  return apiFetch<{ ok: boolean; kind: string }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/versions/${encodeURIComponent(version)}/bind-site`,
    {
      method: 'POST',
      body: { site_id: siteId, confirm_pricing_reviewed: options?.confirmPricingReviewed ?? false },
    },
  )
}

export async function unbindJSPluginSite(pluginId: string, siteId: string) {
  return apiFetch<{ ok: boolean }>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}/unbind-site`, {
    method: 'POST',
    body: { site_id: siteId },
  })
}

export type JSPluginSettingSpec = {
  key: string
  type: 'string' | 'number' | 'boolean' | string
  default: unknown
  pattern?: string
  min?: number
  max?: number
  options?: string[]
  label?: LocalizedText
  description?: LocalizedText
}

export type JSPluginSettings = {
  schema: JSPluginSettingSpec[]
  values: Record<string, unknown>
  resolved: Record<string, unknown>
}

export async function getJSPluginSettings(id: string, signal?: AbortSignal) {
  return apiFetch<JSPluginSettings>(`/api/v1/js-plugins/${encodeURIComponent(id)}/settings`, { signal })
}

export async function updateJSPluginSettings(id: string, values: Record<string, unknown>) {
  return apiFetch<{ ok: boolean; resolved: Record<string, unknown> }>(
    `/api/v1/js-plugins/${encodeURIComponent(id)}/settings`,
    { method: 'PUT', body: { values } },
  )
}

export async function updateJSPluginSiteSettings(id: string, siteId: string, values: Record<string, unknown>) {
  return apiFetch<{ values: Record<string, unknown> }>(
    `/api/v1/js-plugins/${encodeURIComponent(id)}/sites/${encodeURIComponent(siteId)}/settings`,
    { method: 'PUT', body: { values } },
  )
}

export type JSPluginScheduleView = {
  supported: boolean
  enabled: boolean
  every_minutes?: number
  label?: LocalizedText
  next_tick_at?: string
}

export function pluginDeclaresSchedule(versions: JSPluginVersion[] | undefined) {
  for (const row of versions ?? []) {
    const manifest = row.manifest
    if (!manifest || typeof manifest !== 'object') continue
    const exportsList = Array.isArray(manifest.exports) ? manifest.exports : []
    if (exportsList.includes('onSchedule')) return true
    const perms = Array.isArray(manifest.permissions) ? manifest.permissions : []
    if (perms.includes('schedule')) return true
    if (manifest.schedule && typeof manifest.schedule === 'object') return true
  }
  return false
}

export async function getJSPluginSchedule(id: string, signal?: AbortSignal) {
  return apiFetch<JSPluginScheduleView>(`/api/v1/js-plugins/${encodeURIComponent(id)}/schedule`, { signal })
}

export async function updateJSPluginSchedule(id: string, enabled: boolean) {
  return apiFetch<JSPluginScheduleView>(`/api/v1/js-plugins/${encodeURIComponent(id)}/schedule`, {
    method: 'PUT',
    body: { enabled },
  })
}

export async function previewJSPluginPricing(pluginId: string, siteId: string) {
  return apiFetch<JSPluginPricingPreview>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}/preview-pricing`, {
    method: 'POST',
    body: { site_id: siteId },
  })
}

// ---- automation plugins ----

/** Copy a plugin supplies for the form: one string for every language, or one per language. */
export type LocalizedText = string | Record<string, string>

export type JSPluginAutomationInputKind = 'oauth_connection' | 'api_key' | 'site' | 'string' | 'url' | 'number' | 'integer' | 'boolean'

/** One thing an automation plugin asks the admin for: a built-in object to pick, or a parameter. */
export type JSPluginAutomationInput = {
  name: string
  type: JSPluginAutomationInputKind
  title?: LocalizedText
  description?: LocalizedText
  placeholder?: LocalizedText
  emptyText?: LocalizedText
  /** How the form draws the input; see docs/plugins/automation.md. Left out, it follows from the type. */
  control?: 'select' | 'multi_select' | 'radio' | 'input' | 'password' | 'textarea' | 'checkbox' | 'switch'
  /** Defaults to true for objects and false for parameters. */
  required?: boolean
  // objects
  multiple?: boolean
  eventSubject?: boolean
  providers?: string[]
  requires?: string
  // parameters
  default?: string | number | boolean
  enum?: Array<string | number>
  minimum?: number
  maximum?: number
}

/** The automation block of a plugin manifest. */
export type JSPluginAutomationManifest = {
  subscribes: string[]
  permissions: string[]
  /** Present when the plugin subscribes to schedule.tick. */
  schedule?: { everyMinutes?: number }
  form?: { title?: LocalizedText; description?: LocalizedText; addLabel?: LocalizedText; emptyText?: LocalizedText }
  inputs: JSPluginAutomationInput[]
}

export type JSPluginSiteProviderView = {
  siteType: string
  displayName?: LocalizedText
  hosts: string[]
}

export function siteProviderOf(manifest?: Record<string, unknown>): JSPluginSiteProviderView | null {
  if (!manifest) return null
  const kind = typeof manifest.kind === 'string' ? manifest.kind : ''
  // A kindless package may still declare a site type through siteProvider.siteType.
  if (kind !== '' && kind !== 'site_provider') return null
  const raw = manifest.siteProvider
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const section = raw as Record<string, unknown>
  if (kind === '' && (typeof section.siteType !== 'string' || section.siteType.trim() === '')) return null
  const display = section.display
  let displayName: LocalizedText | undefined
  if (display && typeof display === 'object' && !Array.isArray(display)) {
    const name = (display as Record<string, unknown>).name
    if (typeof name === 'string' || (name && typeof name === 'object' && !Array.isArray(name))) {
      displayName = name as LocalizedText
    }
  }
  const hosts = Array.isArray(section.hosts)
    ? section.hosts.filter((item): item is string => typeof item === 'string' && item.trim() !== '')
    : []
  return {
    siteType: typeof section.siteType === 'string' ? section.siteType : '',
    displayName,
    hosts,
  }
}

export function grantedHostPermissions(granted?: string[]) {
  return (granted ?? []).filter((item) => item.startsWith('host:') && item.length > 'host:'.length)
}

export function automationManifest(manifest?: Record<string, unknown>): JSPluginAutomationManifest | null {
  const raw = manifest?.automation as Partial<JSPluginAutomationManifest> | undefined
  if (!raw || typeof raw !== 'object') return null
  return {
    subscribes: raw.subscribes ?? [],
    permissions: raw.permissions ?? [],
    schedule: raw.schedule,
    form: raw.form,
    inputs: raw.inputs ?? [],
  }
}

export type JSPluginAutomationValue = string | number | boolean | string[]

export type JSPluginAutomation = {
  id: string
  version: string
  inputs: Array<{
    name: string
    entities?: Array<{ type: string; id: string; name: string }>
    value?: string | number | boolean
  }>
  state: Record<string, unknown>
  created_at: string
}

export type JSPluginAutomationOption = {
  id: string
  name: string
  description?: string
  /** Brand key of the object (a site's type, an account's provider); the console maps it to a logo. */
  icon_key?: string
  disabled?: boolean
  reason_code?: string
  reason?: string
}

export type JSPluginActionLogEntry = {
  id: number
  version: string
  event_type: string
  action: { type?: string; scope?: string; message?: string; method?: string; url?: string }
  target_id: string
  status: 'applied' | 'skipped' | 'failed'
  detail: string
  created_at: string
}

export const jsPluginAutomationQueryKeys = {
  list: (pluginId: string) => [...jsPluginQueryKeys.all, 'automations', pluginId] as const,
  log: (pluginId: string) => [...jsPluginQueryKeys.all, 'action-log', pluginId] as const,
  options: (pluginId: string, inputName: string) => [...jsPluginQueryKeys.all, 'options', pluginId, inputName] as const,
}

export async function listJSPluginAutomations(pluginId: string, signal?: AbortSignal) {
  const result = await apiFetch<{ items: JSPluginAutomation[] }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/automations`,
    { signal },
  )
  return result.items ?? []
}

export async function createJSPluginAutomation(pluginId: string, inputs: Record<string, JSPluginAutomationValue>) {
  return apiFetch<{ ok: boolean; id: string }>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}/automations`, {
    method: 'POST',
    body: { inputs },
  })
}

export async function updateJSPluginAutomation(
  pluginId: string,
  bindingId: string,
  inputs: Record<string, JSPluginAutomationValue>,
) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/automations/${encodeURIComponent(bindingId)}`,
    { method: 'PUT', body: { inputs } },
  )
}

export async function listJSPluginAutomationOptions(pluginId: string, inputName: string, signal?: AbortSignal) {
  const result = await apiFetch<{ items: JSPluginAutomationOption[] }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/inputs/${encodeURIComponent(inputName)}/options`,
    { signal },
  )
  return result.items ?? []
}

export async function deleteJSPluginAutomation(pluginId: string, bindingId: string) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/automations/${encodeURIComponent(bindingId)}`,
    { method: 'DELETE' },
  )
}

export async function listJSPluginActionLog(pluginId: string, signal?: AbortSignal) {
  const result = await apiFetch<{ items: JSPluginActionLogEntry[] }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/action-log?limit=30`,
    { signal },
  )
  return result.items ?? []
}

// ---- gateway pipeline bindings ----

export type JSPluginGatewayPipelineManifest = {
  scope: string
  exclusive?: boolean
  failurePolicy?: 'fail_open' | 'fail_closed' | string
  unsupportedEndpoint?: 'pass' | 'reject' | string
  transform?: {
    mode?: string
    reversible?: boolean
    propagateMinLength?: number
    systemHint?: string
  }
  form?: { title?: LocalizedText; description?: LocalizedText; addLabel?: LocalizedText; emptyText?: LocalizedText }
  inputs: JSPluginAutomationInput[]
}

export function gatewayPipelineManifest(manifest?: Record<string, unknown>): JSPluginGatewayPipelineManifest | null {
  const raw = manifest?.gatewayPipeline as Partial<JSPluginGatewayPipelineManifest> | undefined
  if (!raw || typeof raw !== 'object') return null
  return {
    scope: typeof raw.scope === 'string' ? raw.scope : 'api_key',
    exclusive: raw.exclusive,
    failurePolicy: raw.failurePolicy,
    unsupportedEndpoint: raw.unsupportedEndpoint,
    transform: raw.transform,
    form: raw.form,
    inputs: raw.inputs ?? [],
  }
}

export type JSPluginPipelineHostOptionSpec = {
  key: string
  type: 'boolean' | string
  default: boolean | string
}

export type JSPluginPipelineHostOptions = {
  propagateSameValue?: boolean
  scanToolDefinitions?: boolean
  transformReasoning?: boolean
}

export type JSPluginPipelineBinding = {
  id: string
  version: string
  name: string
  inputs: Array<{
    name: string
    entities?: Array<{ type: string; id: string; name: string }>
    value?: string | number | boolean
  }>
  host_options: JSPluginPipelineHostOptions
  policy: {
    failurePolicy?: string
    unsupportedEndpoint?: string
    pluginName?: string
    inspectRaw?: boolean
  }
  status: 'active' | 'unavailable' | 'bypassed' | string
  unavailable_reason?: string
  created_at: string
}

export type JSPluginPipelineDryRunFinding = {
  segment_id: string
  role?: string
  field: string
  start: number
  end: number
  type: string
  source?: string
  score?: number
  placeholder?: string
  applied: boolean
}

export type JSPluginPipelineDryRunResult = {
  outcome: string
  changed: boolean
  bypassed?: boolean
  error_kind?: string
  metrics: {
    plugin_id: string
    binding_id: string
    outcome: string
    counts?: Record<string, number>
    segments: number
    cached: number
    hook_calls?: number
    latency_ms: number
  }
  failure?: {
    status: number
    code: string
    message: string
  }
  findings: JSPluginPipelineDryRunFinding[]
  transformed_text?: string
  transformed_payload?: Record<string, unknown>
}

export const jsPluginPipelineQueryKeys = {
  list: (pluginId: string) => [...jsPluginQueryKeys.all, 'pipeline-bindings', pluginId] as const,
  options: (pluginId: string, inputName: string, bindingId?: string) =>
    [...jsPluginQueryKeys.all, 'pipeline-options', pluginId, inputName, bindingId ?? ''] as const,
}

export async function listJSPluginPipelineBindings(pluginId: string, signal?: AbortSignal) {
  return apiFetch<{ items: JSPluginPipelineBinding[]; host_options: JSPluginPipelineHostOptionSpec[] }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/bindings`,
    { signal },
  )
}

export async function createJSPluginPipelineBinding(
  pluginId: string,
  body: { name: string; inputs: Record<string, JSPluginAutomationValue>; host_options: Record<string, unknown> },
) {
  return apiFetch<{ ok: boolean; id: string }>(`/api/v1/js-plugins/${encodeURIComponent(pluginId)}/bindings`, {
    method: 'POST',
    body,
  })
}

export async function updateJSPluginPipelineBinding(
  pluginId: string,
  bindingId: string,
  body: { name: string; inputs: Record<string, JSPluginAutomationValue>; host_options: Record<string, unknown> },
) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/bindings/${encodeURIComponent(bindingId)}`,
    { method: 'PUT', body },
  )
}

export async function deleteJSPluginPipelineBinding(pluginId: string, bindingId: string) {
  return apiFetch<{ ok: boolean }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/bindings/${encodeURIComponent(bindingId)}`,
    { method: 'DELETE' },
  )
}

export async function listJSPluginPipelineInputOptions(
  pluginId: string,
  inputName: string,
  bindingId?: string,
  signal?: AbortSignal,
) {
  const qs = bindingId ? `?binding_id=${encodeURIComponent(bindingId)}` : ''
  const result = await apiFetch<{ items: JSPluginAutomationOption[] }>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/pipeline-inputs/${encodeURIComponent(inputName)}/options${qs}`,
    { signal },
  )
  return result.items ?? []
}

export async function dryRunJSPluginPipelineBinding(
  pluginId: string,
  bindingId: string,
  body: { text?: string; endpoint?: string; model?: string; payload?: Record<string, unknown> },
) {
  return apiFetch<JSPluginPipelineDryRunResult>(
    `/api/v1/js-plugins/${encodeURIComponent(pluginId)}/bindings/${encodeURIComponent(bindingId)}/dry-run`,
    { method: 'POST', body },
  )
}
