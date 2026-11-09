const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

export function isUUID(value: unknown): value is string {
  return typeof value === 'string' && UUID_PATTERN.test(value)
}

function randomUUIDFromValues(): string {
  const bytes = new Uint8Array(16)
  const cryptoObject = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined
  let filled = false
  if (cryptoObject && typeof cryptoObject.getRandomValues === 'function') {
    try {
      cryptoObject.getRandomValues(bytes)
      filled = true
    } catch {
      filled = false
    }
  }
  if (!filled) {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256)
    }
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export function newId(): string {
  const cryptoObject = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined
  if (cryptoObject && typeof cryptoObject.randomUUID === 'function') {
    try {
      const value = cryptoObject.randomUUID()
      if (isUUID(value)) return value
    } catch {
      // Continue with the UUID v4 fallback below.
    }
  }
  return randomUUIDFromValues()
}
