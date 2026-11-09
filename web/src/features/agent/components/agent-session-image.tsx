import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ImageOff } from 'lucide-react'
import { Skeleton } from '@/components/ui/skeleton'
import { fetchSessionFile } from '@/features/agent/api/agent'
import { objectUrlFor } from '@/features/agent/lib/object-url'

export function AgentSessionImage({ sessionId, path, alt }: { sessionId: string; path: string; alt?: string }) {
  const { t } = useTranslation('agent')
  const query = useQuery({
    queryKey: ['agent', 'session-file', sessionId, path],
    queryFn: () => fetchSessionFile(sessionId, path),
    staleTime: 0,
    retry: false,
  })
  if (query.isPending) return <Skeleton className="h-32 w-full max-w-md rounded-xl" />
  if (query.isError || query.data.kind !== 'image') {
    return <span className="inline-flex items-center gap-2 rounded-lg bg-fill-3 px-3 py-2 text-xs text-muted-soft"><ImageOff className="h-4 w-4" />{t('chat.image.missing')}</span>
  }
  return <img src={objectUrlFor(query.data.blob)} alt={alt || path} loading="lazy" className="h-auto max-h-[420px] max-w-full rounded-xl object-contain" />
}
