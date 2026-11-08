import { describe, expect, it } from 'vitest'

import type { BackupSectionInfo } from '@/features/settings/api/settings'
import {
  deselectSection,
  formatSectionSize,
  groupSections,
  lockedSections,
  missingRecommends,
  normalizeSelection,
  orderSections,
  requiredByMap,
  selectSection,
} from '@/features/settings/lib/backup-sections'

function section(overrides: Partial<BackupSectionInfo> & { id: string }): BackupSectionInfo {
  return {
    group: 'system',
    requires: [],
    recommends: [],
    default_manual: false,
    default_auto: false,
    available: true,
    ...overrides,
  }
}

const all: BackupSectionInfo[] = [
  section({ id: 'settings' }),
  section({ id: 'gateway_sites', group: 'gateway' }),
  section({ id: 'gateway_keys', group: 'gateway', requires: ['gateway_sites'] }),
  section({ id: 'gateway_logs', group: 'gateway', requires: ['gateway_sites', 'gateway_keys'] }),
  section({ id: 'plugins' }),
  section({ id: 'agent_config', group: 'agent', recommends: ['agent_capabilities'] }),
  section({ id: 'agent_capabilities', group: 'agent' }),
  section({ id: 'agent_sessions', group: 'agent', recommends: ['agent_workspace'] }),
  section({ id: 'agent_workspace', group: 'agent' }),
]

describe('orderSections', () => {
  it('orders and dedupes by registration order and drops unknown ids', () => {
    expect(orderSections(all, ['gateway_keys', 'settings', 'gateway_keys', 'nope'])).toEqual(['settings', 'gateway_keys'])
  })
})

describe('requiredByMap', () => {
  it('maps each selected section to the selected sections that require it', () => {
    const map = requiredByMap(all, ['gateway_sites', 'gateway_keys', 'gateway_logs'])
    expect(map.get('gateway_sites')).toEqual(['gateway_keys', 'gateway_logs'])
    expect(map.get('gateway_keys')).toEqual(['gateway_logs'])
    expect(map.get('gateway_logs')).toBeUndefined()
    expect(map.get('settings')).toBeUndefined()
  })
})

describe('lockedSections', () => {
  it('locks sections required by other selected sections', () => {
    const locked = lockedSections(all, ['gateway_sites', 'gateway_keys'])
    expect(locked.has('gateway_sites')).toBe(true)
    expect(locked.has('gateway_keys')).toBe(false)
  })

  it('locks transitively required sections', () => {
    const locked = lockedSections(all, ['gateway_sites', 'gateway_keys', 'gateway_logs'])
    expect(locked.has('gateway_sites')).toBe(true)
    expect(locked.has('gateway_keys')).toBe(true)
    expect(locked.has('gateway_logs')).toBe(false)
  })
})

describe('selectSection', () => {
  it('selects a section and transitively pulls in its requirements', () => {
    expect(selectSection(all, ['settings'], 'gateway_logs')).toEqual(['settings', 'gateway_sites', 'gateway_keys', 'gateway_logs'])
  })

  it('returns the selection unchanged for unavailable sections', () => {
    const withUnavailable = [
      ...all.filter((item) => item.id !== 'agent_workspace'),
      section({ id: 'agent_workspace', group: 'agent', available: false, unavailable_reason: 'workspace missing' }),
    ]
    expect(selectSection(withUnavailable, ['settings'], 'agent_workspace')).toEqual(['settings'])
  })
})

describe('deselectSection', () => {
  it('keeps a locked section selected', () => {
    expect(deselectSection(all, ['gateway_sites', 'gateway_keys'], 'gateway_sites')).toEqual(['gateway_sites', 'gateway_keys'])
  })

  it('allows deselecting after its dependents are removed', () => {
    const withoutDependent = deselectSection(all, ['gateway_sites', 'gateway_keys'], 'gateway_keys')
    expect(withoutDependent).toEqual(['gateway_sites'])
    expect(deselectSection(all, withoutDependent, 'gateway_sites')).toEqual([])
  })
})

describe('normalizeSelection', () => {
  it('drops unknown and unavailable ids, completes requirements and orders the result', () => {
    const withUnavailable = [
      ...all.filter((item) => item.id !== 'plugins'),
      section({ id: 'plugins', available: false }),
    ]
    expect(normalizeSelection(withUnavailable, ['plugins', 'gateway_logs', 'nope'])).toEqual(['gateway_sites', 'gateway_keys', 'gateway_logs'])
  })
})

describe('missingRecommends', () => {
  it('reports unselected soft dependencies of selected sections', () => {
    expect(missingRecommends(all, ['agent_config', 'agent_sessions'])).toEqual([
      { id: 'agent_config', recommends: ['agent_capabilities'] },
      { id: 'agent_sessions', recommends: ['agent_workspace'] },
    ])
    expect(missingRecommends(all, ['agent_config', 'agent_capabilities'])).toEqual([])
  })
})

describe('groupSections', () => {
  it('groups in system → gateway → agent order while preserving item order', () => {
    const mixed = [
      all[1], // gateway_sites
      all[0], // settings
      all[5], // agent_config
      all[2], // gateway_keys
      all[4], // plugins
      all[8], // agent_workspace
    ]
    const groups = groupSections(mixed)
    expect(groups.map((group) => group.group)).toEqual(['system', 'gateway', 'agent'])
    expect(groups[0].items.map((item) => item.id)).toEqual(['settings', 'plugins'])
    expect(groups[1].items.map((item) => item.id)).toEqual(['gateway_sites', 'gateway_keys'])
    expect(groups[2].items.map((item) => item.id)).toEqual(['agent_config', 'agent_workspace'])
  })
})

describe('formatSectionSize', () => {
  it('formats size bytes', () => {
    expect(formatSectionSize(section({ id: 'settings', size_bytes: 1536 }), 'en')).toBe('1.5 KB')
  })

  it('prefixes truncated sizes', () => {
    expect(formatSectionSize(section({ id: 'settings', size_bytes: 2048, size_truncated: true }), 'en')).toBe('≥ 2 KB')
  })

  it('formats row counts without size bytes', () => {
    expect(formatSectionSize(section({ id: 'settings', rows: 1234 }), 'en')).toBe('1,234')
  })

  it('returns null when neither size nor rows are present', () => {
    expect(formatSectionSize(section({ id: 'settings' }), 'en')).toBeNull()
  })
})
