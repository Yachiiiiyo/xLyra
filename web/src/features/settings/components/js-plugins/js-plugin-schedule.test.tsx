import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { pluginDeclaresSchedule, type JSPluginVersion } from '@/features/settings/api/js-plugins'
import translations from '@/locales/zh/settings.json'
import { JSPluginScheduleSection } from './js-plugin-schedule'

let mockScheduleData: unknown = {
  supported: true,
  enabled: true,
  every_minutes: 30,
  label: { zh: '自动签到保活' },
  next_tick_at: '2026-11-18T01:00:00Z',
}

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: mockScheduleData }),
  useMutation: () => ({ isPending: false, mutate: () => undefined }),
  useQueryClient: () => ({ invalidateQueries: async () => undefined }),
}))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({ lng: 'zh', resources: { zh: { settings: translations } }, interpolation: { escapeValue: false } })
})

describe('pluginDeclaresSchedule', () => {
  it('detects onSchedule export, schedule permission, or schedule manifest block', () => {
    expect(pluginDeclaresSchedule([])).toBe(false)
    expect(pluginDeclaresSchedule([{ manifest: { exports: ['probe'] } } as unknown as JSPluginVersion])).toBe(false)
    expect(pluginDeclaresSchedule([{ manifest: { exports: ['onSchedule'] } } as unknown as JSPluginVersion])).toBe(true)
    expect(pluginDeclaresSchedule([{ manifest: { permissions: ['schedule'] } } as unknown as JSPluginVersion])).toBe(true)
    expect(pluginDeclaresSchedule([{ manifest: { schedule: { everyMinutes: 60 } } } as unknown as JSPluginVersion])).toBe(true)
  })
})

describe('JSPluginScheduleSection', () => {
  it('renders custom schedule label and interval description when supported', () => {
    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <JSPluginScheduleSection pluginId="acme.schedule" />
      </I18nextProvider>,
    )
    expect(markup).toContain('定时计划')
    expect(markup).toContain('自动签到保活')
    expect(markup).toContain('每隔 30 分钟自动执行一次 onSchedule')
    expect(markup).toContain('下次执行时间')
  })

  it('renders nothing when schedule is not supported', () => {
    mockScheduleData = { supported: false, enabled: false }
    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <JSPluginScheduleSection pluginId="acme.none" />
      </I18nextProvider>,
    )
    expect(markup).toBe('')
  })
})
