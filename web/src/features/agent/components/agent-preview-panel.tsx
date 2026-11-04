import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Download, FileCode2, X } from 'lucide-react'
import { LiquidGlass } from '@/components/glass/liquid-glass'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { Button } from '@/components/ui/button'
import { Draw, DrawBody, DrawContent, DrawHeader, DrawTitle } from '@/components/ui/draw'
import { AgentDiffView } from '@/features/agent/components/agent-diff-view'
import { AgentFilePreview } from '@/features/agent/components/agent-file-preview'
import { SourceView } from '@/features/agent/components/agent-source-view'
import { downloadSessionFile } from '@/features/agent/api/agent'
import { previewKindForPath } from '@/features/agent/lib/preview-kind'
import { changePatches, type AgentFileChange } from '@/features/agent/lib/run-changes'
import { cn } from '@/lib/utils'

type AgentPreviewPanelProps = {
  changes: AgentFileChange[]
  path: string
  onSelectPath: (path: string) => void
  onClose: () => void
  mobile: boolean
  sessionId?: string
}

type PreviewTabName = 'preview' | 'diff' | 'source'

export function AgentPreviewPanel({ changes, path, onSelectPath, onClose, mobile, sessionId }: AgentPreviewPanelProps) {
  const { t } = useTranslation('agent')
  const current = changes.find((change) => change.path === path)
  const patches = current ? changePatches(current) : []
  const canPreview = Boolean(sessionId) && previewKindForPath(path) !== 'binary'
  const defaultTab: PreviewTabName = current?.kind === 'write' && current.content !== undefined
    ? 'source'
    : sessionId && (!current || (patches.length === 0 && current.content === undefined)) ? 'preview' : 'diff'
  const [tabSelection, setTabSelection] = useState<{ path: string; tab: PreviewTabName } | null>(null)
  const tab = tabSelection?.path === path ? tabSelection.tab : defaultTab
  const [width, setWidth] = useState<number | null>(null)
  const asideRef = useRef<HTMLElement | null>(null)
  const drag = useRef<{ startX: number; startWidth: number; containerWidth: number } | null>(null)

  useEffect(() => {
    if (mobile) return
    const handleKeyDown = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose() }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [mobile, onClose])

  function startResize(event: ReactPointerEvent<HTMLDivElement>) {
    const aside = asideRef.current
    const container = aside?.parentElement?.parentElement
    if (!aside || !container) return
    drag.current = { startX: event.clientX, startWidth: aside.getBoundingClientRect().width, containerWidth: container.getBoundingClientRect().width }
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  function resize(event: ReactPointerEvent<HTMLDivElement>) {
    if (!drag.current) return
    const { startX, startWidth, containerWidth } = drag.current
    const maximum = Math.max(360, containerWidth * 0.6)
    setWidth(Math.min(maximum, Math.max(360, startWidth + startX - event.clientX)))
  }

  const fileName = path.split('/').pop() || path
  const actions = current || sessionId ? (
    <div className="flex shrink-0 items-center gap-1">
      {sessionId ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.download')} aria-label={t('preview.download')} onClick={() => void downloadSessionFile(sessionId, path)}>
          <Download className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.copyPath')} aria-label={t('preview.copyPath')} onClick={() => void copyToClipboard(path, null)}>
        <Copy className="h-3.5 w-3.5" />
      </Button>
      {current?.content !== undefined ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.copyContent')} aria-label={t('preview.copyContent')} onClick={() => void copyToClipboard(current.content!, null)}>
          <FileCode2 className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      {!mobile ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.close')} aria-label={t('preview.close')} onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      ) : null}
    </div>
  ) : null

  const body = (
    <>
      {changes.length > 1 ? (
        <div className="border-b border-separator px-4 py-2">
          <select aria-label={t('preview.selectFile')} value={path} onChange={(event) => onSelectPath(event.target.value)} className="h-8 w-full rounded-lg border border-separator bg-fill-3 px-2 text-xs text-label outline-none">
            {changes.map((change) => <option key={change.path} value={change.path}>{change.path}</option>)}
          </select>
        </div>
      ) : null}
      {current || sessionId ? (
        <>
          {canPreview || patches.length > 0 || current?.content !== undefined ? (
            <div role="tablist" className="flex shrink-0 gap-1 border-b border-separator px-4 py-2">
              {canPreview ? <PreviewTab active={tab === 'preview'} onClick={() => setTabSelection({ path, tab: 'preview' })}>{t('preview.tabPreview')}</PreviewTab> : null}
              {patches.length > 0 ? <PreviewTab active={tab === 'diff'} onClick={() => setTabSelection({ path, tab: 'diff' })}>{t('preview.tabDiff')}</PreviewTab> : null}
              {current?.content !== undefined ? <PreviewTab active={tab === 'source'} onClick={() => setTabSelection({ path, tab: 'source' })}>{t('preview.tabSource')}</PreviewTab> : null}
            </div>
          ) : null}
          <div className="min-h-0 flex-1 overflow-auto">
            {tab === 'preview' && sessionId ? <AgentFilePreview sessionId={sessionId} path={path} /> : null}
            {tab === 'diff' && patches.length > 0 ? <AgentDiffView patches={patches} /> : null}
            {tab === 'source' && current?.content !== undefined ? <SourceView content={current.content} /> : null}
            {tab !== 'preview' && patches.length === 0 && current?.kind === 'edit' ? <p className="p-4 text-xs text-muted-soft">{t('preview.noPatch')}</p> : null}
            {current?.contentTruncated ? <p className="p-4 text-xs text-muted-soft">{t('preview.tooLarge')}</p> : null}
          </div>
        </>
      ) : <p className="p-4 text-xs text-muted-soft">{t('preview.notInRun')}</p>}
    </>
  )

  if (mobile) {
    return (
      <Draw open onOpenChange={(open) => { if (!open) onClose() }}>
        <DrawContent aria-label={t('preview.panelLabel')}>
          <DrawHeader className="flex items-center gap-2 pr-3">
            <div className="min-w-0 flex-1">
              <DrawTitle className="truncate text-sm">{fileName}</DrawTitle>
              <p className="truncate text-xs text-faint" title={path}>{path}</p>
            </div>
            {actions}
          </DrawHeader>
          <DrawBody className="flex flex-col p-0">{body}</DrawBody>
        </DrawContent>
      </Draw>
    )
  }

  return (
    <LiquidGlass
      ref={asideRef}
      as="aside"
      role="complementary"
      aria-label={t('preview.panelLabel')}
      className="relative my-4 mr-4 hidden h-[calc(100%-2rem)] shrink-0 flex-col overflow-hidden rounded-shell lg:flex"
      style={{ width: width ?? '40%', minWidth: 360, maxWidth: '60%' }}
    >
      <div role="separator" aria-orientation="vertical" className="absolute inset-y-0 left-0 z-10 w-1 cursor-col-resize" onPointerDown={startResize} onPointerMove={resize} onPointerUp={() => { drag.current = null }} onPointerCancel={() => { drag.current = null }} />
      <div className="flex shrink-0 items-center gap-2 border-b border-separator px-4 py-3 pl-5">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-semibold text-label">{fileName}</h2>
          <p className="truncate text-xs text-faint" title={path}>{path}</p>
        </div>
        {actions}
      </div>
      {body}
    </LiquidGlass>
  )
}

function PreviewTab({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return (
    <button type="button" role="tab" aria-selected={active} onClick={onClick} className={cn('rounded-lg px-3 py-1.5 text-xs transition-colors', active ? 'bg-fill-1 text-label' : 'text-label-3 hover:text-label')}>
      {children}
    </button>
  )
}
