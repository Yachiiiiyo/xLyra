import { describe, expect, it } from 'vitest'
import type { AgentAvailableSite } from '@/features/agent/api/agent'
import {
  filterAgentScopePreviewRows,
  flattenAgentScopePreview,
  groupAgentScopePreview,
  listAgentScopePreviewModelNames,
  summarizeAgentScopePreview,
} from './agent-scope-preview'

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

describe('flattenAgentScopePreview', () => {
  it('sorts primarily by site name and secondarily by model name within the same site', () => {
    const groups = groupAgentScopePreview({
      sites: [
        site({
          site_id: 'helion',
          site_name: 'Helion-国模',
          models: [
            { ...model('h2', 'raw-kimi'), model_key: 'kimi-k3.1-pro' },
            { ...model('h1', 'deepseek-v4.1-flash') },
          ],
        }),
        site({
          site_id: 'ewo',
          site_name: 'ewo-xlyra',
          models: [
            { ...model('e3', 'glm-5.3') },
            { ...model('e1', 'kimi-raw'), model_key: 'kimi-k3.1-pro' },
            { ...model('e2', 'deepseek-v4.1-flash') },
          ],
        }),
      ],
      sitePolicy: 'allow_all',
      siteIds: [],
      modelPolicy: 'allow_all',
      siteModelIds: [],
    })
    const rows = flattenAgentScopePreview(groups)
    expect(rows.map((row) => `${row.site.site_name}:${row.modelName}`)).toEqual([
      'ewo-xlyra:deepseek-v4.1-flash',
      'ewo-xlyra:glm-5.3',
      'ewo-xlyra:kimi-k3.1-pro',
      'Helion-国模:deepseek-v4.1-flash',
      'Helion-国模:kimi-k3.1-pro',
    ])
  })

  it('sorts Alpha Site before Beta Site and models alphabetically inside each site', () => {
    const groups = groupAgentScopePreview({
      sites,
      sitePolicy: 'allow_all',
      siteIds: [],
      modelPolicy: 'allow_all',
      siteModelIds: [],
    })
    const rows = flattenAgentScopePreview(groups)
    expect(rows.map((row) => `${row.site.site_id}:${row.modelName}`)).toEqual([
      'a:gemini-pro',
      'b:claude-3',
      'b:gpt-4o',
    ])
  })

  it('keeps distinct rows and sorts deterministically when duplicate model_key exists under one site', () => {
    const rows = flattenAgentScopePreview([
      {
        site: site({ site_id: 's1', site_name: 'Same' }),
        models: [
          { ...model('m2', 'z-upstream'), model_key: 'shared-key' },
          { ...model('m1', 'a-upstream'), model_key: 'shared-key' },
          { ...model('m3', 'a-upstream'), model_key: 'shared-key' },
        ],
      },
    ])
    expect(rows.map((row) => row.model.id)).toEqual(['m1', 'm3', 'm2'])
  })

  it('groups identical site names by site_id before sorting by model name', () => {
    const rows = flattenAgentScopePreview([
      {
        site: site({ site_id: 'site-2', site_name: 'Mirror' }),
        models: [model('m-a', 'alpha-model')],
      },
      {
        site: site({ site_id: 'site-1', site_name: 'Mirror' }),
        models: [model('m-z', 'zeta-model')],
      },
    ])
    expect(rows.map((row) => `${row.site.site_id}:${row.modelName}`)).toEqual([
      'site-1:zeta-model',
      'site-2:alpha-model',
    ])
  })

  it('resolves displayName and realName using canonical display_name, custom site display_name, and fallbacks', () => {
    const canonicalById = new Map([
      ['can-official', { display_name: 'DeepSeek V4.1 Flash', model_key: 'deepseek-v4.1-flash' }],
      ['can-same', { display_name: 'kimi-k3.1-pro', model_key: 'kimi-k3.1-pro' }],
    ])
    const rows = flattenAgentScopePreview([
      {
        site: site({ site_id: 's1', site_name: 'Site 1' }),
        models: [
          {
            id: 'm1',
            upstream_model_name: 'deepseek-v4.1-flash-001',
            display_name: 'deepseek-v4.1-flash-001',
            canonical_model_id: 'can-official',
            model_key: 'deepseek-v4.1-flash',
          },
          {
            id: 'm2',
            upstream_model_name: 'kimi-k3.1-pro-raw',
            display_name: 'Kimi K3.1 Pro Custom',
            canonical_model_id: 'can-same',
            model_key: 'kimi-k3.1-pro',
          },
          {
            id: 'm3',
            upstream_model_name: 'glm-5.3',
            display_name: '   ',
            canonical_model_id: '',
          },
        ],
      },
    ], canonicalById)
    expect(rows.map((row) => ({
      id: row.model.id,
      modelName: row.modelName,
      displayName: row.displayName,
      realName: row.realName,
    }))).toEqual([
      {
        id: 'm1',
        modelName: 'deepseek-v4.1-flash',
        displayName: 'DeepSeek V4.1 Flash',
        realName: 'deepseek-v4.1-flash-001',
      },
      {
        id: 'm3',
        modelName: 'glm-5.3',
        displayName: 'glm-5.3',
        realName: 'glm-5.3',
      },
      {
        id: 'm2',
        modelName: 'kimi-k3.1-pro',
        displayName: 'Kimi K3.1 Pro Custom',
        realName: 'kimi-k3.1-pro-raw',
      },
    ])
  })
})

