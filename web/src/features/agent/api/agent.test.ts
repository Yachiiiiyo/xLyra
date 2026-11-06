import { afterEach, describe, expect, it, vi } from 'vitest'
import { dedupeImageModels, fetchAgentRuntimeSettings, fetchAgentSuggestion, updateAgentClassifierModel, updateAgentFastModel, updateAgentImageModels, updateAgentPromptSuggestion } from './agent'

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubFetch(data: unknown = {}) {
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('fetchAgentRuntimeSettings image_models compatibility', () => {
  it('converts legacy string elements to unrestricted model selections', async () => {
    stubFetch({ image_models: ['new-image', 'second-image'] })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ image_models: [
      { model: 'new-image', site_ids: [] },
      { model: 'second-image', site_ids: [] },
    ] })
  })

  it('preserves object selections and prefers them over the legacy single value', async () => {
    stubFetch({
      image_models: [{ model: 'new-image', site_ids: ['alpha', 'beta'] }],
      image_model: { site_id: 'site-1', model: 'legacy-image' },
    })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ image_models: [
      { model: 'new-image', site_ids: ['alpha', 'beta'] },
    ] })
  })

  it('uses only the model from the legacy single image_model when image_models is absent', async () => {
    stubFetch({ image_model: { site_id: 'site-1', model: 'legacy-image' } })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ image_models: [
      { model: 'legacy-image', site_ids: [] },
    ] })
  })

  it('defaults to an empty list when neither field is returned', async () => {
    stubFetch({})
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ image_models: [] })
  })

  it('drops invalid elements and non-string site ids', async () => {
    stubFetch({ image_models: [
      null,
      42,
      { site_ids: ['missing-model'] },
      { model: 42, site_ids: ['invalid-model'] },
      { model: 'valid', site_ids: ['alpha', 42, null, 'beta'] },
    ] })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ image_models: [
      { model: 'valid', site_ids: ['alpha', 'beta'] },
    ] })
  })
})

describe('prompt suggestion settings', () => {
  it('reads the setting and defaults an absent value to enabled', async () => {
    stubFetch({ prompt_suggestion_enabled: false })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ prompt_suggestion_enabled: false })
    stubFetch({})
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ prompt_suggestion_enabled: true })
  })

  it('updates only prompt_suggestion_enabled', async () => {
    const fetchMock = stubFetch()
    await updateAgentPromptSuggestion(false)
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/agent/settings')
    expect(request.method).toBe('PUT')
    expect(JSON.parse(request.body as string)).toEqual({ prompt_suggestion_enabled: false })
  })
})

describe('fast model settings', () => {
  it('reads fast_model and defaults absent or non-string values to empty', async () => {
    stubFetch({ fast_model: 'gpt-fast' })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ fast_model: 'gpt-fast' })
    for (const data of [{}, { fast_model: null }, { fast_model: 42 }]) {
      stubFetch(data)
      await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ fast_model: '' })
    }
  })

  it('updates only the trimmed fast_model, including an empty clearing value', async () => {
    const fetchMock = stubFetch()
    await updateAgentFastModel('  gpt-fast  ')
    await updateAgentFastModel('   ')
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/agent/settings')
    expect(request.method).toBe('PUT')
    expect(JSON.parse(request.body as string)).toEqual({ fast_model: 'gpt-fast' })
    const [, clearRequest] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(JSON.parse(clearRequest.body as string)).toEqual({ fast_model: '' })
  })
})

describe('classifier model settings', () => {
  it('reads classifier_model and defaults absent or non-string values to empty', async () => {
    stubFetch({ classifier_model: 'gpt-classifier' })
    await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ classifier_model: 'gpt-classifier' })
    for (const data of [{}, { classifier_model: null }, { classifier_model: 42 }]) {
      stubFetch(data)
      await expect(fetchAgentRuntimeSettings()).resolves.toMatchObject({ classifier_model: '' })
    }
  })

  it('updates only the trimmed classifier_model, including an empty clearing value', async () => {
    const fetchMock = stubFetch()
    await updateAgentClassifierModel('  gpt-classifier  ')
    await updateAgentClassifierModel('   ')
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/agent/settings')
    expect(request.method).toBe('PUT')
    expect(JSON.parse(request.body as string)).toEqual({ classifier_model: 'gpt-classifier' })
    const [, clearRequest] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(JSON.parse(clearRequest.body as string)).toEqual({ classifier_model: '' })
  })
})

