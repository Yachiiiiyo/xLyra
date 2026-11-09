import * as React from 'react'
import { Slot } from '@radix-ui/react-slot'
import { type VariantProps } from 'class-variance-authority'
import { LoaderCircle } from 'lucide-react'
import { useLiquidGlass } from '@/components/glass/use-liquid-glass'
import { buttonVariants } from '@/components/ui/button-variants'
import { cn } from '@/lib/utils'

interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
  /** 进行中：显示转圈并禁用点击（asChild 时不插入图标） */
  loading?: boolean
}

/**
 * 按钮。
 * - 胶囊形；高度取自 --control-h-*，移动端自动放大到 40 等触控尺寸。
 * - 不做 hover 上浮和投影；按下时轻微缩小。
 * - 强调色用在背景上：default（主按钮）是浅色底、深色字，每个视图最多一个。
 */
const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, loading = false, disabled, type = 'button', children, ...props }, ref) => {
    const innerRef = React.useRef<HTMLButtonElement>(null)
    React.useImperativeHandle(ref, () => innerRef.current as HTMLButtonElement)
    useLiquidGlass(innerRef, 'regular', variant === 'glass')

    const Comp = asChild ? Slot : 'button'

    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={innerRef}
        type={asChild ? undefined : type}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        {...props}
      >
        {/* Slot 只接受单个子元素，asChild 时不插入加载图标 */}
        {asChild ? (
          children
        ) : (
          <>
            {loading ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
            {children}
          </>
        )}
      </Comp>
    )
  },
)
Button.displayName = 'Button'

export { Button }
