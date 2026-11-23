import { createInstance } from 'i18next'
import { renderToStaticMarkup } from 'react-dom/server'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { gatewayPipelineManifest } from '@/features/settings/api/js-plugins'
import { jsPluginErrorNotice } from '@/features/settings/components/js-plugins/js-plugin-errors'
import { isSecretUsePermission, permissionLabelKey } from '@/features/settings/components/js-plugins/permissions'
import translations from '@/locales/zh/settings.json'
import { JSPluginPipelineSection } from './js-plugin-pipeline'

const mockBindingsResponse = {
  items: [
    {
      id: 'b1',
      version: '1.0.0',
      name: '严格策略',
      inputs: [
        { name: 'apiKeys', entities: [{ type: 'api_key', id: 'k1', name: 'Prod Key' }] },
      ],
      host_options: { propagateSameValue: true },
      policy: { failurePolicy: 'fail_closed' },
      status: 'unavailable',
      unavailable_reason: 'plugin is disabled',
      created_at: '2026-11-23T00:00:00Z',
    },
  ],
  host_options: [
    { key: 'propagateSameValue', type: 'boolean', default: true },
  ],
}

vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: mockBindingsResponse, isLoading: false }),
  useQueries: () => [],
  useMutation: () => ({ isPending: false, mutate: () => undefined }),
  useQueryClient: () => ({ invalidateQueries: async () => undefined }),
}))

const i18n = createInstance()

beforeAll(async () => {
  await i18n.init({ lng: 'zh', resources: { zh: { settings: translations } }, interpolation: { escapeValue: false } })
})

describe('gatewayPipeline frontend helpers', () => {
  it('parses gatewayPipeline manifest section', () => {
    expect(gatewayPipelineManifest(undefined)).toBeNull()
    const parsed = gatewayPipelineManifest({
      gatewayPipeline: {
        scope: 'api_key',
        failurePolicy: 'fail_closed',
        inputs: [{ name: 'apiKeys', type: 'api_key', multiple: true }],
      },
    })
    expect(parsed?.scope).toBe('api_key')
    expect(parsed?.failurePolicy).toBe('fail_closed')
    expect(parsed?.inputs).toHaveLength(1)
  })

  it('maps payload.inspect_raw permission and gateway_pipeline in-use error', () => {
    expect(permissionLabelKey('payload.inspect_raw')).toBe('settings:jsPlugins.permission.payloadInspectRaw')
    expect(isSecretUsePermission('payload.inspect_raw')).toBe(true)
    expect(jsPluginErrorNotice('js_plugin_in_use', { scope: 'gateway_pipeline', count: 2 })).toEqual({
      key: 'settings:jsPlugins.errors.pipelineInUse',
      params: { count: 2 },
    })
  })
})

describe('JSPluginPipelineSection', () => {
  it('renders fail_closed warning and unavailable binding status even while disabled', () => {
    const markup = renderToStaticMarkup(
      <I18nextProvider i18n={i18n}>
        <JSPluginPipelineSection
          pluginId="acme.pipeline"
          enabled={false}
          manifest={{
            scope: 'api_key',
            failurePolicy: 'fail_closed',
            inputs: [{ name: 'apiKeys', type: 'api_key', multiple: true, title: { zh: 'API 密钥' } }],
          }}
        />
      </I18nextProvider>,
    )
    expect(markup).toContain('网关管线绑定')
    expect(markup).toContain('HTTP 503')
    expect(markup).toContain('严格策略')
    expect(markup).toContain('不可用')
    expect(markup).toContain('plugin is disabled')
    expect(markup).toContain('Prod Key')
  })
})
