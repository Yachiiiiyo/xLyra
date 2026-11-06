import { describe, expect, it } from 'vitest'
import type { AgentScopePreviewGroup } from '@/features/agent/lib/agent-scope-preview'
import { agentFastModelCandidates, resolveFastModel, type AgentFastModelCandidate } from './agent-fast-model'

type PreviewModel = AgentScopePreviewGroup['models'][number]

function model(partial: Partial<PreviewModel> & { id: string }): PreviewModel {
  return { upstream_model_name: partial.id, display_name: '', canonical_model_id: '', ...partial }
}

function group(siteName: string, models: PreviewModel[]): AgentScopePreviewGroup {
  const site = { site_id: siteName, site_name: siteName, site_type: 'openai', enabled: true, models }
  return { site, models }
}

describe('agentFastModelCandidates', () => {
  it('排除非聊天模型，使用模型 key 并丢弃空 id', () => {
    const candidates = agentFastModelCandidates([
      group('Alpha', [
        model({ id: 'chat', model_key: ' gpt-fast ', upstream_model_name: 'upstream', display_name: 'GPT Fast' }),
        model({ id: 'image', category: 'image' }),
        model({ id: 'audio', category: 'audio' }),
        model({ id: 'embedding', category: 'embedding' }),
        model({ id: 'empty', model_key: ' ', upstream_model_name: 'ignored' }),
      ]),
    ])
    expect(candidates).toEqual([{ id: 'gpt-fast', name: 'GPT Fast', siteNames: ['Alpha'] }])
  })

  it('同 id 跨站点合并并去重站点名，display_name 缺省时回退 id', () => {
    const candidates = agentFastModelCandidates([
      group('Alpha', [model({ id: 'one', model_key: 'shared', display_name: 'Shared' })]),
      group('Beta', [model({ id: 'two', model_key: 'shared', display_name: 'Another Name' })]),
      group('Alpha', [model({ id: 'three', upstream_model_name: 'fallback' })]),
    ])
    expect(candidates).toEqual([
      { id: 'fallback', name: 'fallback', siteNames: ['Alpha'] },
      { id: 'shared', name: 'Shared', siteNames: ['Alpha', 'Beta'] },
    ])
  })

  it('按显示名做不区分大小写的排序', () => {
    const candidates = agentFastModelCandidates([
      group('Alpha', [
        model({ id: 'c', display_name: 'charlie' }),
        model({ id: 'a', display_name: 'Alpha' }),
        model({ id: 'b', display_name: 'beta' }),
      ]),
    ])
    expect(candidates.map((candidate) => candidate.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('resolveFastModel', () => {
  const candidates: AgentFastModelCandidate[] = [{ id: 'gpt-fast', name: 'GPT Fast', siteNames: ['Alpha'] }]

  it('解析有效配置并允许配置值两侧有空白', () => {
    expect(resolveFastModel(' gpt-fast ', candidates)).toEqual({ model: candidates[0] })
  })

  it.each(['', '   ', 'removed-model'])('空配置或失效配置 %j 按未配置处理', (configured) => {
    expect(resolveFastModel(configured, candidates)).toEqual({ model: null })
  })
})
