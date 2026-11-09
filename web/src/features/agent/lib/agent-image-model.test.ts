import { describe, expect, it } from 'vitest'
import type { CanonicalModel, Site, SiteModel } from '@/features/sites/api/sites'
import type { AgentAvailableSite, AgentImageModelSelection } from '@/features/agent/api/agent'
import {
  agentImageModelGroups,
  agentImageModelKey,
  agentImageModelsSummary,
  isAgentImageModel,
  isImageModelSiteSelected,
  moveImageModel,
  removeImageModel,
  toggleImageModelSite,
} from './agent-image-model'

function model(capabilities: Record<string, unknown>, overrides: Partial<SiteModel> = {}): SiteModel {
  return {
    id: 'site-model-1',
    site_id: 'site-1',
    upstream_model_name: 'image-model',
    display_name: 'Image model',
    canonical_model_id: 'canonical-1',
    capabilities,
    status: 'active',
    created_at: '',
    updated_at: '',
    ...overrides,
  }
}

function site(id: string, name: string, enabled = true): Site {
  return { id, name, enabled, site_type: 'openai' } as Site
}

function canonical(capabilities: Record<string, unknown>): CanonicalModel {
  return {
    id: 'canonical-1', model_key: 'image-model', display_name: 'Image model', provider: 'openai',
    category: 'image', capabilities, status: 'active', created_at: '', updated_at: '',
  }
}

describe('isAgentImageModel', () => {
  it('recognizes direct and raw image endpoint declarations', () => {
    expect(isAgentImageModel(model({ supported_endpoint_types: ['openai', 'openai-image'] }))).toBe(true)
    expect(isAgentImageModel(model({ raw: { supported_endpoint_types: ['images'] } }))).toBe(true)
    expect(isAgentImageModel(model({ supported_endpoint_types: ['openai'] }))).toBe(false)
  })

  it('recognizes canonical image output modalities', () => {
    expect(isAgentImageModel(model({}), canonical({ output_modalities: ['text', 'image'] }))).toBe(true)
    expect(isAgentImageModel(model({ modality: 'image' }))).toBe(true)
    expect(isAgentImageModel(model({ output: { modalities: ['image'] } }))).toBe(true)
  })
})

describe('agentImageModelGroups', () => {
  it('groups image models by enabled site and skips disabled sites', () => {
    const groups = agentImageModelGroups(
      [site('site-1', 'Alpha'), site('site-2', 'Beta'), site('site-3', 'Off', false)],
      new Map([
        ['site-1', [model({ supported_endpoint_types: ['openai-image'] })]],
        ['site-2', [model({ supported_endpoint_types: ['openai'] }, { id: 'text-model', site_id: 'site-2' })]],
        ['site-3', [model({ output_modalities: ['image'] }, { id: 'off-model', site_id: 'site-3' })]],
      ]),
      new Map(),
    )
    expect(groups.map((group) => ({ site: group.site.id, models: group.models.map((item) => item.id) }))).toEqual([
      { site: 'site-1', models: ['site-model-1'] },
    ])
  })

  it('keeps image models regardless of the agent allow-list scope', () => {
    const modelsBySite = new Map([['site-2', [model({ output_modalities: ['image'] }, { id: 'out-of-scope', site_id: 'site-2' })]]])
    const groups = agentImageModelGroups([site('site-2', 'Beta')], modelsBySite, new Map())
    expect(groups.flatMap((group) => group.models.map((item) => item.id))).toEqual(['out-of-scope'])
  })
})

describe('agentImageModelKey', () => {
  it('prefers the canonical model key and falls back to the upstream model name', () => {
    const canonicalById = new Map([['canonical-1', canonical({})]])
    expect(agentImageModelKey(model({}), canonicalById)).toBe('image-model')
    expect(agentImageModelKey(model({}, { canonical_model_id: 'missing', upstream_model_name: 'upstream-image' }), canonicalById)).toBe('upstream-image')
    expect(agentImageModelKey(model({}, { canonical_model_id: '', upstream_model_name: 'uncategorized-image' }), canonicalById)).toBe('uncategorized-image')
  })
})

describe('isImageModelSiteSelected', () => {
  it('checks the requested site independently and treats an empty site list as unrestricted', () => {
    const selected: AgentImageModelSelection[] = [
      { model: 'shared', site_ids: ['alpha'] },
      { model: 'legacy', site_ids: [] },
    ]
    expect(isImageModelSiteSelected(selected, 'shared', 'alpha')).toBe(true)
    expect(isImageModelSiteSelected(selected, 'shared', 'beta')).toBe(false)
    expect(isImageModelSiteSelected(selected, 'legacy', 'beta')).toBe(true)
    expect(isImageModelSiteSelected(selected, 'missing', 'alpha')).toBe(false)
  })
})

