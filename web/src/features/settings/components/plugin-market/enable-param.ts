const ENABLE_PARAM = /^([a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)@(\d+\.\d+\.\d+)$/

export function parseEnableParam(value: string | null | undefined): { id: string; version: string } | null {
  if (value == null) return null
  const match = value.trim().match(ENABLE_PARAM)
  if (!match) return null
  return { id: match[1], version: match[2] }
}
