import type { GatewayModel, GatewayModelReasoning, ReasoningEffort } from '@/lib/chat-types'

const BASE_REASONING_EFFORTS: ReasoningEffort[] = ['low', 'medium', 'high', 'xhigh']

// Canonical ladder shared with the gateway (server/internal/gateway/reasoning_effort.go):
// server-declared levels are a subset of this list, and snapping walks it by rank.
const EFFORT_RANKS: ReasoningEffort[] = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra']

type ReasoningModel = Pick<GatewayModel, 'id' | 'mappedModel' | 'reasoning'>

function normalizedReasoningModel(model: ReasoningModel | null | undefined): string {
  return (model?.mappedModel || model?.id || '').trim().toLowerCase()
}

/**
 * Parses the reasoning_effort block the gateway exposes on model payloads
 * (metadata.reasoning_effort on /v1/models, reasoning_effort on /api/v1/models).
 * Returns undefined when the server declares nothing usable, so callers fall
 * back to the hardcoded rules below.
 */
export function parseGatewayModelReasoning(value: unknown): GatewayModelReasoning | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawLevels = (value as { levels?: unknown }).levels
  if (!Array.isArray(rawLevels)) return undefined
  const levels = rawLevels
    .map((item) => (typeof item === 'string' ? item.trim().toLowerCase() : ''))
    .filter((item): item is ReasoningEffort => (EFFORT_RANKS as string[]).includes(item))
  const unique = [...new Set(levels)].sort((a, b) => EFFORT_RANKS.indexOf(a) - EFFORT_RANKS.indexOf(b))
  if (unique.length === 0) return undefined
  const thinkingMandatory = (value as { thinking_mandatory?: unknown }).thinking_mandatory === true
  return { levels: unique, thinkingMandatory }
}

/** Snaps an effort onto the supported level set by rank: exact match passes,
 * otherwise nearest by rank, ties resolve upward, out-of-range clamps. Mirrors
 * the gateway's snapReasoningEffort. */
function snapToLevels(effort: ReasoningEffort, levels: ReasoningEffort[]): ReasoningEffort {
  if (levels.includes(effort)) return effort
  const rank = EFFORT_RANKS.indexOf(effort)
  let best = levels[0] ?? effort
  let bestDistance = Number.POSITIVE_INFINITY
  for (const level of levels) {
    const distance = Math.abs(EFFORT_RANKS.indexOf(level) - rank)
    if (distance < bestDistance || (distance === bestDistance && EFFORT_RANKS.indexOf(level) > EFFORT_RANKS.indexOf(best))) {
      best = level
      bestDistance = distance
    }
  }
  return best
}

// ---- Hardcoded fallback rules, used only when the server declares no levels ----

export function supportsMaxReasoning(model: ReasoningModel | null | undefined): boolean {
  const normalized = normalizedReasoningModel(model)
  return normalized === 'gpt-5.6' || normalized.startsWith('gpt-5.6-') || normalized === 'gpt-6-astra'
}

export function supportsUltraReasoning(model: ReasoningModel | null | undefined): boolean {
  const normalized = normalizedReasoningModel(model)
  return normalized === 'gpt-5.6-sol' || normalized === 'gpt-5.6-terra' || normalized === 'gpt-6-astra'
}

function fallbackReasoningEffortsForModel(model: ReasoningModel | null | undefined): ReasoningEffort[] {
  const efforts = [...BASE_REASONING_EFFORTS]
  if (supportsMaxReasoning(model)) efforts.push('max')
  if (supportsUltraReasoning(model)) efforts.push('ultra')
  return efforts
}

/** Levels offered in the picker. "none" (thinking off) is never shown —
 * disabling thinking is not a choice the UI exposes. */
export function reasoningEffortsForModel(model: ReasoningModel | null | undefined): ReasoningEffort[] {
  const levels = model?.reasoning?.levels ?? fallbackReasoningEffortsForModel(model)
  return levels.filter((level) => level !== 'none')
}

export function normalizeReasoningEffort(model: ReasoningModel | null | undefined, effort: ReasoningEffort): ReasoningEffort {
  if (model?.reasoning) {
    return snapToLevels(effort, model.reasoning.levels)
  }
  if (effort === 'ultra' && !supportsUltraReasoning(model)) {
    return supportsMaxReasoning(model) ? 'max' : 'xhigh'
  }
  return effort === 'max' && !supportsMaxReasoning(model) ? 'xhigh' : effort
}
