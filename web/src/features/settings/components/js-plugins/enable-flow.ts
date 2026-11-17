import type { JSPluginErrorFields } from '@/features/settings/components/js-plugins/js-plugin-errors'

export type EnableTrust = 'trusted' | 'untrusted_signer' | 'unsigned' | 'marketplace'

export type EnableFlowState = {
  pluginId: string
  version: string
  trust: EnableTrust
  signer?: string
  permissions: string[]
  /** A declared site type, or secret.use, can never skip the trusted signature. */
  requiresTrusted: boolean
  trustAccepted: boolean
  hosts: string[]
  hostsAccepted: boolean
  /** Hooks the package exports, shown next to the permissions and hosts. */
  exports: string[]
  dialog: 'confirm' | null
}

export type EnableRequest = {
  pluginId: string
  version: string
  confirmUntrusted?: boolean
  grantPermissions?: string[]
  confirmHosts?: string[]
}

export type EnableFlowStep =
  | { type: 'dialog'; state: EnableFlowState }
  | { type: 'request'; state: EnableFlowState; request: EnableRequest }
  | { type: 'toast'; state: EnableFlowState; code: string; fields: JSPluginErrorFields }
  | { type: 'cancel' }
  | { type: 'unhandled'; state: EnableFlowState }

export type BeginEnableInput = {
  pluginId: string
  version: string
  trust?: EnableTrust
  signer?: string
  permissions?: string[]
  requiresTrusted?: boolean
  /** Hosts already known from the manifest (siteProvider.hosts and net: permissions). */
  hosts?: string[]
  exports?: string[]
}

export type EnableFailure = {
  code?: string
  fields?: JSPluginErrorFields
  trust?: EnableTrust
  signer?: string
  permissions?: string[]
  requiresTrusted?: boolean
  hosts?: string[]
  exports?: string[]
}

function countsAsTrusted(trust: EnableTrust) {
  return trust === 'trusted' || trust === 'marketplace'
}

export function enableFlowOffersUntrusted(state: EnableFlowState) {
  return !state.requiresTrusted && !countsAsTrusted(state.trust)
}

function needsConfirm(state: EnableFlowState) {
  if (!countsAsTrusted(state.trust) && !state.trustAccepted) return true
  if (state.permissions.length > 0 && !state.trustAccepted) return true
  if (state.hosts.length > 0 && !state.hostsAccepted) return true
  return false
}

function requestOf(state: EnableFlowState): EnableRequest {
  const request: EnableRequest = { pluginId: state.pluginId, version: state.version }
  if (state.trustAccepted && enableFlowOffersUntrusted(state)) request.confirmUntrusted = true
  if (state.trustAccepted && state.permissions.length > 0) request.grantPermissions = [...state.permissions]
  if (state.hostsAccepted && state.hosts.length > 0) request.confirmHosts = [...state.hosts]
  return request
}

/** One dialog covers trust, permissions, hosts and exports. Nothing to confirm sends the request. */
function present(state: EnableFlowState): EnableFlowStep {
  if (state.requiresTrusted && !countsAsTrusted(state.trust)) {
    return { type: 'toast', state: { ...state, dialog: null }, code: 'js_plugin_untrusted_not_allowed', fields: {} }
  }
  if (needsConfirm(state)) return { type: 'dialog', state: { ...state, dialog: 'confirm' } }
  return { type: 'request', state: { ...state, dialog: null }, request: requestOf(state) }
}

export function beginEnable(input: BeginEnableInput): EnableFlowStep {
  return present({
    pluginId: input.pluginId,
    version: input.version,
    trust: input.trust ?? 'trusted',
    signer: input.signer,
    permissions: input.permissions ?? [],
    requiresTrusted: input.requiresTrusted ?? false,
    trustAccepted: false,
    hosts: input.hosts ?? [],
    hostsAccepted: false,
    exports: input.exports ?? [],
    dialog: null,
  })
}

export function resumeEnableFlow(input: BeginEnableInput & {
  confirmUntrusted?: boolean
  grantPermissions?: string[]
  confirmHosts?: string[]
}): EnableFlowState {
  const granted = input.grantPermissions ?? []
  const hosts = input.confirmHosts
  const trust = input.confirmUntrusted && (input.trust ?? 'trusted') === 'trusted'
    ? 'unsigned'
    : (input.trust ?? 'trusted')
  return {
    pluginId: input.pluginId,
    version: input.version,
    trust,
    signer: input.signer,
    permissions: (input.permissions ?? []).length > 0 ? (input.permissions ?? []) : granted,
    requiresTrusted: input.requiresTrusted ?? false,
    trustAccepted: Boolean(input.confirmUntrusted) || granted.length > 0,
    hosts: hosts ?? input.hosts ?? [],
    hostsAccepted: Array.isArray(hosts),
    exports: input.exports ?? [],
    dialog: null,
  }
}

export function acceptEnableDialog(state: EnableFlowState): EnableFlowStep {
  if (state.dialog !== 'confirm') return present(state)
  return present({ ...state, trustAccepted: true, hostsAccepted: true })
}

export function cancelEnable(): EnableFlowStep {
  return { type: 'cancel' }
}

function union(current: string[], extra?: string[]) {
  if (!extra || extra.length === 0) return current
  const seen = new Set(current)
  const out = [...current]
  for (const item of extra) {
    if (!seen.has(item)) {
      seen.add(item)
      out.push(item)
    }
  }
  return out
}

export function failEnable(state: EnableFlowState, failure: EnableFailure): EnableFlowStep {
  const next: EnableFlowState = {
    ...state,
    dialog: null,
    trust: failure.trust ?? state.trust,
    signer: failure.signer ?? state.signer,
    permissions: failure.permissions ?? state.permissions,
    requiresTrusted: failure.requiresTrusted ?? state.requiresTrusted,
    exports: failure.exports ?? state.exports,
  }
  const fields = failure.fields ?? {}
  switch (failure.code) {
    case 'js_plugin_untrusted_not_allowed':
      return { type: 'toast', state: next, code: failure.code, fields }
    case 'js_plugin_confirm_required':
      if (next.requiresTrusted) {
        return { type: 'toast', state: next, code: 'js_plugin_untrusted_not_allowed', fields }
      }
      return present({ ...next, trustAccepted: false })
    case 'js_plugin_permissions_required':
      return present({
        ...next,
        permissions: union(next.permissions, failure.permissions ?? fields.required),
        trustAccepted: false,
      })
    case 'js_plugin_hosts_required': {
      const hosts = failure.hosts ?? fields.hosts
      if (!hosts || hosts.length === 0) {
        return { type: 'toast', state: next, code: failure.code, fields }
      }
      return present({ ...next, hosts: union(next.hosts, hosts), hostsAccepted: false })
    }
    case 'js_plugin_site_type_locked':
    case 'js_plugin_in_use':
      return { type: 'toast', state: next, code: failure.code, fields }
    default:
      return { type: 'unhandled', state: next }
  }
}
