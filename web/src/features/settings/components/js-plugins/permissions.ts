/** Locale key (under settings:jsPlugins.permission) of a permission the enable dialog explains. */
const PERMISSION_LABEL_KEYS: Record<string, string> = {
  'apikey.reset_usage': 'apikeyResetUsage',
  notify: 'notify',
  'http.request': 'httpRequest',
  storage: 'storage',
  'secret.use': 'secretUse',
  'payload.inspect_raw': 'payloadInspectRaw',
  http: 'http',
  schedule: 'schedule',
}

export function permissionLabelKey(permission: string) {
  if (permission.startsWith('net:') && permission.length > 'net:'.length) return 'settings:jsPlugins.permission.net'
  if (permission.startsWith('event:') && permission.length > 'event:'.length) return 'settings:jsPlugins.permission.event'
  const key = PERMISSION_LABEL_KEYS[permission]
  return key ? `settings:jsPlugins.permission.${key}` : null
}

/** Interpolation values for permissions whose explanation names a host or event. */
export function permissionLabelParams(permission: string): Record<string, string> | undefined {
  if (permission.startsWith('net:') && permission.length > 'net:'.length) return { host: permission.slice('net:'.length) }
  if (permission.startsWith('event:') && permission.length > 'event:'.length) return { event: permission.slice('event:'.length) }
  return undefined
}

export function isSecretUsePermission(permission: string) {
  return permission === 'secret.use' || permission === 'payload.inspect_raw'
}