describe('toggleImageModelSite', () => {
  it('keeps same-name selections independent per site and removes the entry after its last site is cleared', () => {
    let selected = toggleImageModelSite([], 'gpt-image-2', 'alpha', ['alpha', 'beta'])
    expect(selected).toEqual([{ model: 'gpt-image-2', site_ids: ['alpha'] }])
    expect(isImageModelSiteSelected(selected, 'gpt-image-2', 'alpha')).toBe(true)
    expect(isImageModelSiteSelected(selected, 'gpt-image-2', 'beta')).toBe(false)

    selected = toggleImageModelSite(selected, 'gpt-image-2', 'beta', ['alpha', 'beta'])
    expect(selected).toEqual([{ model: 'gpt-image-2', site_ids: ['alpha', 'beta'] }])
    selected = toggleImageModelSite(selected, 'gpt-image-2', 'alpha', ['alpha', 'beta'])
    expect(selected).toEqual([{ model: 'gpt-image-2', site_ids: ['beta'] }])
    selected = toggleImageModelSite(selected, 'gpt-image-2', 'beta', ['alpha', 'beta'])
    expect(selected).toEqual([])
  })

  it('appends a new model at the end without disturbing existing order', () => {
    const selected: AgentImageModelSelection[] = [{ model: 'first', site_ids: ['alpha'] }]
    expect(toggleImageModelSite(selected, 'second', 'beta', ['alpha', 'beta'])).toEqual([
      { model: 'first', site_ids: ['alpha'] },
      { model: 'second', site_ids: ['beta'] },
    ])
  })

  it('expands an unrestricted entry to provided sites before toggling one site', () => {
    const selected: AgentImageModelSelection[] = [{ model: 'shared', site_ids: [] }]
    expect(toggleImageModelSite(selected, 'shared', 'alpha', ['alpha', 'beta'])).toEqual([
      { model: 'shared', site_ids: ['beta'] },
    ])
    expect(toggleImageModelSite(selected, 'shared', 'gamma', ['alpha', 'beta'])).toEqual([
      { model: 'shared', site_ids: ['alpha', 'beta', 'gamma'] },
    ])
  })
})

describe('removeImageModel', () => {
  it('removes the whole model entry and leaves other model entries untouched', () => {
    expect(removeImageModel([
      { model: 'shared', site_ids: ['alpha', 'beta'] },
      { model: 'other', site_ids: ['beta'] },
    ], 'shared')).toEqual([{ model: 'other', site_ids: ['beta'] }])
  })
})

describe('moveImageModel', () => {
  const selected: AgentImageModelSelection[] = [
    { model: 'a', site_ids: ['alpha'] },
    { model: 'b', site_ids: [] },
    { model: 'c', site_ids: ['beta'] },
  ]

  it('moves a selected model entry up and down with its sites', () => {
    expect(moveImageModel(selected, 'c', -1)).toEqual([selected[0], selected[2], selected[1]])
    expect(moveImageModel(selected, 'a', 1)).toEqual([selected[1], selected[0], selected[2]])
  })

  it('keeps the list unchanged at either boundary', () => {
    expect(moveImageModel(selected, 'a', -1)).toBe(selected)
    expect(moveImageModel(selected, 'c', 1)).toBe(selected)
  })

  it('keeps the list unchanged for an unknown key', () => {
    expect(moveImageModel(selected, 'missing', 1)).toBe(selected)
  })
})

describe('agentImageModelsSummary', () => {
  function availableSite(id: string, name: string, enabled: boolean, models: AgentAvailableSite['models']): AgentAvailableSite {
    return { site_id: id, site_name: name, site_type: 'openai', enabled, models }
  }
  const imageModel = (key: string, name = key): AgentAvailableSite['models'][number] => ({
    id: `model-${key}`, upstream_model_name: key, display_name: name, canonical_model_id: '', model_key: key,
  })

  it('preserves model order and reports selected enabled site names in provider order', () => {
    const sites = [
      availableSite('alpha', 'Alpha', true, [imageModel('shared', 'Shared')]),
      availableSite('beta', 'Beta', true, [imageModel('shared', 'Other name'), imageModel('other', 'Other')]),
    ]
    expect(agentImageModelsSummary([
      { model: 'other', site_ids: ['beta'] },
      { model: 'shared', site_ids: ['beta', 'alpha'] },
    ], sites)).toEqual([
      { key: 'other', name: 'Other', siteNames: ['Beta'], available: true },
      { key: 'shared', name: 'Shared', siteNames: ['Alpha', 'Beta'], available: true },
    ])
  })

  it('uses null siteNames for an unrestricted selection', () => {
    const sites = [availableSite('alpha', 'Alpha', true, [imageModel('shared', 'Shared')])]
    expect(agentImageModelsSummary([{ model: 'shared', site_ids: [] }], sites)).toEqual([
      { key: 'shared', name: 'Shared', siteNames: null, available: true },
    ])
  })

  it('marks a model unavailable when its selected provider is disabled or no provider remains', () => {
    const disabled = [availableSite('alpha', 'Alpha', false, [imageModel('shared')])]
    expect(agentImageModelsSummary([{ model: 'shared', site_ids: ['alpha'] }], disabled)).toEqual([
      { key: 'shared', name: 'shared', siteNames: [], available: false },
    ])
    expect(agentImageModelsSummary([{ model: 'missing', site_ids: [] }], disabled)).toEqual([
      { key: 'missing', name: 'missing', siteNames: null, available: false },
    ])
  })
})
