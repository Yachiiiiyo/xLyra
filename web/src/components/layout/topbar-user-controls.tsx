import { LiquidGlass } from '@/components/glass/liquid-glass'
import { Skeleton } from '@/components/ui/skeleton'
import { LanguageSwitcher } from '@/components/common/language-switcher'
import { CurrentAccountAvatar } from '@/components/layout/user-account-menu'
import { useAuthStore } from '@/stores/auth-store'

/** 顶栏右侧的玻璃控件组：语言切换 + 头像与当前账户名 */
export function TopbarUserControls() {
  const user = useAuthStore((state) => state.user)
  const authStatus = useAuthStore((state) => state.status)
  const isAuthChecking = !user && (authStatus === 'idle' || authStatus === 'checking')

  return (
    <LiquidGlass className="pointer-events-auto flex items-center gap-1 rounded-full p-1">
      <LanguageSwitcher />
      <span className="hidden h-4 w-px bg-separator xl:block" />
      <CurrentAccountAvatar className="size-6" />
      <span className="hidden max-w-48 truncate pl-1 pr-3 text-callout text-label-2 xl:block">
        {isAuthChecking ? <Skeleton className="h-3 w-24 rounded-full" /> : user?.displayName ?? user?.username ?? 'Authenticated admin'}
      </span>
    </LiquidGlass>
  )
}
