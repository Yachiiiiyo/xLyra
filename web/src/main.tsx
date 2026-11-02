import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { installAppViewportSize } from '@/lib/app-viewport'
import { installGlassRefraction } from '@/components/glass/refraction'
import { dismissBootSplash, startBootSplash, waitBootSplashReady } from '@/lib/boot-splash'
import { useAuthStore } from '@/stores/auth-store'
import { appRouter } from '@/app/router'
import { i18nReady } from './locales/i18n'
import './index.css'
import App from './App'

startBootSplash()
installAppViewportSize()
installGlassRefraction()
// 主题已固定为深色，清理旧版主题切换留下的本地存储
try {
  window.localStorage.removeItem('xlyra-theme')
} catch {
  // 存储不可用时无需处理
}
void useAuthStore.getState().initialize()

i18nReady.then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )

  // 启动就绪：鉴权已判定、首屏路由（含未登录重定向）加载完成后再收起遮罩
  void waitBootSplashReady({
    isReady: () => {
      const { status } = useAuthStore.getState()
      if (status === 'idle' || status === 'checking') return false
      return appRouter.state.initialized && appRouter.state.navigation.state === 'idle'
    },
    subscribe: (notify) => {
      const unsubscribeAuth = useAuthStore.subscribe(notify)
      const unsubscribeRouter = appRouter.subscribe(notify)
      return () => {
        unsubscribeAuth()
        unsubscribeRouter()
      }
    },
  }).then(dismissBootSplash)
})
