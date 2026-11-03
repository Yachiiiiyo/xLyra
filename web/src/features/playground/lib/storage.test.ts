import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isUUID } from '@/lib/id'
import { loadConversations, loadSettings, saveConversations, saveSettings } from '@/features/playground/lib/storage'

const values = new Map<string, string>()

beforeEach(() => {
  values.clear()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('loadSettings', () => {
  it('fills fields missing from older stored settings', () => {
    values.set('xlyra-playground-settings', JSON.stringify({ mode: 'image' }))

    expect(loadSettings()).toEqual({
      apiKeyId: null,
      mode: 'image',
      chatModel: null,
      reasoningEffort: 'medium',
      imageModel: null,
    })
  })

  it('replaces an invalid reasoning effort with the default', () => {
    values.set('xlyra-playground-settings', JSON.stringify({ reasoningEffort: 'undefined' }))

    expect(loadSettings().reasoningEffort).toBe('medium')
  })

  it('keeps max reasoning effort from stored settings', () => {
    values.set('xlyra-playground-settings', JSON.stringify({
      chatModel: 'gpt-5.6-sol',
      reasoningEffort: 'max',
    }))

    expect(loadSettings().reasoningEffort).toBe('max')
  })

  it('migrates the obsolete light reasoning effort to low', () => {
    values.set('xlyra-playground-settings', JSON.stringify({ reasoningEffort: 'light' }))

    expect(loadSettings().reasoningEffort).toBe('low')
  })

  it('keeps ultra reasoning effort from stored settings', () => {
    values.set('xlyra-playground-settings', JSON.stringify({
      chatModel: 'gpt-5.6-terra',
      reasoningEffort: 'ultra',
    }))

    expect(loadSettings().reasoningEffort).toBe('ultra')
  })

  it('preserves extended reasoning until model metadata is available', () => {
    values.set('xlyra-playground-settings', JSON.stringify({
      chatModel: 'gpt-5.5',
      reasoningEffort: 'ultra',
    }))

    expect(loadSettings().reasoningEffort).toBe('ultra')
  })

  it('persists the selected downstream key', () => {
    saveSettings({
      apiKeyId: 'key-2',
      mode: 'chat',
      chatModel: null,
      reasoningEffort: 'medium',
      imageModel: null,
    })

    expect(loadSettings().apiKeyId).toBe('key-2')
  })
})

describe('conversation storage', () => {
  it('migrates legacy conversation ids before they reach the server', () => {
    values.set('xlyra-playground-conversations', JSON.stringify([{
      id: 'id-legacy-123',
      title: 'Legacy',
      model: 'gpt-test',
      systemPrompt: '',
      messages: [],
      createdAt: 1,
      updatedAt: 1,
      serverPersisted: true,
      lastOrdinal: 4,
      activeRun: { id: 'run-1', status: 'running' },
    }]))

    const [conversation] = loadConversations()

    expect(isUUID(conversation.id)).toBe(true)
    expect(conversation.serverPersisted).toBe(false)
    expect(conversation.lastOrdinal).toBeUndefined()
    expect(conversation.activeRun).toBeUndefined()
    expect(conversation.title).toBe('Legacy')
    expect(JSON.parse(values.get('xlyra-playground-conversations') ?? '[]')[0].id).toBe(conversation.id)
  })

  it('keeps valid UUID conversations unchanged', () => {
    const id = '123e4567-e89b-42d3-a456-426614174000'
    values.set('xlyra-playground-conversations', JSON.stringify([{
      id,
      title: 'Valid',
      model: 'gpt-test',
      systemPrompt: '',
      messages: [],
      createdAt: 1,
      updatedAt: 1,
      serverPersisted: true,
      lastOrdinal: 4,
    }]))

    const [conversation] = loadConversations()

    expect(conversation.id).toBe(id)
    expect(conversation.serverPersisted).toBe(true)
    expect(conversation.lastOrdinal).toBe(4)
    expect(JSON.parse(values.get('xlyra-playground-conversations') ?? '[]')[0].id).toBe(id)
  })

  it('reassigns duplicated conversation ids to a fresh UUID', () => {
    const id = '123e4567-e89b-42d3-a456-426614174000'
    const build = (title: string) => ({
      id,
      title,
      model: 'gpt-test',
      systemPrompt: '',
      messages: [],
      createdAt: 1,
      updatedAt: 1,
      serverPersisted: true,
    })
    values.set('xlyra-playground-conversations', JSON.stringify([build('First'), build('Second')]))

    const [first, second] = loadConversations()

    expect(first.id).toBe(id)
    expect(first.serverPersisted).toBe(true)
    expect(second.id).not.toBe(id)
    expect(isUUID(second.id)).toBe(true)
    expect(second.serverPersisted).toBe(false)
    expect(second.title).toBe('Second')
  })

  it('keeps migrated ids stable across repeated loads', () => {
    values.set('xlyra-playground-conversations', JSON.stringify([{
      id: 'id-legacy-123',
      title: 'Legacy',
      model: 'gpt-test',
      systemPrompt: '',
      messages: [],
      createdAt: 1,
      updatedAt: 1,
    }]))

    const [first] = loadConversations()
    const [second] = loadConversations()

    expect(second.id).toBe(first.id)
  })

  it('persists attachment metadata without storing the binary payload', () => {
    saveConversations([{
      id: 'conversation-1',
      title: 'Files',
      model: 'gpt-test',
      systemPrompt: '',
      messages: [{
        id: 'message-1',
        role: 'user',
        content: 'summarize',
        attachments: [{
          id: 'attachment-1',
          name: 'report.pdf',
          mimeType: 'application/pdf',
          size: 4,
          dataURL: 'data:application/pdf;base64,cGRm',
        }],
        createdAt: 1,
      }],
      createdAt: 1,
      updatedAt: 1,
    }])

    expect(loadConversations()[0].messages[0].attachments).toEqual([{
      id: 'attachment-1',
      name: 'report.pdf',
      mimeType: 'application/pdf',
      size: 4,
    }])
  })

  it('persists the final response duration', () => {
    saveConversations([{
      id: 'conversation-1',
      title: 'Timing',
      model: 'gpt-test',
      systemPrompt: '',
      messages: [{
        id: 'message-1',
        role: 'assistant',
        content: 'done',
        responseDurationMs: 6_420,
        createdAt: 1,
      }],
      createdAt: 1,
      updatedAt: 1,
    }])

    expect(loadConversations()[0].messages[0].responseDurationMs).toBe(6_420)
  })
})
