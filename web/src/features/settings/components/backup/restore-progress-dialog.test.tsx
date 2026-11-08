import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RestoreProgressEvent } from '@/features/settings/api/settings'
import { RestoreProgressDialog, type RestoreTask } from '@/features/settings/components/backup/restore-progress-dialog'
import { findMiniNodes, mountMini, type MiniNode } from '@/test/mini-dom'
import zhSettings from '@/locales/zh/settings.json'

const importBackupSSE = vi.fn()
vi.mock('@/features/settings/api/settings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/settings')>()
  return {
    ...actual,
    importBackupSSE: (...args: unknown[]) => importBackupSSE(...args),
    startAutomaticBackupRestore: vi.fn(),
    fetchAutomaticBackupRestoreTask: vi.fn(),
    cancelAutomaticBackupRestoreTask: vi.fn(),
  }
})

vi.mock('@/components/ui/dialog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/components/ui/dialog')>()
  const { createElement, Fragment } = await import('react')
  const pass = (tag: string) => ({ children, ...props }: { children?: unknown }) =>
    createElement(tag, props, children as never)
  return {
    ...actual,
    Dialog: ({ open, children }: { open?: boolean; children?: unknown }) =>
      (open ? createElement(Fragment, null, children as never) : null),
    DialogContent: pass('div'),
    DialogTitle: pass('h2'),
    DialogDescription: pass('p'),
  }
})

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'settings', resources: { zh: { settings: zhSettings } } })
})

const reload = vi.fn()
beforeAll(() => {
  vi.stubGlobal('location', { reload })
  vi.stubGlobal('requestAnimationFrame', () => 0)
  vi.stubGlobal('cancelAnimationFrame', () => {})
})

beforeEach(() => {
  importBackupSSE.mockReset()
  reload.mockReset()
})

const testFile = new File(['x'], 'backup.xlyra', { type: 'application/vnd.xlyra.backup' })

async function renderTask(task: RestoreTask) {
  return mountMini(
    <I18nextProvider i18n={i18n}>
      <RestoreProgressDialog task={task} onClose={() => {}} onBackground={() => {}} />
    </I18nextProvider>,
  )
}

function stepRow(container: MiniNode, label: string): MiniNode | null {
  const labelNode = findMiniNodes(container, (candidate) => candidate.tagName === 'SPAN' && candidate.textContent === label)[0]
  return labelNode?.parentNode ?? null
}

async function flush(rounds = 6) {
  const { act } = await import('react')
  for (let index = 0; index < rounds; index += 1) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  }
}

describe('RestoreProgressDialog', () => {
  it('手动恢复：未出现的步骤不显示，出现的 agent 步骤在完成后保持完成', async () => {
    const events: RestoreProgressEvent[] = [
      { step: 'decrypt', status: 'in_progress' },
      { step: 'parse', status: 'complete' },
      { step: 'agent', status: 'in_progress' },
      { step: 'agent', status: 'complete' },
      {
        step: 'complete',
        status: 'complete',
        summary: {
          tables: 3,
          rows: 0,
          config_keys: 5,
          format_version: 2,
          sections: ['settings', 'agent_config'],
          cleared_sections: [{ id: 'gateway_keys', partial: true }],
        },
      },
    ]
    importBackupSSE.mockImplementationOnce(async function* () {
      yield* events
    })
    const mounted = await renderTask({ type: 'manual', file: testFile, passphrase: 'secret' })
    try {
      await flush()
      const text = mounted.container.textContent
      // download/import 从未出现，不显示
      expect(stepRow(mounted.container, '下载备份文件')).toBeNull()
      expect(stepRow(mounted.container, '写入备份数据')).toBeNull()
      // 出现过的步骤保持显示且已完成
      expect(stepRow(mounted.container, '解密备份文件')?.textContent).toContain('已完成')
      expect(stepRow(mounted.container, '写入 Agent 配置')?.textContent).toContain('已完成')
      // 摘要含 sections 时按类别数量描述
      expect(text).toContain('已恢复 2 个内容类别，数据库 3 张表 / 0 行。')
      // cleared_sections 追加清空提示
      expect(text).toContain('已清空依赖数据：API 密钥与权限')
      expect(reload).toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('手动恢复：旧后端无 sections 时沿用原完成描述', async () => {
    importBackupSSE.mockImplementationOnce(async function* () {
      yield { step: 'decrypt', status: 'in_progress' }
      yield { step: 'decrypt', status: 'complete' }
      yield { step: 'parse', status: 'complete' }
      yield { step: 'import', status: 'complete' }
      yield { step: 'complete', status: 'complete', summary: { tables: 2, rows: 10, config_keys: 0, format_version: 1 } }
    })
    const mounted = await renderTask({ type: 'manual', file: testFile, passphrase: 'secret' })
    try {
      await flush()
      expect(mounted.container.textContent).toContain('成功导入 2 张表，共 10 行数据。')
      expect(stepRow(mounted.container, '写入备份数据')?.textContent).toContain('已完成')
      expect(mounted.container.textContent).not.toContain('已清空依赖数据')
    } finally {
      await mounted.unmount()
    }
  })

  it('手动恢复失败：显示错误步骤与信息', async () => {
    importBackupSSE.mockImplementationOnce(async function* () {
      yield { step: 'decrypt', status: 'in_progress' }
      yield { step: 'decrypt', status: 'error', message: '口令错误' }
    })
    const mounted = await renderTask({ type: 'manual', file: testFile, passphrase: 'wrong' })
    try {
      await flush()
      const text = mounted.container.textContent
      expect(text).toContain('还原失败')
      expect(text).toContain('口令错误')
      expect(stepRow(mounted.container, '解密备份文件')?.textContent).toContain('失败')
      expect(reload).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })
})
