import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Hash } from 'lucide-react'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'
import {
  filterReferenceableSessions,
  isSessionId,
  searchReferenceSessions,
  shortSessionId,
  type AgentSessionReference,
} from '@/features/agent/lib/references'
import type { AgentSession } from '@/features/agent/api/agent'

type AgentReferenceDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  sessions: AgentSession[]
  /** 当前会话 id：不能引用自己 */
  currentId: string | null
  /** 已选中的引用 id：列表中不再出现 */
  selectedIds: string[]
  onSelect: (reference: AgentSessionReference) => void
}

/** 引用会话选择器：搜索标题 / 粘贴会话 ID，点选一行即引用并关闭（按需挂载，每次打开都是干净的搜索态） */
export function AgentReferenceDialog({ open, onOpenChange, sessions, currentId, selectedIds, onSelect }: AgentReferenceDialogProps) {
  const { t, i18n } = useTranslation('agent')
  const [query, setQuery] = useState('')

  const excluded = useMemo(() => new Set([...(currentId ? [currentId] : []), ...selectedIds]), [currentId, selectedIds])
  const pastedId = isSessionId(query) ? query.trim().toLowerCase() : null
  const rows = useMemo(
    () => searchReferenceSessions(filterReferenceableSessions(sessions, currentId, selectedIds), query),
    [sessions, currentId, selectedIds, query],
  )
  const showUseId = Boolean(pastedId && !excluded.has(pastedId))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm" className="w-[min(92vw,480px)]">
        <DialogHeader>
          <DialogTitle>{t('reference.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('reference.dialogHint')}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t('reference.searchPlaceholder')}
          />
          {showUseId && pastedId ? (
            <button
              type="button"
              data-agent-reference-use-id="true"
              onClick={() => onSelect({ sessionId: pastedId, title: sessions.find((session) => session.session_id === pastedId)?.title ?? '' })}
              className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-label-2 transition-colors hover:bg-fill-4 hover:text-label"
            >
              <Hash className="h-4 w-4 shrink-0 text-current" />
              <span className="min-w-0 flex-1 truncate">{t('reference.useId')}</span>
              <span className="shrink-0 font-mono text-xs text-faint">{shortSessionId(pastedId)}</span>
            </button>
          ) : null}
          <div className="space-y-0.5">
            {rows.length === 0 ? (
              <p className="px-2.5 py-6 text-center text-xs text-muted-soft">
                {query.trim() ? t('reference.noMatch') : t('reference.empty')}
              </p>
            ) : rows.map((session) => (
              <button
                key={session.session_id}
                type="button"
                data-agent-reference-row="true"
                onClick={() => onSelect({ sessionId: session.session_id, title: session.title ?? session.preview ?? '' })}
                className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-label-2 transition-colors hover:bg-fill-4 hover:text-label"
              >
                <span className="min-w-0 flex-1 truncate">{session.title || session.preview || t('sidebar.untitled')}</span>
                <span className="shrink-0 text-[11px] text-faint">{formatRelativeTime(session.updated_at, i18n.language)}</span>
                <span className="shrink-0 font-mono text-[11px] text-faint">{shortSessionId(session.session_id)}</span>
              </button>
            ))}
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
