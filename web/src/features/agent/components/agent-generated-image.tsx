import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Copy, Download, ImageOff, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { copyToClipboard } from '@/components/common/copy-to-clipboard'
import { objectUrlFor } from '@/features/agent/lib/object-url'
import { apiFetchBlob } from '@/lib/http'
import { cn } from '@/lib/utils'

export function AgentGeneratedImage({ sessionId, path, alt, model, site, compact = false }: {
  sessionId: string
  path: string
  alt?: string
  model?: string
  site?: string
  compact?: boolean
}) {
  const { t } = useTranslation('agent')
  const [open, setOpen] = useState(false)
  const query = useQuery({
    queryKey: ['agent', 'session-image', sessionId, path],
    queryFn: async () => (await apiFetchBlob(`/api/v1/agent/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(path)}`)).blob,
    staleTime: Infinity,
    retry: false,
  })

  const url = query.data ? objectUrlFor(query.data) : undefined

  function download() {
    if (!query.data) return
    const downloadUrl = URL.createObjectURL(query.data)
    const anchor = document.createElement('a')
    anchor.href = downloadUrl
    anchor.download = path.slice(path.lastIndexOf('/') + 1)
    anchor.click()
    URL.revokeObjectURL(downloadUrl)
  }

  const modelLabel = model && site ? t('chat.image.caption', { model, site }) : model || site
  const caption = [alt, modelLabel].filter(Boolean).join(' · ')

  if (query.isError) {
    return (
      <span className="inline-flex w-full max-w-md items-center justify-center gap-2 rounded-xl border border-separator bg-fill-3 px-4 py-3 text-center text-xs text-muted-soft">
        <ImageOff className="h-4 w-4 shrink-0" />
        {t('chat.image.missing')}
      </span>
    )
  }

  return (
    <span className="block w-full max-w-full">
      <button
        type="button"
        disabled={!url}
        onClick={() => setOpen(true)}
        className="block max-w-full overflow-hidden rounded-xl text-left disabled:cursor-wait"
        aria-label={alt || path}
      >
        {!url ? <Skeleton className={cn('aspect-square w-full', compact ? 'max-h-40' : 'max-h-[420px]')} /> : (
          <img
            src={url}
            alt={alt || path}
            loading="lazy"
            className={cn('h-auto w-auto max-w-full rounded-xl object-contain', compact ? 'max-h-40' : 'max-h-[420px]')}
          />
        )}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent presentation="center" className="w-[min(94vw,1100px)] max-w-none overflow-hidden p-4">
          <DialogTitle className="sr-only">{alt || path}</DialogTitle>
          {url ? <img src={url} alt={alt || path} className="max-h-[72dvh] w-full rounded-lg object-contain" /> : null}
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {caption ? <p className="min-w-0 flex-1 break-words text-xs text-muted-soft">{caption}</p> : <span className="flex-1" />}
            <Button size="sm" variant="outline" onClick={download}><Download className="h-3.5 w-3.5" />{t('chat.image.download')}</Button>
            <Button size="sm" variant="outline" onClick={() => void copyToClipboard(path, null, null)}><Copy className="h-3.5 w-3.5" />{t('chat.image.copyPath')}</Button>
            <Button size="sm" variant="outline" onClick={() => setOpen(false)}><X className="h-3.5 w-3.5" />{t('chat.image.close')}</Button>
          </div>
        </DialogContent>
      </Dialog>
    </span>
  )
}
