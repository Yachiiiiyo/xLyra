import { useState, type ReactNode } from 'react'
import * as PopoverPrimitive from '@radix-ui/react-popover'
import { ChevronDown, LoaderCircle, type LucideIcon } from 'lucide-react'
import { buttonVariants } from '@/components/ui/button-variants'
import { cn } from '@/lib/utils'

export type SplitButtonItem = {
  key: string
  label: string
  icon?: LucideIcon
  onSelect: () => void
  disabled?: boolean
}

const mainPadding = {
  default: 'px-4',
  sm: 'px-3',
  lg: 'px-5',
} as const

const chevronWidth = {
  default: 'w-[var(--control-h)]',
  sm: 'w-[var(--control-h-sm)]',
  lg: 'w-[var(--control-h-lg)]',
} as const

type SplitSize = keyof typeof mainPadding

/** 胶囊拆分按钮：左侧主操作，右侧下拉。两段共用同一套 variant / size。 */
export function SplitButton({
  children,
  onClick,
  items,
  menuLabel,
  variant = 'default',
  size = 'default',
  loading = false,
  disabled = false,
  side = 'bottom',
  align = 'end',
  className,
  title,
}: {
  children: ReactNode
  onClick: () => void
  items: SplitButtonItem[]
  /** 下拉三角的无障碍名称 */
  menuLabel: string
  variant?: 'default' | 'secondary'
  size?: SplitSize
  loading?: boolean
  disabled?: boolean
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  className?: string
  title?: string
}) {
  const [open, setOpen] = useState(false)
  const inactive = disabled || loading
  const tip = title || undefined

  return (
    <div
      className={cn(
        buttonVariants({ variant, size }),
        'shrink items-stretch gap-0 overflow-hidden p-0',
        inactive && 'opacity-40',
        className,
      )}
      title={tip}
      aria-busy={loading || undefined}
    >
      <button
        type="button"
        className={cn(
          'inline-flex h-full min-w-0 flex-1 items-center justify-center gap-2 truncate bg-transparent text-inherit outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-current disabled:cursor-not-allowed',
          mainPadding[size],
        )}
        disabled={inactive}
        title={tip}
        onClick={onClick}
      >
        {loading ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
        {children}
      </button>
      <span aria-hidden="true" className="w-px shrink-0 self-stretch bg-current opacity-25" />
      <PopoverPrimitive.Root
        open={inactive ? false : open}
        onOpenChange={(next) => {
          if (inactive) return
          setOpen(next)
        }}
      >
        <PopoverPrimitive.Trigger asChild>
          <button
            type="button"
            className={cn(
              'inline-flex h-full shrink-0 items-center justify-center bg-transparent text-inherit outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-current disabled:cursor-not-allowed',
              chevronWidth[size],
            )}
            aria-label={menuLabel}
            aria-haspopup="menu"
            aria-expanded={inactive ? false : open}
            disabled={inactive}
            title={tip}
          >
            <ChevronDown aria-hidden="true" />
          </button>
        </PopoverPrimitive.Trigger>
        <PopoverPrimitive.Portal>
          <PopoverPrimitive.Content
            side={side}
            align={align}
            sideOffset={6}
            collisionPadding={16}
            onOpenAutoFocus={(event) => event.preventDefault()}
            className="z-[120] min-w-32 overflow-hidden lg-regular rounded-inner py-1"
          >
            {items.map((item) => (
              <button
                key={item.key}
                type="button"
                disabled={item.disabled || inactive}
                onClick={() => {
                  setOpen(false)
                  item.onSelect()
                }}
                className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-body font-medium text-label transition-colors hover:bg-fill-3 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {item.icon ? <item.icon className="size-4 shrink-0" aria-hidden="true" /> : null}
                {item.label}
              </button>
            ))}
          </PopoverPrimitive.Content>
        </PopoverPrimitive.Portal>
      </PopoverPrimitive.Root>
    </div>
  )
}
