import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Copy, Download, X } from 'lucide-react'
import { LiquidGlass } from '@/components/glass/liquid-glass'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Draw, DrawBody, DrawContent, DrawHeader, DrawTitle } from '@/components/ui/draw'
import { AgentDiffView } from '@/features/agent/components/agent-diff-view'
import { AgentFilePreview } from '@/features/agent/components/agent-file-preview'
import { SourceView } from '@/features/agent/components/agent-source-view'
import { downloadSessionFile, fetchSessionDiff, fetchSessionFile, sessionDiffErrorKind } from '@/features/agent/api/agent'
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
  /** 「文件列表」模式:外壳换成列表内容 */
  filesList?: { title: string; children: ReactNode } | null
  /** 从文件列表进入的预览才显示「返回文件列表」 */
  onBackToFiles?: () => void
}

type PreviewTabName = 'preview' | 'diff' | 'source'

export function AgentPreviewPanel({ changes, path, onSelectPath, onClose, mobile, sessionId, filesList, onBackToFiles }: AgentPreviewPanelProps) {
  const { t } = useTranslation('agent')
  const current = changes.find((change) => change.path === path)
  const server = current?.source === 'server'
  const patches = current && !server ? changePatches(current) : []
  const previewKind = previewKindForPath(path)
  const hasDiffTab = server ? current?.status === 'deleted' || Boolean(current?.hasDiff) : patches.length > 0
  const canPreview = Boolean(sessionId)
    && previewKind !== 'binary'
    && (server ? current?.status !== 'deleted' || !hasDiffTab : true)
  const showPreviewTab = canPreview || (server && !hasDiffTab && Boolean(sessionId))
  // 默认先看预览(能预览的文件);删除的文件只有 diff;不能预览的退回 diff/源码
  const defaultTab: PreviewTabName = showPreviewTab && current?.status !== 'deleted'
    ? 'preview'
    : !server && current?.kind === 'write' && current.content !== undefined ? 'source'
      : hasDiffTab ? 'diff' : !server && current?.content !== undefined ? 'source' : 'preview'
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
  // 复制内容:本地推导项直接用已知内容;其余文本类文件按需向后端取(图片/二进制不提供)
  const canCopyContent = current?.content !== undefined
    || (Boolean(sessionId) && previewKind !== 'binary' && previewKind !== 'image' && current?.status !== 'deleted')
  async function copyContent() {
    if (current?.content !== undefined) {
      await copyToClipboard(current.content, null)
      return
    }
    if (!sessionId) return
    try {
      const file = await fetchSessionFile(sessionId, path)
      if (file.kind === 'text') await copyToClipboard(file.text, null)
    } catch {
      // 取不到内容(过大/不存在)时静默:预览区本身已有对应提示
    }
  }
  const actions = (
    <div className="flex shrink-0 items-center gap-1">
      {onBackToFiles && !filesList ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.backToFiles')} aria-label={t('preview.backToFiles')} onClick={onBackToFiles}>
          <ArrowLeft className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      {!filesList && sessionId ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.download')} aria-label={t('preview.download')} onClick={() => void downloadSessionFile(sessionId, path)}>
          <Download className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      {!filesList && canCopyContent ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.copyContent')} aria-label={t('preview.copyContent')} onClick={() => void copyContent()}>
          <Copy className="h-3.5 w-3.5" />
        </Button>
      ) : null}
      {!mobile ? (
        <Button variant="ghost" size="icon" className="h-8 w-8" title={t('preview.close')} aria-label={t('preview.close')} onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      ) : null}
    </div>
  )

  const body = filesList ? filesList.children : (
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
          {showPreviewTab || hasDiffTab || current?.content !== undefined ? (
            <div role="tablist" className="flex shrink-0 gap-1 border-b border-separator px-4 py-2">
              {showPreviewTab ? <PreviewTab active={tab === 'preview'} onClick={() => setTabSelection({ path, tab: 'preview' })}>{t('preview.tabPreview')}</PreviewTab> : null}
              {hasDiffTab ? <PreviewTab active={tab === 'diff'} onClick={() => setTabSelection({ path, tab: 'diff' })}>{t('preview.tabDiff')}</PreviewTab> : null}
              {!server && current?.content !== undefined ? <PreviewTab active={tab === 'source'} onClick={() => setTabSelection({ path, tab: 'source' })}>{t('preview.tabSource')}</PreviewTab> : null}
            </div>
          ) : null}
          <div className="min-h-0 flex-1 overflow-auto">
            {tab === 'preview' && sessionId ? <AgentFilePreview sessionId={sessionId} path={path} /> : null}
            {tab === 'diff' && server && current ? <ServerDiffView sessionId={sessionId} change={current} path={path} /> : null}
            {tab === 'diff' && !server && patches.length > 0 ? <AgentDiffView patches={patches} /> : null}
            {tab === 'source' && current?.content !== undefined ? <SourceView content={current.content} /> : null}
            {tab !== 'preview' && !server && patches.length === 0 && current?.kind === 'edit' ? <p className="p-4 text-xs text-muted-soft">{t('preview.noPatch')}</p> : null}
            {current?.contentTruncated ? <p className="p-4 text-xs text-muted-soft">{t('preview.tooLarge')}</p> : null}
          </div>
        </>
      ) : <p className="p-4 text-xs text-muted-soft">{t('preview.notInRun')}</p>}
    </>
  )

  if (mobile) {
    return (
      <Draw open onOpenChange={(open) => { if (!open) onClose() }}>
        <DrawContent
          aria-label={t('preview.panelLabel')}
          // 窄屏预览铺满视口:横屏 HTML 才能居中完整显示;lg: 覆盖是因为横屏手机宽度可能超过 1024
          className="top-[calc(env(safe-area-inset-top,0px)+8px)] max-h-none lg:w-[calc(100vw-16px)]"
        >
          <DrawHeader className="flex items-center gap-2 pr-3">
            <div className="min-w-0 flex-1">
              <DrawTitle className="truncate text-sm">{filesList?.title ?? fileName}</DrawTitle>
              {filesList ? null : <p className="truncate text-xs text-faint" title={path}>{path}</p>}
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
          <h2 className="truncate text-sm font-semibold text-label">{filesList?.title ?? fileName}</h2>
          {filesList ? null : <p className="truncate text-xs text-faint" title={path}>{path}</p>}
        </div>
        {actions}
      </div>
      {body}
    </LiquidGlass>
  )
}

