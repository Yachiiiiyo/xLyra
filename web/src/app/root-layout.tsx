import { Outlet } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ProtectedRoute } from '@/components/auth/auth-guard'
import { inferStatusCode } from '@/components/auth/auth-utils'
import { StatusPageScreen } from '@/components/common/status-page-screen'
import { AppShell } from '@/components/layout/app-shell'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth-store'

export function RootLayout() {
  const status = useAuthStore((state) => state.status)
  const errorMessage = useAuthStore((state) => state.errorMessage)
  const errorStatus = useAuthStore((state) => state.errorStatus)

  // 鉴权失败（如后端 502）时整页换成全屏错误页，不再渲染 AppShell。
  // 初始化由下面 ProtectedRoute 首次渲染触发；status 还是 idle/checking 时不会进这个分支，顺序安全。
  if (status === 'error') {
    return <ServiceErrorScreen description={errorMessage} statusCode={errorStatus} />
  }

  return (
    <AppShell>
      <ProtectedRoute>
        <Outlet />
      </ProtectedRoute>
    </AppShell>
  )
}

function ServiceErrorScreen({
  description,
  statusCode,
}: {
  description: string | null
  statusCode: number | null
}) {
  const { t } = useTranslation('components')
  const displayStatusCode = statusCode ?? inferStatusCode(description ?? '') ?? 502

  return (
    <StatusPageScreen
      code={displayStatusCode}
      title={t('authGuard.serviceErrorTitle')}
      description={t('authGuard.serviceErrorDesc')}
      footnote={`HTTP ${displayStatusCode}`}
      action={
        <Button size="lg" className="w-full" onClick={() => window.location.reload()}>
          {t('authGuard.refresh')}
        </Button>
      }
    />
  )
}
