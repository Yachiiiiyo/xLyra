import { cn } from '@/lib/utils'

export function PlainText({
  text,
  className,
  preserveNewlines = false,
}: {
  text: string
  className?: string
  preserveNewlines?: boolean
}) {
  if (!text) return null
  return <p className={cn('break-words', preserveNewlines && 'whitespace-pre-wrap', className)}>{text}</p>
}
