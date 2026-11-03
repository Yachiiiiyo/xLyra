import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { SkillEditor } from './agent-settings-dialog'
import { toast } from '@/lib/toast'
import zhAgent from '@/locales/zh/agent.json'

const mutation = vi.hoisted(() => ({
  onSuccess: undefined as undefined | (() => Promise<void>),
  invalidateQueries: vi.fn(),
}))

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: { editable: true, content: 'Skill body' }, isLoading: false }),
  useQueryClient: () => ({ invalidateQueries: mutation.invalidateQueries, removeQueries: vi.fn() }),
  useMutation: (options: { onSuccess: () => Promise<void> }) => {
    if (options.onSuccess.toString().includes('skillSaved')) mutation.onSuccess = options.onSuccess
    return { mutate: vi.fn(), isPending: false }
  },
}))
vi.mock('@/hooks/use-media-query', () => ({ useMobileLayout: () => true }))
vi.mock('@/lib/toast', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({
    lng: 'zh',
    defaultNS: 'agent',
    resources: { zh: { agent: zhAgent } },
  })
})

beforeEach(() => {
  mutation.onSuccess = undefined
  mutation.invalidateQueries.mockReset().mockResolvedValue(undefined)
})

describe('Agent 移动端 Skills 保存', () => {
  it('保存成功后保留当前编辑页并提示成功', async () => {
    const onBack = vi.fn()
    const onSaved = vi.fn()
    renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <SkillEditor
          name="demo"
          existingNames={['demo']}
          onBack={onBack}
          onSaved={onSaved}
          onActionsChange={() => {}}
        />
      </I18nextProvider>,
    )

    await mutation.onSuccess?.()

    expect(onBack).not.toHaveBeenCalled()
    expect(onSaved).not.toHaveBeenCalled()
    expect(mutation.invalidateQueries).toHaveBeenCalled()
    expect(toast.success).toHaveBeenCalledWith(zhAgent.settings.skillSaved)
  })
})
