import { describe, expect, it } from 'vitest'
import type { AgentAvailableSite } from '@/features/agent/api/agent'
import { filterAgentScopePreview, groupAgentScopePreview, summarizeAgentScopePreview } from './agent-scope-preview'

function site(partial: Partial<AgentAvailableSite> & { site_id: string; site_name: string }): AgentAvailableSite {
  return { site_type: 'openai', enabled: true, models: [], ...partial }
}

function model(id: string, name = id) {
  return { id, upstream_model_name: name, display_name: name, canonical_model_id: '' }
}

const sites: AgentAvailableSite[] = [
  site({
    site_id: 'b',
    site_name: 'Beta Site',
    models: [model('b1', 'gpt-4o'), model('b2', 'claude-3')],
  }),
  site({
    site_id: 'a',
    site_name: 'Alpha Site',
    models: [model('a1', 'gemini-pro')],
  }),
  site({ site_id: 'off', site_name: 'Disabled', enabled: false, models: [model('off1')] }),
  site({ site_id: 'empty', site_name: 'Empty', models: [] }),
]

describe('groupAgentScopePreview', () => {
  it('allow_all keeps enabled sites with all models, sorted by name', () => {
    const groups = groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_all',
      siteIds: [],
      modelPolicy: 'allow_all',
      siteModelIds: [],
    })
    expect(groups.map((group) => group.site.site_id)).toEqual(['a', 'b'])
    expect(groups[0]?.models.map((item) => item.id)).toEqual(['a1'])
  })

  it('allow_list filters sites and models; sites without remaining models are dropped', () => {
    const groups = groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_list',
      siteIds: ['b'],
      modelPolicy: 'allow_list',
      siteModelIds: ['b2'],
    })
    expect(groups).toHaveLength(1)
    expect(groups[0]?.site.site_id).toBe('b')
    expect(groups[0]?.models.map((item) => item.id)).toEqual(['b2'])

    expect(groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_list',
      siteIds: ['a'],
      modelPolicy: 'allow_list',
      siteModelIds: ['b2'],
    })).toEqual([])
  })

  it('allow_all sites with allow_list models still filters models', () => {
    const groups = groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_all',
      siteIds: [],
      modelPolicy: 'allow_list',
      siteModelIds: ['a1'],
    })
    expect(groups).toHaveLength(1)
    expect(groups[0]?.site.site_id).toBe('a')
  })
})

describe('filterAgentScopePreview', () => {
  const groups = groupAgentScopePreview({
    sites,
    sitePolicy: 'allow_all',
    siteIds: [],
    modelPolicy: 'allow_all',
    siteModelIds: [],
  })

  it('returns groups untouched for a blank keyword', () => {
    expect(filterAgentScopePreview(groups, '  ')).toBe(groups)
  })

  it('a site-name hit keeps every model of that site', () => {
    const filtered = filterAgentScopePreview(groups, 'alpha')
    expect(filtered).toHaveLength(1)
    expect(filtered[0]?.site.site_id).toBe('a')
    expect(filtered[0]?.models).toHaveLength(1)
  })

  it('a model-name hit keeps only matching models and drops empty groups', () => {
    const filtered = filterAgentScopePreview(groups, 'GPT')
    expect(filtered).toHaveLength(1)
    expect(filtered[0]?.site.site_id).toBe('b')
    expect(filtered[0]?.models.map((item) => item.id)).toEqual(['b1'])
    expect(filterAgentScopePreview(groups, 'nonexistent')).toEqual([])
  })
})

describe('summarizeAgentScopePreview', () => {
  it('counts sites and total models', () => {
    const groups = groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_all',
      siteIds: [],
      modelPolicy: 'allow_all',
      siteModelIds: [],
    })
    expect(summarizeAgentScopePreview(groups)).toEqual({ sites: 2, models: 3 })
    expect(summarizeAgentScopePreview([])).toEqual({ sites: 0, models: 0 })
  })
})
