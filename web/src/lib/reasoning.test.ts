import { describe, expect, it } from 'vitest'
import {
  normalizeReasoningEffort,
  parseGatewayModelReasoning,
  reasoningEffortsForModel,
  supportsMaxReasoning,
  supportsUltraReasoning,
} from '@/lib/reasoning'
import type { GatewayModelReasoning, ReasoningEffort } from '@/lib/chat-types'

const serverLevels = (levels: ReasoningEffort[]): GatewayModelReasoning => ({ levels })

describe('chat reasoning effort rules', () => {
  it('offers max across the gpt-5.6 family and ultra only for sol and terra', () => {
    expect(supportsMaxReasoning({ id: 'gpt-5.6' })).toBe(true)
    expect(supportsMaxReasoning({ id: 'gpt-5.6-luna' })).toBe(true)
    expect(supportsMaxReasoning({ id: 'gpt-5.5' })).toBe(false)
    expect(supportsUltraReasoning({ id: 'gpt-5.6-sol' })).toBe(true)
    expect(supportsUltraReasoning({ id: ' GPT-5.6-Terra ' })).toBe(true)
    expect(supportsUltraReasoning({ id: 'gpt-5.6-luna' })).toBe(false)

    expect(reasoningEffortsForModel({ id: 'gpt-5.6-sol' })).toEqual(['low', 'medium', 'high', 'xhigh', 'max', 'ultra'])
    expect(reasoningEffortsForModel({ id: 'gpt-5.6-luna' })).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
    expect(reasoningEffortsForModel({ id: 'gpt-5.5' })).toEqual(['low', 'medium', 'high', 'xhigh'])
  })

  it('offers max and ultra for gpt-6-astra in the fallback rules', () => {
    expect(supportsMaxReasoning({ id: 'gpt-6-astra' })).toBe(true)
    expect(supportsUltraReasoning({ id: 'gpt-6-astra' })).toBe(true)
    expect(reasoningEffortsForModel({ id: 'gpt-6-astra' })).toEqual(['low', 'medium', 'high', 'xhigh', 'max', 'ultra'])
    expect(normalizeReasoningEffort({ id: 'gpt-6-astra' }, 'ultra')).toBe('ultra')
    expect(supportsUltraReasoning({ id: 'gpt-6-astra-mini' })).toBe(false)
  })

  it('downgrades unsupported max and ultra to the strongest available effort', () => {
    expect(normalizeReasoningEffort({ id: 'gpt-5.5' }, 'max')).toBe('xhigh')
    expect(normalizeReasoningEffort({ id: 'gpt-5.5' }, 'ultra')).toBe('xhigh')
    expect(normalizeReasoningEffort({ id: 'gpt-5.6-luna' }, 'ultra')).toBe('max')
    expect(normalizeReasoningEffort({ id: 'gpt-5.6-terra' }, 'ultra')).toBe('ultra')
    expect(normalizeReasoningEffort({ id: 'gpt-5.4' }, 'high')).toBe('high')
  })

  it('uses the mapped target when checking model aliases', () => {
    const mappedTo56 = { id: 'codex-pro', mappedModel: 'gpt-5.6-sol' }
    const mappedTo55 = { id: 'gpt-5.6-custom', mappedModel: 'gpt-5.5' }

    expect(reasoningEffortsForModel(mappedTo56)).toEqual(['low', 'medium', 'high', 'xhigh', 'max', 'ultra'])
    expect(normalizeReasoningEffort(mappedTo56, 'ultra')).toBe('ultra')
    expect(reasoningEffortsForModel(mappedTo55)).toEqual(['low', 'medium', 'high', 'xhigh'])
    expect(normalizeReasoningEffort(mappedTo55, 'ultra')).toBe('xhigh')
  })

  it('parses the server-declared reasoning_effort block, dropping unknown levels and sorting by rank', () => {
    expect(parseGatewayModelReasoning(undefined)).toBeUndefined()
    expect(parseGatewayModelReasoning(null)).toBeUndefined()
    expect(parseGatewayModelReasoning({})).toBeUndefined()
    expect(parseGatewayModelReasoning({ levels: 'high' })).toBeUndefined()
    expect(parseGatewayModelReasoning({ levels: ['turbo'] })).toBeUndefined()

    expect(parseGatewayModelReasoning({ levels: ['MAX', ' low ', 'high', 'low', 'turbo'] }))
      .toEqual({ levels: ['low', 'high', 'max'], thinkingMandatory: false })
    expect(parseGatewayModelReasoning({ levels: ['low', 'high'], thinking_mandatory: true }))
      .toEqual({ levels: ['low', 'high'], thinkingMandatory: true })
  })

  it('never offers none in the picker, even when the model supports it', () => {
    const glm52 = { id: 'glm-5.2', reasoning: serverLevels(['none', 'high', 'max']) }
    expect(reasoningEffortsForModel(glm52)).toEqual(['high', 'max'])
    const gpt5 = { id: 'gpt-5.5', reasoning: serverLevels(['none', 'minimal', 'low', 'medium', 'high', 'xhigh']) }
    expect(reasoningEffortsForModel(gpt5)).toEqual(['minimal', 'low', 'medium', 'high', 'xhigh'])
  })

  it('prefers server-declared levels over the hardcoded rules', () => {
    // kimi-k3 style: server says low/high/max even though the id matches no hardcoded rule.
    const kimi = { id: 'kimi-k3', reasoning: serverLevels(['low', 'high', 'max']) }
    expect(reasoningEffortsForModel(kimi)).toEqual(['low', 'high', 'max'])
    // A gpt-5.6 variant whose server levels exclude ultra wins over the fallback.
    const gated = { id: 'gpt-5.6-sol', reasoning: serverLevels(['low', 'medium', 'high', 'xhigh', 'max']) }
    expect(reasoningEffortsForModel(gated)).toEqual(['low', 'medium', 'high', 'xhigh', 'max'])
  })

  it('snaps to server levels by rank: exact passes, ties resolve upward, out-of-range clamps', () => {
    const model = { id: 'glm-5.3', reasoning: serverLevels(['low', 'high', 'max']) }
    expect(normalizeReasoningEffort(model, 'high')).toBe('high')
    expect(normalizeReasoningEffort(model, 'medium')).toBe('high') // equidistant tie goes up
    expect(normalizeReasoningEffort(model, 'xhigh')).toBe('max') // equidistant tie goes up
    expect(normalizeReasoningEffort(model, 'ultra')).toBe('max')
    expect(normalizeReasoningEffort(model, 'none')).toBe('low')
    expect(normalizeReasoningEffort(model, 'minimal')).toBe('low')
  })

  it('snaps minimal-gap misses to the nearest level either way', () => {
    const opus46 = { id: 'claude-opus-4-6', reasoning: serverLevels(['low', 'medium', 'high', 'max']) }
    expect(normalizeReasoningEffort(opus46, 'xhigh')).toBe('max') // equidistant tie goes up
    expect(normalizeReasoningEffort(opus46, 'ultra')).toBe('max')
    expect(normalizeReasoningEffort(opus46, 'minimal')).toBe('low')
  })
})
