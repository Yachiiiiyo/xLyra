import { useTranslation } from 'react-i18next'
import { parseUnifiedPatch, type DiffLine } from '@/features/agent/lib/run-changes'
import { cn } from '@/lib/utils'

export function AgentDiffView({ patches }: { patches: string[] }) {
  const { t } = useTranslation('agent')
  return (
    <div className="overflow-x-auto font-mono text-[12px] leading-5">
      {patches.map((patch, patchIndex) => (
        <div key={patchIndex}>
          {patchIndex > 0 ? (
            <div className="my-3 border-t border-separator pt-2 text-[11px] text-faint">
              {t('changes.editIndex', { index: patchIndex + 1 })}
            </div>
          ) : null}
          {parseUnifiedPatch(patch).map((hunk, hunkIndex) => (
            <div key={hunkIndex} className="min-w-max">
              <div className="whitespace-pre bg-fill-3 px-3 py-1 text-faint">{hunk.header}</div>
              {hunk.lines.map((line, lineIndex) => <DiffRow key={lineIndex} line={line} />)}
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}

function DiffRow({ line }: { line: DiffLine }) {
  const { t } = useTranslation('agent')
  const prefix = line.kind === 'add' ? '+' : line.kind === 'del' ? '-' : ' '
  return (
    <div
      className={cn('flex min-w-full whitespace-pre px-3', line.kind === 'add' && 'bg-emerald-500/10', line.kind === 'del' && 'bg-red-500/10')}
      aria-label={line.kind === 'add' ? t('changes.addedLine') : line.kind === 'del' ? t('changes.removedLine') : undefined}
    >
      <span className="w-10 shrink-0 pr-3 text-right tabular-nums text-faint">{line.oldNo ?? ''}</span>
      <span className="w-10 shrink-0 pr-3 text-right tabular-nums text-faint">{line.newNo ?? ''}</span>
      <span className="w-4 shrink-0 select-none text-faint">{prefix}</span>
      <span>{line.text}</span>
    </div>
  )
}
