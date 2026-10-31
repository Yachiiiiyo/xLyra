import { useEffect, type PropsWithChildren } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { AuthStatusScreen } from '@/components/auth/auth-status-screen'
import { readBootstrapInitialized } from '@/lib/bootstrap-state'
import { buildRedirectSearch, resolveAuthRedirectTarget } from '@/lib/navigation'
import { useAuthStore } from '@/stores/auth-store'

function useInitializeAuth() {
  const status = useAuthStore((state) => state.status)
  const initialize = useAuthStore((state) => state.initialize)

  useEffect(() => {
    if (status === 'idle') {
      void initialize()
    }
  }, [initialize, status])

  return initialize
}

export function ProtectedRoute({ children }: PropsWithChildren) {
  const location = useLocation()
  useInitializeAuth()
  const status = useAuthStore((state) => state.status)

  const pending = status === 'idle' || status === 'checking'
  const initializedHint = readBootstrapInitialized()

  // 鉴权失败（status === 'error'）由 RootLayout 整页接管，这里不再处理

  if (status === 'registration-required' || (pending && initializedHint === false)) {
    return <Navigate to={`/register?${buildRedirectSearch(location)}`} replace />
  }

  if (status === 'unauthenticated') {
    return <Navigate to={`/login?${buildRedirectSearch(location)}`} replace />
  }

  if (pending) {
    return null
  }

  return <>{children}</>
}

type PublicOnlyRouteProps = PropsWithChildren<{
  mode: 'login' | 'register'
}>

export function PublicOnlyRoute({ children, mode }: PublicOnlyRouteProps) {
  const location = useLocation()
  const { t } = useTranslation('components')
  useInitializeAuth()
  const status = useAuthStore((state) => state.status)
  const errorMessage = useAuthStore((state) => state.errorMessage)
  const errorStatus = useAuthStore((state) => state.errorStatus)

  const pending = status === 'idle' || status === 'checking'
  const initializedHint = readBootstrapInitialized()
  const registrationRequired = status === 'registration-required' || (pending && initializedHint === false)
  const sessionAnonymous = status === 'unauthenticated' || (pending && initializedHint === true)

  if (status === 'error') {
    return (
      <AuthStatusScreen
        state="error"
        title={t('authGuard.unavailableTitle')}
        description={errorMessage ?? t('authGuard.unavailableDesc')}
        statusCode={errorStatus}
      />
    )
  }

  if (status === 'authenticated') {
    return <Navigate to={resolveAuthRedirectTarget(location.search)} replace />
  }

  if (registrationRequired) {
    if (mode === 'register') return <>{children}</>
    return <Navigate to={`/register${location.search}`} replace />
  }

  if (sessionAnonymous) {
    if (mode === 'login') return <>{children}</>
    return <Navigate to={`/login${location.search}`} replace />
  }

  if (pending) return null

  return <>{children}</>
}
