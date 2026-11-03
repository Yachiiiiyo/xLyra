import type { CSSProperties } from 'react'

/**
 * Agent 工作台的自定义背景层（预留，本期不接任何 UI / 接口）。
 *
 * `image` 为空时什么都不渲染，页面露出系统的全局星点背景；有值时铺满视口。
 * 必须放在工作台最底层（fixed、低于所有内容、pointer-events:none），且它与上层
 * LiquidGlass 面板之间不能出现新的 backdrop root（filter / opacity<1 / transform /
 * isolation / will-change 等都会让 backdrop-filter 只能看到该祖先内部的内容），
 * 否则玻璃折射不到这层背景。因此它作为工作台根元素的直接子元素渲染，
 * 与面板之间只隔普通的布局容器。
 */
export function AgentBackdrop({
  image = null,
  dim = 0.35,
  blur = 0,
}: {
  image?: string | null
  /** 0–1：叠在背景图上的黑色遮罩浓度 */
  dim?: number
  /** 背景图高斯模糊（px），0 为不模糊 */
  blur?: number
}) {
  if (!image) return null
  const style: CSSProperties = {
    backgroundImage: `url("${image}")`,
    backgroundSize: 'cover',
    backgroundPosition: 'center',
    ...(blur > 0 ? { filter: `blur(${blur}px)` } : null),
  }
  const dimOpacity = Math.max(0, Math.min(1, dim))
  return (
    <div aria-hidden="true" data-agent-backdrop="true" className="pointer-events-none fixed inset-0 z-0" style={style}>
      {dimOpacity > 0 ? <div className="absolute inset-0 bg-black" style={{ opacity: dimOpacity }} /> : null}
    </div>
  )
}
