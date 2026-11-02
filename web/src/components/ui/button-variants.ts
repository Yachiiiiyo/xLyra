import { cva } from 'class-variance-authority'

/** 按钮的颜色和尺寸。SplitButton 与 Button 共用这一份，避免两套胶囊色。 */
export const buttonVariants = cva(
  'inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-full font-medium select-none transition-[transform,background-color,color,border-color,filter,opacity] duration-150 ease-out focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-tint/55 focus-visible:ring-offset-2 focus-visible:ring-offset-canvas active:scale-[0.97] disabled:pointer-events-none disabled:opacity-40 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        /** 主按钮：最可能执行的操作 */
        default: 'bg-tint text-tint-foreground hover:brightness-[1.06] active:brightness-95',
        /** 次要按钮：填充色底 */
        secondary: 'bg-fill-2 text-label hover:bg-fill-1',
        /** 描边按钮：与次要按钮同级，用于需要更轻视觉的并列操作 */
        outline: 'border border-white/14 bg-transparent text-label hover:bg-fill-4',
        /** 纯文字按钮：工具栏、列表行内、取消 */
        ghost: 'bg-transparent text-label hover:bg-fill-4',
        /** 破坏性操作的最终确认（确认弹窗中）：红色实底 */
        destructive: 'bg-danger text-white hover:brightness-110 active:brightness-95',
        /** 普通位置的破坏性操作：红字 + 浅红底，不抢主按钮的视觉 */
        'destructive-soft': 'bg-danger/14 text-danger hover:bg-danger/22',
        /** 功能层浮动按钮：Liquid Glass，放在工具栏或浮于内容之上 */
        glass: 'lg-regular text-label hover:brightness-110',
      },
      size: {
        default: 'h-[var(--control-h)] px-4 text-body [&_svg]:size-4',
        sm: 'h-[var(--control-h-sm)] px-3 text-callout [&_svg]:size-3.5',
        lg: 'h-[var(--control-h-lg)] px-5 text-body [&_svg]:size-[18px]',
        icon: 'size-[var(--control-h)] [&_svg]:size-[18px]',
        'icon-sm': 'size-[var(--control-h-sm)] [&_svg]:size-4',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)
