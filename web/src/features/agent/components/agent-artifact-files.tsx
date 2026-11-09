import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChevronRight, File, FileImage, FileText } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { AgentSessionImage } from '@/features/agent/components/agent-session-image'
import { SourceView } from '@/features/agent/components/agent-source-view'
import { cn } from '@/lib/utils'
import type { AgentArtifactFile, AgentArtifacts } from '@/features/agent/api/agent'
import { downloadSessionFile, fetchSessionFile } from '@/features/agent/api/agent'
import { sortArtifactFiles } from '@/features/agent/lib/artifacts'
import { formatBytes } from '@/features/agent/lib/format-bytes'
import { formatRelativeTime } from '@/features/agent/lib/relative-time'

const KIND_ICONS = { text: FileText, image: FileImage, binary: File } as const

export function AgentArtifactFiles({ sessionId, artifacts, onPreview }: { sessionId: string; artifacts: AgentArtifacts; onPreview: (path: string) => void }) {
  const { t, i18n } = useTranslation('agent')
  const files = useMemo(() => sortArtifactFiles(artifacts.files), [artifacts.files])
  // 同时只展开一个文件
  const [openPath, setOpenPath] = useState<string | null>(null)
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      {files.length === 0 ? (
        <p className="p-4 text-xs text-muted-soft">{t('files.empty')}</p>
      ) : (
        <ul>
          {files.map((file) => <ArtifactRow key={file.path} file={file} sessionId={sessionId} locale={i18n.language} open={openPath === file.path} onToggle={() => setOpenPath((current) => (current === file.path ? null : file.path))} onPreview={onPreview} />)}
        </ul>
      )}
      {artifacts.truncated ? <p className="border-t border-separator px-4 py-2 text-xs text-muted-soft">{t('files.truncated')}</p> : null}
    </div>
  )
}

function ArtifactRow({ file, sessionId, locale, open, onToggle, onPreview }: { file: AgentArtifactFile; sessionId: string; locale: string; open: boolean; onToggle: () => void; onPreview: (path: string) => void }) {
  const { t } = useTranslation('agent')
  const Icon = KIND_ICONS[file.kind] ?? File
  // 列表内文字统一 text-xs:路径等宽,其余沿用正文字体
  return (
    <li className="border-b border-separator last:border-b-0">
      {/* 展开时文件行吸附在顶部直到被下一个文件行顶走;半透明加模糊,不能用不透明底色 */}
      <div className={cn('flex items-center gap-2 px-4 py-2 text-xs', open && 'sticky top-0 z-[1] border-b border-separator bg-fill-3 backdrop-blur-xl')}>
        <button type="button" aria-expanded={open} onClick={onToggle} className="flex min-w-0 flex-1 items-center gap-2 text-left" title={file.path}>
          <ChevronRight className={cn('h-3 w-3 shrink-0 text-muted-soft transition-transform', open && 'rotate-90')} />
          <Icon className="h-3.5 w-3.5 shrink-0 text-muted-soft" />
          <span className="min-w-0 flex-1 truncate font-mono text-label">{file.path}</span>
        </button>
        <span className="shrink-0 tabular-nums text-label-3">{formatBytes(file.size)}</span>
        <span className="w-20 shrink-0 text-right text-faint">{formatRelativeTime(file.mtime, locale)}</span>
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={() => onPreview(file.path)}>{t('files.preview')}</Button>
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={() => void downloadSessionFile(sessionId, file.path)}>{t('files.download')}</Button>
      </div>
      {open ? <div className="bg-fill-3"><RawContent file={file} sessionId={sessionId} /></div> : null}
    </li>
  )
}

/** 展开后检查文件原文:文本直接按源码显示,图片显示缩略图,其余提示不支持 */
function RawContent({ file, sessionId }: { file: AgentArtifactFile; sessionId: string }) {
  const { t } = useTranslation('agent')
  const query = useQuery({
    queryKey: ['agent', 'session-file', sessionId, file.path],
    queryFn: () => fetchSessionFile(sessionId, file.path),
    staleTime: 0,
    retry: false,
    enabled: file.kind === 'text',
  })
  if (file.kind === 'image') return <div className="p-3"><AgentSessionImage sessionId={sessionId} path={file.path} alt={file.path} /></div>
  if (file.kind !== 'text') return <p className="p-3 text-xs text-muted-soft">{t('preview.unsupported')}</p>
  if (query.isPending) return <div className="p-3"><Skeleton className="h-16 w-full" /></div>
  if (query.isError || query.data.kind !== 'text') return <p className="p-3 text-xs text-muted-soft">{t('preview.loadFailed')}</p>
  return <SourceView content={query.data.text} />
}
