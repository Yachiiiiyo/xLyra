import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

const ACTION_BUTTON_CLASS =
  'flex h-7 w-7 items-center justify-center rounded-md text-label-2 transition-colors hover:bg-fill-3 hover:text-label disabled:opacity-40'

type MessageActionProps = {
  label: string
  onClick: () => void
  disabled?: boolean
  className?: string
  children: ReactNode
}

export function MessageAction({
  label,
  onClick,
  disabled,
  className,
  children,
}: MessageActionProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className={cn(ACTION_BUTTON_CLASS, className)}
    >
      {children}
    </button>
  )
}

