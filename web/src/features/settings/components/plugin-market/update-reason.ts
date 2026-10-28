export function updateReasonText(
  t: (key: string, options?: { detail?: string }) => string,
  reason?: string,
  detail?: string,
) {
  if (!reason) return ''
  return t(`jsPlugins.market.autoReason.${reason}`, { detail: detail || '' })
}

export function isUpdatePolicy(value: string | undefined): value is 'off' | 'notify' | 'auto' {
  return value === 'off' || value === 'notify' || value === 'auto'
}
