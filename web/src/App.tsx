import { Suspense } from 'react'
import { RouterProvider } from 'react-router-dom'
import { AppProviders } from '@/app/providers'
import { appRouter } from '@/app/router'
import { PwaUpdatePrompt } from '@/components/common/pwa-update-prompt'

export default function App() {
  return (
    <AppProviders>
      <PwaUpdatePrompt />
      <Suspense fallback={<AppFallback />}>
        <RouterProvider router={appRouter} />
      </Suspense>
    </AppProviders>
  )
}

function AppFallback() {
  // 启动阶段由全局遮罩覆盖，这里不再闪现 Loading 文案
  return null
}
