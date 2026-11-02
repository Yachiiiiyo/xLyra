import type { ReactNode } from 'react'
import { AmbientBackground } from '@/components/layout/ambient-background'
import { AuthViewport } from '@/features/auth/components/auth-viewport'
import { APP_LOGO_SRC } from '@/lib/brand'

type StatusPageScreenProps = {
  code: string | number
  title: string
  description: string
  /** 状态码脚注，如「HTTP 502」；存在时作为徽标文字，避免状态码出现两次 */
  footnote?: string
  /** 操作区，放按钮 */
  action?: ReactNode
}

/** 全屏状态页（404 / 502 等）：不经过 AppShell，与登录页同一片夜空、同调性的居中玻璃卡片 */
export function StatusPageScreen({ code, title, description, footnote, action }: StatusPageScreenProps) {
  return (
    <AuthViewport>
      <AmbientBackground />
      {/* 内容层在背景之上；min-h-full 让内容超高时可滚动且不裁切两端 */}
      <main className="relative z-10 h-full overflow-y-auto">
        <div className="flex min-h-full items-center justify-center px-4 pb-[max(2.5rem,env(safe-area-inset-bottom,0px))] pt-[max(2.5rem,env(safe-area-inset-top,0px))]">
          <div className="w-full max-w-sm rounded-shell lg-thick p-6 text-center sm:p-8">
            {/* 品牌 logo：克制装饰，不做动画 */}
            <img src={APP_LOGO_SRC} alt="" className="mx-auto block size-12" />
            {/* 状态码徽标：有 footnote 时只显示 footnote，避免状态码信息重复 */}
            <p className="mx-auto mt-5 w-fit rounded-full bg-fill-3 px-3 py-1 font-mono text-footnote tabular-nums text-label-2">
              {footnote ?? code}
            </p>
            <h1 className="mt-3 text-title-2 text-label">{title}</h1>
            <p className="mt-2 text-balance text-subhead text-label-2">{description}</p>
            {action ? <div className="mt-6">{action}</div> : null}
          </div>
        </div>
      </main>
    </AuthViewport>
  )
}
