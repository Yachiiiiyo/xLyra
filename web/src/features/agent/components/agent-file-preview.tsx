import { useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { MarkdownMessage } from '@/components/chat/markdown-message'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { downloadSessionFile, fetchSessionFile, sessionFileErrorKind } from '@/features/agent/api/agent'
import { AgentSessionImage } from '@/features/agent/components/agent-session-image'
import { SourceView } from '@/features/agent/components/agent-source-view'
import { parseDelimited } from '@/features/agent/lib/csv'
import { buildPreviewDocument, HTML_PREVIEW_MAX_BYTES, PREVIEW_SANDBOX } from '@/features/agent/lib/html-preview'
import { objectUrlFor } from '@/features/agent/lib/object-url'
import { previewKindForPath } from '@/features/agent/lib/preview-kind'
import { resolveRelativePath } from '@/features/agent/lib/relative-path'

const CSV_PREVIEW_ROWS = 1000

export function AgentFilePreview({ sessionId, path }: { sessionId: string; path: string }) {
  const { t } = useTranslation('agent')
  const kind = previewKindForPath(path)
  const query = useQuery({
    queryKey: ['agent', 'session-file', sessionId, path],
    queryFn: () => fetchSessionFile(sessionId, path),
    staleTime: 0,
    retry: false,
    enabled: kind !== 'binary',
  })
  const download = <Button variant="outline" size="sm" onClick={() => void downloadSessionFile(sessionId, path)}><Download className="h-3.5 w-3.5" />{t('preview.download')}</Button>

  if (kind === 'binary') return <EmptyPreview message={t('preview.unsupported')}>{download}</EmptyPreview>
  if (query.isPending) return <div className="p-4"><Skeleton className="h-48 w-full" /></div>
  if (query.isError) {
    const errorKind = sessionFileErrorKind(query.error)
    if (errorKind === 'missing') return <EmptyPreview message={t('preview.missing')} />
    if (errorKind === 'unsupported') return <EmptyPreview message={t('preview.unsupported')}>{download}</EmptyPreview>
    if (errorKind === 'tooLarge') return <EmptyPreview message={t('preview.tooLarge')}>{download}</EmptyPreview>
    return <EmptyPreview message={t('preview.loadFailed')}><Button variant="outline" size="sm" onClick={() => void query.refetch()}>{t('preview.retry')}</Button></EmptyPreview>
  }

  if (kind === 'image') {
    return query.data.kind === 'image'
      ? <div className="flex h-full items-center justify-center p-4"><img src={objectUrlFor(query.data.blob)} alt={path} className="max-h-full max-w-full object-contain" /></div>
      : <EmptyPreview message={t('preview.unsupported')}>{download}</EmptyPreview>
  }
  if (query.data.kind !== 'text') return <EmptyPreview message={t('preview.unsupported')}>{download}</EmptyPreview>
  const text = query.data.text
  if (kind === 'svg') return <SvgPreview text={text} path={path} />
  if (kind === 'html') return <HtmlPreview text={text} path={path} />
  if (kind === 'markdown') {
    return <div className="p-4"><MarkdownMessage content={text} renderImage={({ src, alt }) => {
      if (!src || /^(?:https?:|data:|blob:|\/)/i.test(src)) return <img src={src} alt={alt} />
      const resolved = resolveRelativePath(path, src)
      return resolved ? <AgentSessionImage sessionId={sessionId} path={resolved} alt={alt} /> : null
    }} /></div>
  }
  if (kind === 'csv') return <DelimitedPreview text={text} delimiter={path.toLowerCase().endsWith('.tsv') ? '\t' : ','} />
  return <SourceView content={text} />
}

function EmptyPreview({ message, children }: { message: string; children?: React.ReactNode }) {
  return <div className="flex h-full min-h-40 flex-col items-center justify-center gap-3 p-6 text-center text-xs text-muted-soft"><p>{message}</p>{children}</div>
}

function SvgPreview({ text, path }: { text: string; path: string }) {
  const blob = useMemo(() => new Blob([text], { type: 'image/svg+xml' }), [text])
  return <div className="flex h-full items-center justify-center p-4"><img src={objectUrlFor(blob)} alt={path} className="max-h-full max-w-full object-contain" /></div>
}

function DelimitedPreview({ text, delimiter }: { text: string; delimiter: string }) {
  const { t } = useTranslation('agent')
  const rows = useMemo(() => parseDelimited(text, delimiter), [text, delimiter])
  return (
    <div className="min-w-0">
      <table className="w-full border-collapse text-xs"><tbody>{rows.slice(0, CSV_PREVIEW_ROWS).map((row, rowIndex) => (
        <tr key={rowIndex} className="border-b border-separator">{row.map((cell, cellIndex) => <td key={cellIndex} className="whitespace-pre-wrap break-words border-r border-separator px-2 py-1.5 align-top last:border-r-0">{cell}</td>)}</tr>
      ))}</tbody></table>
      {rows.length > CSV_PREVIEW_ROWS ? <p className="p-3 text-xs text-muted-soft">{t('preview.csvTruncated', { count: CSV_PREVIEW_ROWS })}</p> : null}
    </div>
  )
}

function HtmlPreview({ text, path }: { text: string; path: string }) {
  const { t } = useTranslation('agent')
  const [generation, setGeneration] = useState(0)
  const [stopped, setStopped] = useState(false)
  const [navigated, setNavigated] = useState(false)
  const loads = useRef(0)
  const reload = () => {
    loads.current = 0
    setGeneration((value) => value + 1)
    setStopped(false)
    setNavigated(false)
  }
  if (new TextEncoder().encode(text).byteLength > HTML_PREVIEW_MAX_BYTES) return <SourceView content={text} />
  if (stopped || navigated) {
    return <EmptyPreview message={navigated ? t('preview.navigated') : ''}><Button variant="outline" size="sm" onClick={reload}>{t('preview.reload')}</Button></EmptyPreview>
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 justify-end border-b border-separator px-2 py-1"><Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setStopped(true)}>{t('preview.stop')}</Button></div>
      <iframe
        key={generation}
        sandbox={PREVIEW_SANDBOX}
        srcDoc={buildPreviewDocument(text)}
        title={path}
        className="h-full min-h-0 w-full flex-1 border-0 bg-white"
        onLoad={() => {
          loads.current += 1
          if (loads.current > 1) setNavigated(true)
        }}
      />
    </div>
  )
}
