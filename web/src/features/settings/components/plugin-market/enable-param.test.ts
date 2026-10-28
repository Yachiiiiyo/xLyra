import { describe, expect, it } from 'vitest'
import { parseEnableParam } from './enable-param'

describe('parseEnableParam', () => {
  it('accepts an id with dots and hyphens and an x.y.z version', () => {
    expect(parseEnableParam('acme.one@1.2.0')).toEqual({ id: 'acme.one', version: '1.2.0' })
    expect(parseEnableParam('acme-tools.beta@10.0.1')).toEqual({ id: 'acme-tools.beta', version: '10.0.1' })
    expect(parseEnableParam(' a@0.0.1 ')).toEqual({ id: 'a', version: '0.0.1' })
  })

  it('returns null for malformed input', () => {
    expect(parseEnableParam(null)).toBeNull()
    expect(parseEnableParam(undefined)).toBeNull()
    expect(parseEnableParam('')).toBeNull()
    expect(parseEnableParam('acme.one')).toBeNull()
    expect(parseEnableParam('acme.one@1.2')).toBeNull()
    expect(parseEnableParam('acme.one@1.2.3.4')).toBeNull()
    expect(parseEnableParam('@1.2.0')).toBeNull()
    expect(parseEnableParam('acme.one@1.2.0-beta')).toBeNull()
    expect(parseEnableParam('acme one@1.0.0')).toBeNull()
    expect(parseEnableParam('.acme@1.0.0')).toBeNull()
    expect(parseEnableParam('acme.@1.0.0')).toBeNull()
  })
})
