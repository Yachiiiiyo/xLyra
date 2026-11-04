import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FilePenLine, FilePlus2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { AgentFileChange } from '@/features/agent/lib/run-changes'

export function AgentRunChanges({ changes, onOpen }: { changes: AgentFileChange[]; onOpen: (path: string) => void }) {
  const { t } = useTranslation('agent')
  const [expanded, setExpanded] = useState(false)
  if (changes.length === 0) return null
  const additions = changes.reduce((total, change) => total + change.additions, 0)
  const deletions = changes.reduce((total, change) => total + (change.deletions ?? 0), 0)
  const hasDeletions = changes.some((change) => change.deletions !== null)
  return (
    <section className="overflow-hidden rounded-xl border border-separator bg-fill-3 text-xs">
      <div className="border-b border-separator px-3 py-2 text-label-2">
        {t('changes.title')} · {t('changes.fileCount', { count: changes.length })} · <span className="text-emerald-500">+{additions}</span>{hasDeletions ? <> <span className="text-red-500">−{deletions}</span></> : null}
      </div>
      <div>
        {(expanded ? changes : changes.slice(0, 5)).map((change) => {
          const separator = change.path.lastIndexOf('/')
          const directory = separator >= 0 ? change.path.slice(0, separator) : ''
          const name = separator >= 0 ? change.path.slice(separator + 1) : change.path
          const Icon = change.kind === 'write' ? FilePlus2 : FilePenLine
          return (
            <button
              key={change.path}
              type="button"
              data-change-path={change.path}
              onClick={() => onOpen(change.path)}
              className="flex w-full items-center gap-2 border-b border-separator px-3 py-2 text-left transition-colors last:border-b-0 hover:bg-fill-2"
            >
              <Icon className="h-3.5 w-3.5 shrink-0 text-muted-soft" />
              <span className="shrink-0 text-label-2">{t(change.kind === 'write' ? 'changes.kindWrite' : 'changes.kindEdit')}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-label" title={change.path}>
                {name}{directory ? <span className="text-faint"> · {directory}</span> : null}
              </span>
              <span className="shrink-0 tabular-nums text-emerald-500">+{change.additions}</span>
              {change.deletions !== null ? <span className="shrink-0 tabular-nums text-red-500">−{change.deletions}</span> : null}
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
    </section>
  )
}
