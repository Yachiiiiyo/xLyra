import { useLocation } from 'react-router-dom'
import { AmbientBackground } from '@/components/layout/ambient-background'
import { useAppearanceQuery } from '@/features/appearance/api/appearance'

/**
 * 全局背景（L0）：配置了实例背景图时整层铺满视口，取代星点背景；
 * 未配置 / 加载中 / 加载失败一律回退星点背景，不报错、不阻塞。
 *
 * 必须作为 `.app-viewport` 的直接子元素渲染（与 AmbientBackground 相同的定位方式），
 * 且它与上层 LiquidGlass 面板之间不能出现新的 backdrop root（filter / opacity<1 /
 * transform / isolation / will-change 等都会让 backdrop-filter 只能看到该祖先内部），
 * 否则玻璃折射不到这层背景。非 /agent 页面在图片上再叠一层模糊压暗遮罩保证正文可读；
 * 遮罩层用 backdrop-filter 没有这个问题，因为它不是内容的祖先。
 */
export function AppBackground({ mainContentOverlay = false }: { mainContentOverlay?: boolean }) {
  const { data } = useAppearanceQuery()
  const location = useLocation()
  const image = data?.background_image?.trim() || null
  const isAgentWorkspace = location.pathname === '/agent' || location.pathname.startsWith('/agent/')

  if (!image) {
    return <AmbientBackground />
  }

  return (
    <>
      <div
        className="app-background-image"
        data-liquid-glass-background="true"
        aria-hidden="true"
        style={{ backgroundImage: `url("${image}")` }}
      />
      {isAgentWorkspace ? null : (
        <div
          className={`app-background-overlay${mainContentOverlay ? ' app-background-overlay-main' : ''}`}
          aria-hidden="true"
        />
      )}
    </>
  )
}
