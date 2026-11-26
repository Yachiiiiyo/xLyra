export type JSPluginErrorFields = {
  hosts?: string[]
  /** Permissions the server still wants granted (js_plugin_permissions_required). */
  required?: string[]
  from?: string
  to?: string
  count?: number
  site_type?: string
  scope?: string
}

export type JSPluginErrorNotice = {
  key: string
  params?: Record<string, string | number>
}

export function readJSPluginErrorFields(details: Record<string, unknown> | null | undefined): JSPluginErrorFields {
  if (!details) return {}
  const fields: JSPluginErrorFields = {}
  if (Array.isArray(details.hosts) && details.hosts.every((item) => typeof item === 'string' && item.trim() !== '')) {
    fields.hosts = details.hosts
  }
  if (Array.isArray(details.required) && details.required.every((item) => typeof item === 'string' && item.trim() !== '')) {
    fields.required = details.required
  }
  if (typeof details.from === 'string') fields.from = details.from
  if (typeof details.to === 'string') fields.to = details.to
  if (typeof details.count === 'number' && Number.isFinite(details.count)) fields.count = details.count
  if (typeof details.site_type === 'string') fields.site_type = details.site_type
  if (typeof details.scope === 'string') fields.scope = details.scope
  return fields
}

export function jsPluginErrorNotice(code: string | undefined, fields: JSPluginErrorFields = {}): JSPluginErrorNotice | null {
  switch (code) {
    case 'js_plugin_untrusted_not_allowed':
      return { key: 'settings:jsPlugins.errors.untrustedNotAllowed' }
    case 'js_plugin_site_type_locked':
      return {
        key: 'settings:jsPlugins.errors.siteTypeLocked',
        params: { from: fields.from ?? '', to: fields.to ?? '', count: fields.count ?? 0 },
      }
    case 'js_plugin_in_use':
      if (fields.scope === 'gateway_pipeline') {
        return {
          key: 'settings:jsPlugins.errors.pipelineInUse',
          params: { count: fields.count ?? 0 },
        }
      }
      return {
        key: 'settings:jsPlugins.errors.inUse',
        params: { count: fields.count ?? 0, site_type: fields.site_type ?? '' },
      }
    case 'js_plugin_hosts_required':
      return { key: 'settings:jsPlugins.errors.hostsRequired' }
    default:
      return null
  }
}
