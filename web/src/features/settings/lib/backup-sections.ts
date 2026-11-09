import type { BackupSectionInfo } from '@/features/settings/api/settings'

const GROUP_ORDER: Record<string, number> = { system: 0, gateway: 1, agent: 2 }

export function orderSections(all: BackupSectionInfo[], ids: string[]): string[] {
  const known = new Set(all.map((section) => section.id))
  return all.map((section) => section.id).filter((id) => known.has(id) && ids.includes(id))
}

export function requiredByMap(all: BackupSectionInfo[], selected: string[]): Map<string, string[]> {
  const selectedSet = new Set(selected)
  const result = new Map<string, string[]>()
  for (const section of all) {
    if (!selectedSet.has(section.id)) continue
    for (const required of section.requires) {
      if (!selectedSet.has(required)) continue
      const dependents = result.get(required) ?? []
      if (!dependents.includes(section.id)) dependents.push(section.id)
      result.set(required, dependents)
    }
  }
  return result
}

export function lockedSections(all: BackupSectionInfo[], selected: string[]): Set<string> {
  const byId = new Map(all.map((section) => [section.id, section]))
  const selectedSet = new Set(selected)
  const locked = new Set<string>()
  for (const id of selected) {
    const queue = [...(byId.get(id)?.requires ?? [])]
    const seen = new Set<string>()
    while (queue.length > 0) {
      const current = queue.pop()!
      if (seen.has(current)) continue
      seen.add(current)
      if (!selectedSet.has(current)) continue
      if (current !== id) locked.add(current)
      queue.push(...(byId.get(current)?.requires ?? []))
    }
  }
  return locked
}

export function selectSection(all: BackupSectionInfo[], selected: string[], id: string): string[] {
  const info = all.find((section) => section.id === id)
  if (!info || !info.available) return orderSections(all, selected)
  const next = new Set(selected)
  const queue = [id]
  while (queue.length > 0) {
    const current = queue.pop()!
    if (next.has(current)) continue
    next.add(current)
    const currentInfo = all.find((section) => section.id === current)
    if (currentInfo) queue.push(...currentInfo.requires)
  }
  return orderSections(all, [...next])
}

export function deselectSection(all: BackupSectionInfo[], selected: string[], id: string): string[] {
  const info = all.find((section) => section.id === id)
  if (!info) return orderSections(all, selected)
  if (lockedSections(all, selected).has(id)) return orderSections(all, selected)
  return orderSections(all, selected.filter((item) => item !== id))
}

export function normalizeSelection(all: BackupSectionInfo[], ids: string[]): string[] {
  let next = ids
  let changed = true
  const available = new Set(all.filter((section) => section.available).map((section) => section.id))
  while (changed) {
    changed = false
    for (const id of next) {
      if (!available.has(id)) continue
      const info = all.find((section) => section.id === id)!
      for (const required of info.requires) {
        if (!available.has(required)) continue
        if (!next.includes(required)) {
          next = [...next, required]
          changed = true
        }
      }
    }
  }
  return orderSections(all, next.filter((id) => available.has(id)))
}

export function missingRecommends(all: BackupSectionInfo[], selected: string[]): { id: string; recommends: string[] }[] {
  const selectedSet = new Set(selected)
  const result: { id: string; recommends: string[] }[] = []
  for (const section of all) {
    if (!selectedSet.has(section.id)) continue
    const missing = section.recommends.filter((recommended) => !selectedSet.has(recommended))
    if (missing.length > 0) result.push({ id: section.id, recommends: missing })
  }
  return result
}

export function groupSections(all: BackupSectionInfo[]): { group: string; items: BackupSectionInfo[] }[] {
  const result: { group: string; items: BackupSectionInfo[] }[] = []
  for (const section of all) {
    const entry = result.find((item) => item.group === section.group)
    if (entry) entry.items.push(section)
    else result.push({ group: section.group, items: [section] })
  }
  result.sort((a, b) => (GROUP_ORDER[a.group] ?? 0) - (GROUP_ORDER[b.group] ?? 0))
  return result
}

export function formatSectionSize(info: BackupSectionInfo, locale: string): string | null {
  if (info.size_bytes !== undefined && info.size_bytes !== null) {
    const text = formatBytes(info.size_bytes, locale)
    return info.size_truncated ? `≥ ${text}` : text
  }
  if (info.rows !== undefined && info.rows !== null) {
    return new Intl.NumberFormat(locale).format(info.rows)
  }
  return null
}

function formatBytes(value: number, locale: string) {
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let size = value / 1024
  let unit = units[0]
  for (let index = 1; index < units.length && size >= 1024; index += 1) {
    size /= 1024
    unit = units[index]
  }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(size)} ${unit}`
}
