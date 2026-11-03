/**
 * Agent 会话内的「dull」遮罩：进入有消息的会话且配置了全局背景图时，
 * 由 AgentWorkspace 渲染在背景图之上、面板之下——高斯模糊 + 半透明压暗，
 * 隐约透出底层背景图，正文不被打扰（数值沿用旧版 --dull）。
 * 无背景图时由调用方不渲染，避免影响全局星点背景；背景图本身由壳层的
 * AppBackground 负责铺，这里不再渲染图片层。
 *
 * 必须放在工作台最底层（低于所有内容、pointer-events:none），且它与上层
 * LiquidGlass 面板之间不能出现新的 backdrop root（filter / opacity<1 / transform /
 * isolation / will-change 等都会让 backdrop-filter 只能看到该祖先内部的内容），
 * 否则玻璃折射不到这层之下的背景。因此它作为工作台根元素的直接子元素渲染，
 * 与面板之间只隔普通的布局容器。
 */
export function AgentBackdrop() {
  return (
    <div
      aria-hidden="true"
      data-agent-backdrop="true"
      className="agent-backdrop-dull pointer-events-none absolute inset-0 z-0"
    />
  )
}
