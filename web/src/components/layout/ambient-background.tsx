import { useEffect, useRef } from 'react'
import { drawStarfield } from '@/lib/draw-starfield'

/**
 * 全局背景（L0）：一层很空的星空。绘制逻辑见 drawStarfield，
 * 只在挂载和窗口尺寸变化时画一次，之后没有任何逐帧开销。
 */

export function AmbientBackground() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const draw = () => drawStarfield(canvas)
    draw()
    let timer = 0
    const onResize = () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(draw, 120)
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.clearTimeout(timer)
      window.removeEventListener('resize', onResize)
    }
  }, [])

  return (
    <div className="ambient-background" aria-hidden="true">
      <canvas ref={canvasRef} className="size-full" />
    </div>
  )
}
