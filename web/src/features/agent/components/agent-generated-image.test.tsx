import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentGeneratedImage } from '@/features/agent/components/agent-generated-image'
import { apiFetchBlob } from '@/lib/http'
import zhAgent from '@/locales/zh/agent.json'

vi.mock('@/lib/http', () => ({ apiFetchBlob: vi.fn() }))
vi.mock('@tanstack/react-query', () => ({
  useQuery: (options: { queryFn: () => Promise<unknown> }) => {
    void options.queryFn().catch(() => {})
    return { data: undefined, isError: true }
  },
}))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

describe('AgentGeneratedImage', () => {
  it('404 时显示文件缺失占位而不是破图', async () => {
    vi.mocked(apiFetchBlob).mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }))
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentGeneratedImage sessionId="session-1" path="images/a.png" /></I18nextProvider>)
    await Promise.resolve()
    expect(apiFetchBlob).toHaveBeenCalledExactlyOnceWith('/api/v1/agent/sessions/session-1/files?path=images%2Fa.png')
    expect(html).toContain(zhAgent.chat.image.missing)
    expect(html).not.toContain('<img')
  })
})
