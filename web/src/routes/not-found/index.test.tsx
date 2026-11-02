import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter } from 'react-router-dom'
import { beforeAll, describe, expect, it } from 'vitest'
import zh from '@/locales/zh/common.json'
import { NotFoundPage } from './index'

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    resources: { zh: { common: zh } },
    interpolation: { escapeValue: false },
  })
})

function render(initialEntry: string) {
  return renderToStaticMarkup(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <NotFoundPage />
      </MemoryRouter>
    </I18nextProvider>,
  )
}

describe('NotFoundPage', () => {
  it('全屏展示 404、标题与描述', () => {
    const markup = render('/missing-page')
    expect(markup).toContain('app-viewport')
    expect(markup).toContain('>404<')
    expect(markup).toContain('页面不存在')
    expect(markup).toContain('你访问的页面不存在')
  })

  it('渲染「返回首页」按钮并链接到 /', () => {
    const markup = render('/missing-page')
    expect(markup).toContain('返回首页')
    expect(markup).toContain('href="/"')
  })
})
