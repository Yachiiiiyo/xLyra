import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { AutomaticBackupFile, BackupInspectResult } from '@/features/settings/api/settings'
import { AutomaticRestoreDialog } from '@/features/settings/components/backup/automatic-restore-dialog'
import { findMiniNodes, mountMini, type MiniNode } from '@/test/mini-dom'
import common from '@/locales/zh/common.json'
import zhSettings from '@/locales/zh/settings.json'

const inspectAutomaticBackupFile = vi.fn()
vi.mock('@/features/settings/api/settings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/settings')>()
  return {
    ...actual,
    inspectAutomaticBackupFile: (...args: unknown[]) => inspectAutomaticBackupFile(...args),
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
  await i18n.init({
    lng: 'zh',
    resources: { zh: { settings: zhSettings, common } },
    interpolation: { escapeValue: false },
  })
})

function backupFile(): AutomaticBackupFile {
  return {
    key: 'xlyra/2025/backup.zip.xlyra',
    filename: 'backup.zip.xlyra',
    size: 1024,
    last_modified: '2025-01-01T00:00:00Z',
  } as AutomaticBackupFile
}

function inspectResult(overrides: Partial<BackupInspectResult['backup']> = {}): BackupInspectResult {
  return {
    backup: {
      format_version: 2,
      created_at: '2025-01-01T00:00:00Z',
      legacy: false,
      sections: ['settings'],
      impact: [],
      unavailable: [],
      ...overrides,
    },
  }
}

let client: QueryClient
async function renderDialog(file: AutomaticBackupFile | null, onConfirm: (file: AutomaticBackupFile) => void = vi.fn()) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return mountMini(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <AutomaticRestoreDialog file={file} onCancel={() => {}} onConfirm={onConfirm} />
      </I18nextProvider>
    </QueryClientProvider>,
  )
}

function confirmButton(container: MiniNode): MiniNode {
  const node = findMiniNodes(container, (candidate) => candidate.tagName === 'BUTTON' && candidate.textContent.includes('确认覆盖'))[0]
  if (!node) throw new Error('找不到确认按钮')
  return node
}

async function flush() {
  const { act } = await import('react')
  for (let index = 0; index < 3; index += 1) {
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
  }
}

describe('AutomaticRestoreDialog 预览确认', () => {
  it('关闭时不加载预览', async () => {
    const mounted = await renderDialog(null)
    try {
      await flush()
      expect(inspectAutomaticBackupFile).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('加载中显示加载态且确认禁用', async () => {
    let resolveInspect: (value: BackupInspectResult) => void = () => {}
    inspectAutomaticBackupFile.mockImplementationOnce(() => new Promise((resolve) => { resolveInspect = resolve }))
    const mounted = await renderDialog(backupFile())
    try {
      await flush()
      expect(inspectAutomaticBackupFile).toHaveBeenCalledOnce()
      expect(mounted.container.textContent).toContain('正在读取备份内容')
      expect(confirmButton(mounted.container).getAttribute('disabled')).toBeDefined()
      resolveInspect(inspectResult())
      await flush()
      expect(mounted.container.textContent).toContain('将恢复')
    } finally {
      await mounted.unmount()
    }
  })

  it('加载失败显示错误且确认禁用', async () => {
    inspectAutomaticBackupFile.mockRejectedValueOnce(new Error('S3 不可用'))
    const mounted = await renderDialog(backupFile())
    try {
      await flush()
      const text = mounted.container.textContent
      expect(text).toContain('无法读取备份内容')
      expect(text).toContain('S3 不可用')
      expect(confirmButton(mounted.container).getAttribute('disabled')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('加载成功显示预览，unavailable 非空时确认禁用', async () => {
    inspectAutomaticBackupFile.mockResolvedValueOnce(inspectResult({ unavailable: [{ id: 'agent_workspace', reason: 'missing' }] }))
    const onConfirm = vi.fn()
    const mounted = await renderDialog(backupFile(), onConfirm)
    try {
      await flush()
      expect(mounted.container.textContent).toContain('将恢复')
      const confirm = confirmButton(mounted.container)
      expect(confirm.getAttribute('disabled')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('确认后以原文件触发恢复', async () => {
    const file = backupFile()
    inspectAutomaticBackupFile.mockResolvedValueOnce(inspectResult())
    const onConfirm = vi.fn()
    const mounted = await renderDialog(file, onConfirm)
    try {
      await flush()
      const confirm = confirmButton(mounted.container)
      expect(confirm.getAttribute('disabled')).toBeNull()
      const { act } = await import('react')
      const { miniEvent } = await import('@/test/mini-dom')
      await act(async () => { confirm.dispatchEvent(miniEvent('click')) })
      expect(onConfirm).toHaveBeenCalledOnce()
      expect(onConfirm).toHaveBeenCalledWith(file)
    } finally {
      await mounted.unmount()
    }
  })
})
