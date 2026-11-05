import { Navigate } from 'react-router-dom'
import { useAgentAvailability } from '@/features/agent/lib/use-agent-availability'
import { useAppearanceQuery } from '@/features/appearance/api/appearance'

/** 程序首页：按外观配置跳转 /dashboard 或 /agent；加载中不渲染避免闪跳，失败按 dashboard，agent 不可用回退 dashboard */
export function HomeRedirect() {
  const agentAvailable = useAgentAvailability()
  const appearanceQuery = useAppearanceQuery()
  if (appearanceQuery.isLoading) return null
  const homePage = appearanceQuery.data?.home_page ?? 'dashboard'
  const target = homePage === 'agent' && agentAvailable ? '/agent' : '/dashboard'
  return <Navigate to={target} replace />
}
