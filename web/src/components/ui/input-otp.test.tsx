import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { formatOTPValue, InputOTP, insertOTPChars, otpChars } from './input-otp'

describe('InputOTP', () => {
  it('renders a 4+4 code with a separator', () => {
    const html = renderToStaticMarkup(
      <InputOTP readOnly value="gyd4-lmgt" aria-label="配对码" testId="hub-pairing-code" />,
    )
    expect(html).toContain('value="GYD4-LMGT"')
    expect(html).toContain('data-testid="hub-pairing-code"')
    expect(html.match(/data-otp-slot="/g)).toHaveLength(8)
    expect(html).toContain('>-</span>')
    expect(html).toContain('readOnly')
  })

  it('splits, formats, and pastes across the hyphen', () => {
    expect(otpChars('ab-cd', 8)).toEqual(['A', 'B', 'C', 'D'])
    expect(formatOTPValue(['G', 'Y', 'D', '4', 'L'], [4, 4], '-')).toBe('GYD4-L')
    expect(formatOTPValue(['G', 'Y', 'D', '4', 'L', 'M', 'G', 'T'], [4, 4], '-')).toBe('GYD4-LMGT')
    const pasted = insertOTPChars(['', '', '', '', '', '', '', ''], 0, 'gyd4-lmgt', 8)
    expect(formatOTPValue(pasted.chars, [4, 4], '-')).toBe('GYD4-LMGT')
    expect(pasted.cursor).toBe(8)
  })
})