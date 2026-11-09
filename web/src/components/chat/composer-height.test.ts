import { describe, expect, it } from 'vitest'
import { COMPOSER_LINE_HEIGHT, COMPOSER_MAX_HEIGHT, composerTextareaHeight } from '@/components/chat/composer-height'

describe('composerTextareaHeight', () => {
  it('defaults to a single row as the lower bound', () => {
    expect(composerTextareaHeight(0)).toBe(COMPOSER_LINE_HEIGHT)
    expect(composerTextareaHeight(COMPOSER_LINE_HEIGHT)).toBe(COMPOSER_LINE_HEIGHT)
  })

  it('grows with content but never beyond the cap', () => {
    expect(composerTextareaHeight(120, 3)).toBe(120)
    expect(composerTextareaHeight(COMPOSER_MAX_HEIGHT + 500, 3)).toBe(COMPOSER_MAX_HEIGHT)
  })

  it('never shrinks below minRows', () => {
    expect(composerTextareaHeight(24, 3)).toBe(3 * COMPOSER_LINE_HEIGHT)
    expect(composerTextareaHeight(0, 3)).toBe(3 * COMPOSER_LINE_HEIGHT)
  })

  it('treats row counts below one as one row', () => {
    expect(composerTextareaHeight(10, 0)).toBe(COMPOSER_LINE_HEIGHT)
  })
})