/** server 项的「变更」标签:diff 由后端接口按 turn 提供,410/404 区分空态 */
function ServerDiffView({ sessionId, change, path }: { sessionId?: string; change: AgentFileChange; path: string }) {
  const { t } = useTranslation('agent')
  const deleted = change.status === 'deleted'
  const query = useQuery({
    queryKey: ['agent', 'session-diff', sessionId ?? '', change.turnId ?? '', path],
    queryFn: () => fetchSessionDiff(sessionId ?? '', change.turnId ?? '', path),
    staleTime: 0,
    retry: false,
    enabled: Boolean(sessionId && change.turnId) && (change.hasDiff || deleted),
  })
  if (!change.hasDiff && !deleted) return <PanelEmpty message={t('preview.noPatch')} />
  if (query.isPending) return <div className="p-4"><Skeleton className="h-48 w-full" /></div>
  if (query.isError) {
    const kind = sessionDiffErrorKind(query.error)
    if (kind === 'gone') return <PanelEmpty message={t('preview.diffGone')} />
    if (kind === 'missing') return <PanelEmpty message={deleted ? t('preview.fileDeleted') : t('preview.missing')} />
    return <PanelEmpty message={t('preview.loadFailed')}><Button variant="outline" size="sm" onClick={() => void query.refetch()}>{t('preview.retry')}</Button></PanelEmpty>
  }
  const text = query.data ?? ''
  if (!text.trim()) return <PanelEmpty message={deleted ? t('preview.fileDeleted') : t('preview.noPatch')} />
  return (
    <>
      <AgentDiffView patches={[text]} />
      {change.truncated ? <p className="border-t border-separator p-4 text-xs text-muted-soft">{t('preview.diffTruncated')}</p> : null}
    </>
  )
}

function PanelEmpty({ message, children }: { message: string; children?: ReactNode }) {
  return <div className="flex h-full min-h-40 flex-col items-center justify-center gap-3 p-6 text-center text-xs text-muted-soft"><p>{message}</p>{children}</div>
}

function PreviewTab({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return (
    <button type="button" role="tab" aria-selected={active} onClick={onClick} className={cn('rounded-lg px-3 py-1.5 text-xs transition-colors', active ? 'bg-fill-1 text-label' : 'text-label-3 hover:text-label')}>
      {children}
    </button>
  )
}