describe('listAgentScopePreviewModelNames', () => {
  it('deduplicates model names across and within sites and sorts them', () => {
    const rows = flattenAgentScopePreview([
      {
        site: site({ site_id: 's1', site_name: 'Site 1' }),
        models: [
          { ...model('m1', 'raw-1'), model_key: 'kimi-k3.1-pro' },
          { ...model('m2', 'raw-2'), model_key: 'kimi-k3.1-pro' },
          model('m3', 'deepseek-v4.1-flash'),
        ],
      },
      {
        site: site({ site_id: 's2', site_name: 'Site 2' }),
        models: [model('m4', 'deepseek-v4.1-flash'), model('m5', 'glm-5.3')],
      },
    ])
    expect(listAgentScopePreviewModelNames(rows)).toEqual([
      'deepseek-v4.1-flash',
      'glm-5.3',
      'kimi-k3.1-pro',
    ])
  })
})

describe('filterAgentScopePreviewRows', () => {
  const rows = flattenAgentScopePreview([
    {
      site: site({ site_id: 'alpha', site_name: 'Alpha Site', site_type: 'anthropic' }),
      models: [
        { id: 'a1', upstream_model_name: 'gemini-pro-upstream', display_name: 'Gemini Pro Display', canonical_model_id: '', model_key: 'gemini-pro' },
        model('a2', 'gpt-4o'),
      ],
    },
    {
      site: site({ site_id: 'beta', site_name: 'Beta Site', site_type: 'openai' }),
      models: [
        model('b1', 'gpt-4o'),
        model('b2', 'claude-3'),
      ],
    },
  ])

  it('returns rows untouched when both filters are blank', () => {
    expect(filterAgentScopePreviewRows(rows, { keyword: '  ', modelName: '  ' })).toBe(rows)
  })

  it('filters by exact modelName across sites', () => {
    const filtered = filterAgentScopePreviewRows(rows, { modelName: 'gpt-4o' })
    expect(filtered.map((row) => `${row.site.site_id}:${row.modelName}`)).toEqual([
      'alpha:gpt-4o',
      'beta:gpt-4o',
    ])
  })

  it('searches across site name, site type, display name, upstream name, and model_key', () => {
    expect(filterAgentScopePreviewRows(rows, { keyword: 'anthropic' }).map((row) => row.model.id)).toEqual(['a1', 'a2'])
    expect(filterAgentScopePreviewRows(rows, { keyword: 'Pro Display' }).map((row) => row.model.id)).toEqual(['a1'])
    expect(filterAgentScopePreviewRows(rows, { keyword: 'upstream' }).map((row) => row.model.id)).toEqual(['a1'])
    expect(filterAgentScopePreviewRows(rows, { keyword: 'BETA' }).map((row) => row.model.id)).toEqual(['b2', 'b1'])
    expect(filterAgentScopePreviewRows(rows, { keyword: 'gemini-pro' }).map((row) => row.model.id)).toEqual(['a1'])
  })

  it('intersects modelName and keyword filters and returns empty when unmatched', () => {
    const filtered = filterAgentScopePreviewRows(rows, { keyword: 'beta', modelName: 'gpt-4o' })
    expect(filtered.map((row) => `${row.site.site_id}:${row.modelName}`)).toEqual(['beta:gpt-4o'])
    expect(filterAgentScopePreviewRows(rows, { keyword: 'nonexistent' })).toEqual([])
  })

  it('matches canonical displayName resolved into row.displayName via keyword', () => {
    const canonicalRows = flattenAgentScopePreview([
      {
        site: site({ site_id: 's1', site_name: 'Site 1' }),
        models: [
          {
            id: 'm1',
            upstream_model_name: 'raw-ds',
            display_name: 'raw-ds',
            canonical_model_id: 'can-1',
            model_key: 'deepseek-v4.1-flash',
          },
        ],
      },
    ], new Map([['can-1', { display_name: 'DeepSeek Official Name', model_key: 'deepseek-v4.1-flash' }]]))
    expect(filterAgentScopePreviewRows(canonicalRows, { keyword: 'Official Name' }).map((row) => row.model.id)).toEqual(['m1'])
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
