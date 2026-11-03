import { useState } from 'react'
import * as PopoverPrimitive from '@radix-ui/react-popover'
import { useTranslation } from 'react-i18next'
import { Copy, Ellipsis, GitBranch, MoveLeft, Plus, Settings2, ShieldOff, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { toast } from '@/lib/toast'
import { AgentConfirmDialog } from '@/features/agent/components/agent-confirm-dialog'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'
import type { AgentSession } from '@/features/agent/api/agent'

type AgentSidebarProps = {
  className?: string
  sessions: AgentSession[]
  activeId: string | null
  onBack: () => void
  onSelect: (id: string) => void
  onNew: () => void
  onDelete: (id: string) => void
  onFork: (id: string) => void
  onRevokeAccess: (id: string) => void
  onOpenSettings: () => void
}

export function AgentSidebar({ className, sessions, activeId, onBack, onSelect, onNew, onDelete, onFork, onRevokeAccess, onOpenSettings }: AgentSidebarProps) {
  const { t, i18n } = useTranslation('agent')
  const [pendingDelete, setPendingDelete] = useState<string | null>(null)

  return (
    <div className={cn('flex h-full min-h-0 flex-col p-3', className)}>
      <button
        type="button"
        onClick={onBack}
        className="flex h-10 max-lg:h-9 shrink-0 items-center gap-2 rounded-full px-3 max-lg:px-2.5 text-sm max-lg:text-[0.8rem] text-muted-soft transition-colors hover:bg-[hsl(var(--surface-subtle))] hover:text-foreground"
      >
        <MoveLeft className="h-4 w-4 shrink-0" />
        {t('header.back')}
      </button>

      <button
        type="button"
        onClick={onNew}
        className="mt-1 flex h-10 max-lg:h-9 shrink-0 items-center gap-2 rounded-full px-3 max-lg:px-2.5 text-sm max-lg:text-[0.8rem] font-medium text-foreground transition-colors hover:bg-[hsl(var(--surface-subtle))]"
      >
        <Plus className="h-4 w-4 shrink-0" />
        {t('sidebar.new')}
      </button>

      <p className="mt-4 max-lg:mt-2 px-3 max-lg:px-2.5 pb-2 max-lg:pb-1.5 text-xs max-lg:text-[0.68rem] font-medium text-faint">{t('sidebar.recent')}</p>

      <div className="min-h-0 flex-1 space-y-0.5 overflow-y-auto overscroll-contain">
        {sessions.length === 0 ? (
          <p className="px-3 py-6 text-center text-xs text-muted-soft">{t('sidebar.empty')}</p>
        ) : (
          sessions.map((session) => (
            <SessionRow
              key={session.session_id}
              session={session}
              active={session.session_id === activeId}
              locale={i18n.language}
              untitledLabel={t('sidebar.untitled')}
              menuLabel={t('sidebar.menu')}
              revokeLabel={revokeAccessLabel(session, t)}
              deleteLabel={t('sidebar.delete')}
              copyIdLabel={t('sidebar.copyId')}
              forkLabel={t('sidebar.fork')}
              idCopiedLabel={t('sidebar.idCopied')}
              onSelect={onSelect}
              onDelete={setPendingDelete}
              onFork={onFork}
              onRevoke={onRevokeAccess}
            />
          ))
        )}
      </div>

      <button
        type="button"
        onClick={onOpenSettings}
        className="mt-2 flex h-10 max-lg:h-9 shrink-0 items-center gap-2 rounded-full px-3 max-lg:px-2.5 text-sm max-lg:text-[0.8rem] text-muted-soft transition-colors hover:bg-[hsl(var(--surface-subtle))] hover:text-foreground"
      >
        <Settings2 className="h-4 w-4 shrink-0" />
        {t('settings.entry')}
      </button>

      <AgentConfirmDialog
        open={pendingDelete !== null}
        title={t('sidebar.delete')}
        description={t('sidebar.deleteConfirm')}
        confirmLabel={t('sidebar.delete')}
        destructive
        onCancel={() => setPendingDelete(null)}
        onConfirm={() => {
          if (pendingDelete) onDelete(pendingDelete)
          setPendingDelete(null)
        }}
      />
    </div>
  )
}

function revokeAccessLabel(session: AgentSession, t: (key: string, options?: Record<string, unknown>) => string): string {
  const tools = (session.session_allow ?? []).filter(Boolean)
  if (tools.length === 0) return ''
  const readable = tools.map((tool) => {
    const name = tool.toLowerCase()
    if (name === 'write') return t('permission.tool.write')
    if (name === 'edit') return t('permission.tool.edit')
    if (name === 'bash') return t('permission.tool.bash')
    return tool
  })
  return t('sidebar.revokeAccess', { tools: readable.join(t('work.summaryJoin')) })
}

function SessionRow({
  session,
  active,
  locale,
  untitledLabel,
  menuLabel,
  revokeLabel,
  deleteLabel,
  copyIdLabel,
  forkLabel,
  idCopiedLabel,
  onSelect,
  onDelete,
  onFork,
  onRevoke,
}: {
  session: AgentSession
  active: boolean
  locale: string
  untitledLabel: string
  menuLabel: string
  revokeLabel: string
  deleteLabel: string
  copyIdLabel: string
  forkLabel: string
  idCopiedLabel: string
  onSelect: (id: string) => void
  onDelete: (id: string) => void
  onFork: (id: string) => void
  onRevoke: (id: string) => void
}) {
  const [menuOpen, setMenuOpen] = useState(false)

  return (
    <div
      data-agent-session-row="true"
      data-active={active}
      className={cn(
        'group flex min-h-10 max-lg:min-h-9 items-center gap-1 rounded-full px-3 max-lg:px-2.5 py-2 max-lg:py-1.5 text-sm max-lg:text-[0.78rem] transition-colors',
        active
          ? 'nav-item-selected text-label'
          : 'text-label-2 hover:bg-fill-4 hover:text-label',
      )}
    >
      <button
        type="button"
        className="min-w-0 flex-1 text-left"
        onClick={() => onSelect(session.session_id)}
      >
        <span className="block truncate">{session.title || session.preview || untitledLabel}</span>
      </button>
      <span className="shrink-0 text-[11px] text-faint">{formatRelativeTime(session.updated_at, locale)}</span>
      <PopoverPrimitive.Root open={menuOpen} onOpenChange={setMenuOpen}>
        <PopoverPrimitive.Trigger asChild>
          <button
            type="button"
            className={cn(
              'shrink-0 rounded p-1 text-muted-soft transition-opacity hover:text-foreground',
              menuOpen || active
                ? 'opacity-100'
                // 移动端没有 hover，让「三个点」菜单常驻；桌面端保留悬停行才显示
                : 'opacity-100 md:opacity-0 md:group-hover:opacity-100',
            )}
            aria-label={menuLabel}
          >
            <Ellipsis className="h-4 w-4" />
          </button>
        </PopoverPrimitive.Trigger>
        <PopoverPrimitive.Portal>
          <PopoverPrimitive.Content
            align="end"
            sideOffset={6}
            onClick={(event) => event.stopPropagation()}
            className="lg-regular z-[120] w-max min-w-36 rounded-card p-1.5"
          >
            <button
              type="button"
              data-agent-copy-id="true"
              onClick={() => {
                setMenuOpen(false)
                void copyToClipboard(session.session_id, null, null).then((ok) => {
                  if (ok) toast.success(idCopiedLabel)
                })
              }}
              className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-label-2 transition-colors hover:bg-fill-4 hover:text-label"
            >
              <Copy className="h-4 w-4 shrink-0 text-current" />
              {copyIdLabel}
            </button>
            <button
              type="button"
              data-agent-fork="true"
              onClick={() => {
                setMenuOpen(false)
                onFork(session.session_id)
              }}
              className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-label-2 transition-colors hover:bg-fill-4 hover:text-label"
            >
              <GitBranch className="h-4 w-4 shrink-0 text-current" />
              {forkLabel}
            </button>
            {revokeLabel ? (
              <button
                type="button"
                data-agent-revoke="true"
                onClick={() => {
                  setMenuOpen(false)
                  onRevoke(session.session_id)
                }}
                className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-label-2 transition-colors hover:bg-fill-4 hover:text-label"
              >
                <ShieldOff className="h-4 w-4 shrink-0 text-current" />
                {revokeLabel}
              </button>
            ) : null}
            <button
              type="button"
              data-agent-delete="true"
              onClick={() => {
                setMenuOpen(false)
                onDelete(session.session_id)
              }}
              className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-danger/90 transition-colors hover:bg-danger/10 hover:text-danger"
            >
              <Trash2 className="h-4 w-4 shrink-0 text-current" />
              {deleteLabel}
            </button>
          </PopoverPrimitive.Content>
        </PopoverPrimitive.Portal>
      </PopoverPrimitive.Root>
    </div>
  )
}
