import { useState } from 'react'
import { cn } from '@/lib/utils'

const sizeClass = {
  sm: 'h-8 w-8',
  md: 'h-10 w-10',
  lg: 'h-12 w-12',
} as const

export function PluginAvatar({
  iconUrl,
  name,
  size = 'md',
  className,
}: {
  iconUrl?: string
  name: string
  size?: 'sm' | 'md' | 'lg'
  className?: string
}) {
  const [failedUrl, setFailedUrl] = useState<string | null>(null)
  const box = sizeClass[size]
  if (iconUrl && failedUrl !== iconUrl) {
    return (
      <span
        className={cn(
          'relative inline-block shrink-0 overflow-hidden rounded-xl bg-elevated-1 ring-1 ring-inset ring-white/10',
          box,
          className,
        )}
      >
        <img
          src={iconUrl}
          alt=""
          loading="lazy"
          decoding="async"
          draggable={false}
          referrerPolicy="no-referrer"
          className="absolute inset-0 h-full w-full object-cover"
          onError={() => setFailedUrl(iconUrl)}
        />
      </span>
    )
  }
  const letter = name.trim().slice(0, 1).toUpperCase() || '?'
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-xl bg-elevated-1 text-headline text-label ring-1 ring-inset ring-white/10',
        box,
        className,
      )}
    >
      {letter}
    </span>
  )
}
