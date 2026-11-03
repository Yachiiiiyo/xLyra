import type { Conversation, PlaygroundSettings } from '@/features/playground/lib/types'
import { isUUID, newId } from '@/lib/id'

const CONVERSATIONS_KEY = 'xlyra-playground-conversations'
const SETTINGS_KEY = 'xlyra-playground-settings'

const MAX_CONVERSATIONS = 50

type ConversationIdentity = {
  id: string
  serverPersisted?: boolean
  lastOrdinal?: number
  activeRun?: unknown
}

export function migrateConversationIds<T extends ConversationIdentity>(items: T[]): { items: T[]; changed: boolean } {
  const seen = new Set<string>()
  let changed = false
  const migrated = items.map((conversation) => {
    if (isUUID(conversation.id) && !seen.has(conversation.id)) {
      seen.add(conversation.id)
      return conversation
    }
    let id = newId()
    while (seen.has(id)) id = newId()
    seen.add(id)
    changed = true
    return {
      ...conversation,
      id,
      serverPersisted: false,
      lastOrdinal: undefined,
      activeRun: undefined,
    }
  })
  return { items: migrated, changed }
}

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

function write(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    return
  }
}

export function loadConversations(): Conversation[] {
  const items = read<Conversation[]>(CONVERSATIONS_KEY, [])
  if (!Array.isArray(items)) return []
  const migrated = migrateConversationIds(items)
  if (migrated.changed) saveConversations(migrated.items)
  return migrated.items
}

export function saveConversations(items: Conversation[]) {
  write(CONVERSATIONS_KEY, items.slice(0, MAX_CONVERSATIONS).map((conversation) => ({
    ...conversation,
    messages: conversation.messages.map((message) => ({
      ...message,
      attachments: message.attachments?.map((attachment) => ({
        id: attachment.id,
        name: attachment.name,
        mimeType: attachment.mimeType,
        size: attachment.size,
        assetId: attachment.assetId,
        src: attachment.src,
      })),
    })),
  })))
}

export function loadSettings(): PlaygroundSettings {
  const defaults: PlaygroundSettings = {
    apiKeyId: null,
    mode: 'chat',
    chatModel: null,
    reasoningEffort: 'medium',
    imageModel: null,
  }
  const stored = read<Partial<PlaygroundSettings>>(SETTINGS_KEY, {})
  const rawReasoningEffort = stored.reasoningEffort as string | undefined
  const migratedReasoningEffort = rawReasoningEffort === 'light' ? 'low' : rawReasoningEffort
  const storedReasoningEffort = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'].includes(migratedReasoningEffort ?? '')
    ? migratedReasoningEffort as PlaygroundSettings['reasoningEffort']
    : defaults.reasoningEffort
  return { ...defaults, ...stored, reasoningEffort: storedReasoningEffort }
}

export function saveSettings(settings: PlaygroundSettings) {
  write(SETTINGS_KEY, settings)
}
