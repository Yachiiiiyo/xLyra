import { describe, expect, it } from 'vitest'
import {
  acceptEnableDialog,
  beginEnable,
  cancelEnable,
  enableFlowOffersUntrusted,
  failEnable,
  resumeEnableFlow,
  type EnableFlowState,
  type EnableFlowStep,
} from '@/features/settings/components/js-plugins/enable-flow'
import { jsPluginErrorNotice } from '@/features/settings/components/js-plugins/js-plugin-errors'

function stateOf(step: EnableFlowStep): EnableFlowState {
  if (step.type === 'cancel') throw new Error('missing state')
  return step.state
}

describe('jsPluginErrorNotice', () => {
  it.each([
    ['js_plugin_untrusted_not_allowed', {}, { key: 'settings:jsPlugins.errors.untrustedNotAllowed' }],
    ['js_plugin_site_type_locked', { from: 'acme.example/chat', to: 'acme.example/other', count: 2 }, {
      key: 'settings:jsPlugins.errors.siteTypeLocked',
      params: { from: 'acme.example/chat', to: 'acme.example/other', count: 2 },
    }],
    ['js_plugin_in_use', { site_type: 'acme.example/chat', count: 3 }, {
      key: 'settings:jsPlugins.errors.inUse',
      params: { count: 3, site_type: 'acme.example/chat' },
    }],
    ['js_plugin_hosts_required', {}, { key: 'settings:jsPlugins.errors.hostsRequired' }],
    ['js_plugin_unknown', {}, null],
    [undefined, {}, null],
  ] as const)('maps %s', (code, fields, notice) => {
    expect(jsPluginErrorNotice(code, fields)).toEqual(notice)
  })
})

