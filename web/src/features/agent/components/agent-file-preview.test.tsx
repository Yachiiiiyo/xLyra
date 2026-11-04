import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { AgentFilePreview } from '@/features/agent/components/agent-file-preview'
import zhAgent from '@/locales/zh/agent.json'

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: { kind: 'text', text: '<html><head></head><body>ok</body></html>' }, isPending: false, isError: false }),
}))
vi.mock('@/features/agent/api/agent', () => ({
  fetchSessionFile: vi.fn(),
  downloadSessionFile: vi.fn(),
  sessionFileErrorKind: vi.fn(),
}))

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'agent', resources: { zh: { agent: zhAgent } } })
})

describe('AgentFilePreview HTML', () => {
  it('使用仅 allow-scripts 的沙箱并在 srcdoc 首个标签注入 CSP', () => {
    const html = renderToStaticMarkup(<I18nextProvider i18n={i18n}><AgentFilePreview sessionId="session-1" path="page.html" /></I18nextProvider>)
    expect(html).toContain('sandbox="allow-scripts"')
    expect(html).toContain('srcDoc="&lt;html&gt;&lt;head&gt;&lt;meta http-equiv=&quot;Content-Security-Policy&quot;')
    expect(html).not.toContain('allow-same-origin')
  })
})
