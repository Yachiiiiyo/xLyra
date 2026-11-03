import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Download, ImageOff } from 'lucide-react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { objectUrlFor } from '@/features/agent/lib/object-url'
import { apiFetchBlob } from '@/lib/http'
import { cn } from '@/lib/utils'

export function AgentGeneratedImage({ sessionId, path, alt, compact = false }: {
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
        <DialogContent surface="none" presentation="center" overlayClassName="bg-black/85" className="w-auto max-w-[92vw] bg-transparent p-0 shadow-none backdrop-blur-0">
          <DialogTitle className="sr-only">{alt || path}</DialogTitle>
          {url ? (
            <div className="flex flex-col items-center gap-3">
              <img src={url} alt={alt || path} className="max-h-[80dvh] max-w-[92vw] rounded-2xl object-contain" />
              <button
                type="button"
                onClick={download}
                className="inline-flex items-center gap-2 rounded-full bg-white/15 px-4 py-2 text-body text-white backdrop-blur-md transition-colors hover:bg-white/25"
              >
                <Download className="h-4 w-4" />
                {t('chat.image.download')}
              </button>
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
    </span>
  )
}