describe('enable flow', () => {
  it('confirms trust, permissions and hosts in one dialog and sends all three', () => {
    const started = beginEnable({
      pluginId: 'auto.example',
      version: '1.0.0',
      trust: 'unsigned',
      permissions: ['notify', 'net:api.example.com'],
      hosts: ['api.example.com'],
      exports: ['probe'],
    })
    expect(started.type).toBe('dialog')
    expect(stateOf(started).dialog).toBe('confirm')
    expect(stateOf(started).hosts).toEqual(['api.example.com'])
    expect(stateOf(started).exports).toEqual(['probe'])
    expect(enableFlowOffersUntrusted(stateOf(started))).toBe(true)

    const accepted = acceptEnableDialog(stateOf(started))
    expect(accepted).toMatchObject({
      type: 'request',
      request: {
        pluginId: 'auto.example',
        version: '1.0.0',
        confirmUntrusted: true,
        grantPermissions: ['notify', 'net:api.example.com'],
        confirmHosts: ['api.example.com'],
      },
    })
  })

  it('requests immediately when the package is trusted and asks for nothing', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.2.0', trust: 'trusted' })
    expect(started).toMatchObject({
      type: 'request',
      request: { pluginId: 'acme.example', version: '1.2.0' },
    })
    if (started.type === 'request') {
      expect(started.request.confirmUntrusted).toBeUndefined()
      expect(started.request.grantPermissions).toBeUndefined()
      expect(started.request.confirmHosts).toBeUndefined()
    }
  })

  it('treats marketplace as already trusted', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.2.0', trust: 'marketplace' })
    expect(started.type).toBe('request')
    expect(enableFlowOffersUntrusted(stateOf(started))).toBe(false)
    if (started.type !== 'request') return
    expect(started.request).toEqual({ pluginId: 'acme.example', version: '1.2.0' })
  })

  it.each([
    ['a declared site type', { requiresTrusted: true }],
    ['secret.use', { requiresTrusted: true, permissions: ['secret.use'] }],
  ])('refuses an untrusted package that %s without a dialog', (_label, extra) => {
    const started = beginEnable({
      pluginId: 'acme.example',
      version: '1.0.0',
      trust: 'unsigned',
      ...extra,
    })
    expect(started).toMatchObject({ type: 'toast', code: 'js_plugin_untrusted_not_allowed' })
    expect(stateOf(started).dialog).toBeNull()
    expect(enableFlowOffersUntrusted(stateOf(started))).toBe(false)
  })

  it('still confirms hosts for a trusted package that requires a trusted signature', () => {
    const started = beginEnable({
      pluginId: 'acme.example',
      version: '1.0.0',
      trust: 'trusted',
      requiresTrusted: true,
      hosts: ['api.example.com'],
    })
    expect(stateOf(started).dialog).toBe('confirm')
    const accepted = acceptEnableDialog(stateOf(started))
    expect(accepted).toMatchObject({
      type: 'request',
      request: { confirmHosts: ['api.example.com'] },
    })
    if (accepted.type === 'request') expect(accepted.request.confirmUntrusted).toBeUndefined()
  })

  it('reopens the same dialog with the permissions the server still wants', () => {
    const started = beginEnable({ pluginId: 'auto.example', version: '2.0.0', trust: 'trusted' })
    expect(started.type).toBe('request')
    if (started.type !== 'request') return
    const again = failEnable(started.state, {
      code: 'js_plugin_permissions_required',
      fields: { required: ['notify', 'http'] },
    })
    expect(stateOf(again).dialog).toBe('confirm')
    expect(stateOf(again).permissions).toEqual(['notify', 'http'])

    const retried = acceptEnableDialog(stateOf(again))
    expect(retried).toMatchObject({
      type: 'request',
      request: { grantPermissions: ['notify', 'http'] },
    })
    if (retried.type === 'request') expect(retried.request.confirmUntrusted).toBeUndefined()
  })

  it('reopens the same dialog with the hosts the server returned', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.1.0', trust: 'trusted', requiresTrusted: true })
    expect(started.type).toBe('request')
    if (started.type !== 'request') return
    const asked = failEnable(started.state, {
      code: 'js_plugin_hosts_required',
      fields: { hosts: ['api.example.com', 'files.example.com'] },
    })
    expect(stateOf(asked).dialog).toBe('confirm')
    expect(stateOf(asked).hosts).toEqual(['api.example.com', 'files.example.com'])
    const accepted = acceptEnableDialog(stateOf(asked))
    expect(accepted).toMatchObject({
      type: 'request',
      request: { confirmHosts: ['api.example.com', 'files.example.com'] },
    })
    if (accepted.type === 'request') expect(accepted.request.confirmUntrusted).toBeUndefined()
  })

  it('keeps an accepted host list when permissions are required again', () => {
    const started = beginEnable({
      pluginId: 'auto.example',
      version: '2.0.0',
      trust: 'trusted',
      permissions: ['notify'],
      hosts: ['api.example.com'],
    })
    const granted = acceptEnableDialog(stateOf(started))
    if (granted.type !== 'request') throw new Error('expected request')
    const again = failEnable(granted.state, {
      code: 'js_plugin_permissions_required',
      permissions: ['notify', 'http.request'],
    })
    expect(stateOf(again).dialog).toBe('confirm')
    expect(stateOf(again).hosts).toEqual(['api.example.com'])
    const retried = acceptEnableDialog(stateOf(again))
    expect(retried).toMatchObject({
      type: 'request',
      request: {
        grantPermissions: ['notify', 'http.request'],
        confirmHosts: ['api.example.com'],
      },
    })
  })

  it('toasts when hosts_required has no host list', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.0.0', trust: 'trusted' })
    if (started.type !== 'request') throw new Error('expected request')
    const failed = failEnable(started.state, { code: 'js_plugin_hosts_required', fields: {} })
    expect(failed).toMatchObject({ type: 'toast', code: 'js_plugin_hosts_required' })
    expect(stateOf(failed).dialog).toBeNull()
  })

  it('turns a confirm_required of a trusted-signature package into the refusal toast', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.0.0', trust: 'trusted', requiresTrusted: true })
    if (started.type !== 'request') throw new Error('expected request')
    const confirm = failEnable(started.state, { code: 'js_plugin_confirm_required', trust: 'unsigned' })
    expect(confirm).toMatchObject({ type: 'toast', code: 'js_plugin_untrusted_not_allowed' })
  })

  it('does not enable when the confirmation is cancelled', () => {
    const started = beginEnable({ pluginId: 'acme.example', version: '1.0.0', trust: 'unsigned' })
    expect(started.type).toBe('dialog')
    expect(cancelEnable()).toEqual({ type: 'cancel' })
  })

  it('resumes confirm_untrusted from the request that was already sent', () => {
    const resumed = resumeEnableFlow({
      pluginId: 'auto.example',
      version: '1.0.0',
      trust: 'trusted',
      permissions: ['notify'],
      confirmUntrusted: true,
      grantPermissions: ['notify'],
    })
    const asked = failEnable(resumed, { code: 'js_plugin_hosts_required', fields: { hosts: ['api.example.com'] } })
    expect(stateOf(asked).dialog).toBe('confirm')
    const accepted = acceptEnableDialog(stateOf(asked))
    expect(accepted).toMatchObject({
      type: 'request',
      request: {
        confirmUntrusted: true,
        grantPermissions: ['notify'],
        confirmHosts: ['api.example.com'],
      },
    })
  })
})
