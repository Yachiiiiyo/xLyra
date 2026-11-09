export const PERMISSION_MODE_KEY = 'xlyra-agent-permission-mode'

export type PermissionMode = 'ask' | 'auto' | 'full'

export function loadPermissionMode(): PermissionMode {
  try {
    const mode = window.localStorage.getItem(PERMISSION_MODE_KEY)
    return mode === 'ask' || mode === 'auto' || mode === 'full' ? mode : 'ask'
  } catch {
    return 'ask'
  }
}
