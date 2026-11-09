import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FilePenLine, FilePlus2, FileX2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/features/agent/lib/format-bytes'
import type { AgentFileChange } from '@/features/agent/lib/run-changes'
import { cn } from '@/lib/utils'

const STATUS_ICONS = { added: FilePlus2, modified: FilePenLine, deleted: FileX2 } as const
const STATUS_LABELS = { added: 'changes.kindAdded', modified: 'changes.kindModified', deleted: 'changes.kindDeleted' } as const

export function AgentRunChanges({ changes, total, incomplete, onOpen }: { changes: AgentFileChange[]; total?: number; incomplete?: boolean; onOpen: (path: string) => void }) {
  const { t } = useTranslation('agent')
  const [expanded, setExpanded] = useState(false)
  if (changes.length === 0) return null
  const fileCount = Math.max(total ?? changes.length, changes.length)
  const omitted = fileCount - changes.length
  const additions = changes.reduce((sum, change) => sum + change.additions, 0)
  const deletions = changes.reduce((sum, change) => sum + (change.deletions ?? 0), 0)
  const hasDeletions = changes.some((change) => change.deletions !== null)
  return (
    <section className="overflow-hidden rounded-xl border border-separator bg-fill-3 text-xs">
      <div className="border-b border-separator px-3 py-2 text-label-2">
        {t('changes.title')} · {t('changes.fileCount', { count: fileCount })} · <span className="text-emerald-500">+{additions}</span>{hasDeletions ? <> <span className="text-red-500">−{deletions}</span></> : null}
        {incomplete ? <p className="mt-0.5 text-[11px] text-muted-soft">{t('changes.incomplete')}</p> : null}
      </div>
      <div>
        {(expanded ? changes : changes.slice(0, 5)).map((change) => {
          const separator = change.path.lastIndexOf('/')
          const directory = separator >= 0 ? change.path.slice(0, separator) : ''
          const name = separator >= 0 ? change.path.slice(separator + 1) : change.path
          const Icon = change.status ? STATUS_ICONS[change.status] : change.kind === 'write' ? FilePlus2 : FilePenLine
          const label = change.status ? STATUS_LABELS[change.status] : change.kind === 'write' ? 'changes.kindWrite' : 'changes.kindEdit'
          return (
            <button
              key={change.path}
              type="button"
              data-change-path={change.path}
              onClick={() => onOpen(change.path)}
              className="flex w-full items-center gap-2 border-b border-separator px-3 py-2 text-left transition-colors last:border-b-0 hover:bg-fill-2"
            >
              <Icon className="h-3.5 w-3.5 shrink-0 text-muted-soft" />
              <span className="shrink-0 text-label-2">{t(label)}</span>
              <span className={cn('min-w-0 flex-1 truncate font-mono text-[12px] text-label', change.status === 'deleted' && 'line-through decoration-label-3')} title={change.path}>
                {name}{directory ? <span className="text-faint"> · {directory}</span> : null}
              </span>
              {change.binary ? (
                <span className="shrink-0 tabular-nums text-label-3">{formatBytes(change.size ?? 0)}</span>
              ) : (
                <>
                  <span className="shrink-0 tabular-nums text-emerald-500">+{change.additions}</span>
                  {change.deletions !== null ? <span className="shrink-0 tabular-nums text-red-500">−{change.deletions}</span> : null}
                </>
              )}
              <span className="shrink-0 text-label-3">{t('changes.view')}</span>
            </button>
          )
        })}
      </div>
      {changes.length > 5 ? (
        <Button variant="ghost" size="sm" className="h-8 w-full rounded-none text-xs text-label-2" onClick={() => setExpanded((value) => !value)}>
          {expanded ? t('changes.collapse') : t('changes.showAll', { count: changes.length })}
        </Button>
      ) : null}
      {omitted > 0 ? <p className="border-t border-separator px-3 py-2 text-muted-soft">{t('changes.omitted', { count: omitted })}</p> : null}
    </section>
  )
}