describe('fetchAgentSuggestion', () => {
  it('posts without a body and returns trimmed suggestion text', async () => {
    const fetchMock = stubFetch({ suggestion: '  下一条消息  ', request_id: 'request-1' })
    const controller = new AbortController()
    await expect(fetchAgentSuggestion('session/1', controller.signal)).resolves.toBe('下一条消息')
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/agent/sessions/session%2F1/suggestion')
    expect(request.method).toBe('POST')
    expect(request.body).toBeUndefined()
    expect(request.signal).toBe(controller.signal)
  })

  it.each([null, '', '   '])('treats %j as no suggestion', async (suggestion) => {
    stubFetch({ suggestion })
    await expect(fetchAgentSuggestion('session-1')).resolves.toBeNull()
  })

  it('silently returns null for non-2xx responses and network failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('failed', { status: 500 })))
    await expect(fetchAgentSuggestion('session-1')).resolves.toBeNull()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await expect(fetchAgentSuggestion('session-1')).resolves.toBeNull()
  })

  it('silently returns null when aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new DOMException('aborted', 'AbortError')))
    await expect(fetchAgentSuggestion('session-1', controller.signal)).resolves.toBeNull()
  })
})

describe('dedupeImageModels', () => {
  it('merges duplicate model sites as an ordered union without changing model order', () => {
    expect(dedupeImageModels([
      { model: 'first', site_ids: ['alpha'] },
      { model: 'second', site_ids: ['beta'] },
      { model: 'first', site_ids: ['beta', 'alpha', 'beta'] },
    ])).toEqual([
      { model: 'first', site_ids: ['alpha', 'beta'] },
      { model: 'second', site_ids: ['beta'] },
    ])
  })

  it('lets an unrestricted duplicate absorb all site restrictions', () => {
    expect(dedupeImageModels([
      { model: 'shared', site_ids: ['alpha'] },
      { model: 'shared', site_ids: [] },
      { model: 'shared', site_ids: ['beta'] },
    ])).toEqual([{ model: 'shared', site_ids: [] }])
    expect(dedupeImageModels([
      { model: 'shared', site_ids: [] },
      { model: 'shared', site_ids: ['alpha'] },
    ])).toEqual([{ model: 'shared', site_ids: [] }])
  })

  it('trims names and site ids and discards empty values', () => {
    expect(dedupeImageModels([
      { model: ' ', site_ids: ['alpha'] },
      { model: ' first ', site_ids: [' alpha ', '', 'alpha'] },
    ])).toEqual([{ model: 'first', site_ids: ['alpha'] }])
  })
})

describe('updateAgentImageModels', () => {
  it('sends only the ordered de-duplicated image_models object array', async () => {
    const fetchMock = stubFetch()
    await updateAgentImageModels([
      { model: ' first ', site_ids: ['alpha'] },
      { model: 'second', site_ids: ['beta'] },
      { model: 'first', site_ids: ['beta', 'alpha'] },
      { model: '', site_ids: [] },
    ])
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/agent/settings')
    expect(request.method).toBe('PUT')
    expect(JSON.parse(request.body as string)).toEqual({ image_models: [
      { model: 'first', site_ids: ['alpha', 'beta'] },
      { model: 'second', site_ids: ['beta'] },
    ] })
  })

  it('sends an empty image_models list to clear the selection', async () => {
    const fetchMock = stubFetch()
    await updateAgentImageModels([])
    const [, request] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(request.body as string)).toEqual({ image_models: [] })
  })
})
