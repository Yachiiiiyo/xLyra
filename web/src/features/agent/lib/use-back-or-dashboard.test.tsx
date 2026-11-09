import { afterEach, describe, expect, it, vi } from 'vitest'
import { useBackOrDashboard } from './use-back-or-dashboard'
import { mountMini } from '@/test/mini-dom'

const navigate = vi.hoisted(() => vi.fn())

vi.mock('react-router-dom', () => ({ useNavigate: () => navigate }))

function Harness({ onValue }: { onValue: (back: () => void) => void }) {
  onValue(useBackOrDashboard())
  return null
}

async function setup(idx: number) {
  vi.stubGlobal('history', { state: { idx } })
  let back: (() => void) | undefined
  const mounted = await mountMini(<Harness onValue={(value) => { back = value }} />)
  if (!back) throw new Error('hook has not rendered')
  return { back, mounted }
}

afterEach(() => {
  navigate.mockReset()
  vi.unstubAllGlobals()
})

describe('useBackOrDashboard', () => {
  it('存在应用内上一页时返回上一页', async () => {
    const hook = await setup(1)
    try {
      hook.back()
      expect(navigate).toHaveBeenCalledExactlyOnceWith(-1)
    } finally {
      await hook.mounted.unmount()
    }
  })

  it('没有应用内上一页时替换为默认仪表盘', async () => {
    const hook = await setup(0)
    try {
      hook.back()
      expect(navigate).toHaveBeenCalledExactlyOnceWith('/dashboard', { replace: true })
    } finally {
      await hook.mounted.unmount()
    }
  })
})
