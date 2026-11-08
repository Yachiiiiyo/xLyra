import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it } from 'vitest'
import type { BackupInspectResult } from '@/features/settings/api/settings'
import { RestorePreview } from '@/features/settings/components/backup/restore-preview'
import { mountMini } from '@/test/mini-dom'
import zhSettings from '@/locales/zh/settings.json'

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'settings', resources: { zh: { settings: zhSettings } } })
})

function inspectResult(overrides: Partial<BackupInspectResult['backup']>): BackupInspectResult {
  return {
    backup: {
      format_version: 2,
      created_at: '2025-01-01T00:00:00Z',
      legacy: false,
      sections: [],
      impact: [],
      unavailable: [],
      ...overrides,
    },
  }
}

async function renderPreview(result: BackupInspectResult) {
  return mountMini(
    <I18nextProvider i18n={i18n}>
      <RestorePreview result={result} />
    </I18nextProvider>,
  )
}

describe('RestorePreview', () => {
  it('展示备份时间、格式版本与「将恢复」类别', async () => {
    const mounted = await renderPreview(inspectResult({ sections: ['settings', 'gateway_sites'] }))
    try {
      const text = mounted.container.textContent
      expect(text).toContain('备份时间')
      expect(text).toContain('格式版本')
      expect(text).toContain('v2')
      expect(text).toContain('将恢复')
      expect(text).toContain('系统设置')
      expect(text).toContain('站点与模型')
      expect(text).not.toContain('将清空')
      expect(text).not.toContain('无法恢复')
    } finally {
      await mounted.unmount()
    }
  })

  it('legacy 备份显示旧版说明', async () => {
    const mounted = await renderPreview(inspectResult({ legacy: true, sections: ['settings'] }))
    try {
      expect(mounted.container.textContent).toContain('这是旧版备份，按旧版全量内容恢复')
    } finally {
      await mounted.unmount()
    }
  })

  it('impact 非空时显示「将清空」警示块，partial 项追加说明', async () => {
    const mounted = await renderPreview(inspectResult({
      sections: ['settings'],
      impact: [{ id: 'gateway_keys', partial: true }, { id: 'gateway_logs', partial: false }],
    }))
    try {
      const text = mounted.container.textContent
      expect(text).toContain('恢复后，以下现有数据会被清空')
      expect(text).toContain('API 密钥与权限（部分数据）')
      expect(text).toContain('请求日志与用量')
      expect(text).toContain('这些数据依赖被恢复的内容，无法单独保留')
    } finally {
      await mounted.unmount()
    }
  })

  it('unavailable 非空时显示「无法恢复」块与原因', async () => {
    const mounted = await renderPreview(inspectResult({
      sections: ['settings'],
      unavailable: [{ id: 'agent_workspace', reason: 'workspace 目录不存在' }],
    }))
    try {
      const text = mounted.container.textContent
      expect(text).toContain('当前实例无法恢复以下内容，需先处理后才能继续')
      expect(text).toContain('Agent 工作区')
      expect(text).toContain('workspace 目录不存在')
    } finally {
      await mounted.unmount()
    }
  })

  it('未知类别回退显示 id', async () => {
    const mounted = await renderPreview(inspectResult({ sections: ['mystery_section'] }))
    try {
      expect(mounted.container.textContent).toContain('mystery_section')
    } finally {
      await mounted.unmount()
    }
  })
})
