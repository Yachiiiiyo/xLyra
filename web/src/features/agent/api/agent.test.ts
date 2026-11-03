import { afterEach, describe, expect, it, vi } from 'vitest'
import { dedupeImageModels, fetchAgentRuntimeSettings, updateAgentImageModels } from './agent'

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubFetch(data: unknown = {}) {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }))
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
