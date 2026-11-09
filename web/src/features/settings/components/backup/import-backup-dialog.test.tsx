import { act } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { BackupInspectResult } from '@/features/settings/api/settings'
import { ImportBackupDialog } from '@/features/settings/components/backup/import-backup-dialog'
import { findMiniNodes, miniEvent, mountMini, type MiniEvent, type MiniNode } from '@/test/mini-dom'
import common from '@/locales/zh/common.json'
import zhSettings from '@/locales/zh/settings.json'

const inspectBackupFile = vi.fn()
vi.mock('@/features/settings/api/settings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/settings/api/settings')>()
  return {
    ...actual,
    inspectBackupFile: (...args: unknown[]) => inspectBackupFile(...args),
  }
})

const toastError = vi.fn()
vi.mock('@/lib/toast', () => ({
  toast: { success: vi.fn(), error: (...args: unknown[]) => toastError(...args) },
}))

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

const testFile = new File(['x'], 'backup.xlyra', { type: 'application/vnd.xlyra.backup' })

let client: QueryClient
async function renderDialog(onConfirm: (file: File, passphrase: string) => void = vi.fn(), disabled = false) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return mountMini(
    <QueryClientProvider client={client}>
      <I18nextProvider i18n={i18n}>
        <ImportBackupDialog open onClose={() => {}} onConfirm={onConfirm} disabled={disabled} />
      </I18nextProvider>
    </QueryClientProvider>,
  )
}

function dropzone(container: MiniNode): MiniNode {
  const node = findMiniNodes(container, (candidate) => candidate.getAttribute('class')?.includes('border-dashed'))[0]
  if (!node) throw new Error('找不到拖放区域')
  return node
}

function button(container: MiniNode, label: string): MiniNode {
  const node = findMiniNodes(container, (candidate) => candidate.tagName === 'BUTTON' && candidate.textContent.includes(label))[0]
  if (!node) throw new Error(`找不到按钮: ${label}`)
  return node
}

function passphraseInput(container: MiniNode): MiniNode {
  const node = findMiniNodes(container, (candidate) => candidate.tagName === 'INPUT' && candidate.getAttribute('autoComplete') === 'current-password')[0]
  if (!node) throw new Error('找不到口令输入框')
  return node
}

async function click(node: MiniNode) {
  await act(async () => { node.dispatchEvent(miniEvent('click')) })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

async function dropFile(node: MiniNode, file: File) {
  const event = { ...miniEvent('drop'), dataTransfer: { files: { item: () => file } } } as unknown as MiniEvent
  await act(async () => { node.dispatchEvent(event) })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

async function typeInto(node: MiniNode, value: string) {
  await act(async () => {
    node.dispatchEvent(miniEvent('focusin', { target: node }))
  })
  const prototypeSetter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set
  prototypeSetter?.call(node, value)
  await act(async () => {
    node.dispatchEvent(miniEvent('input', { target: node }))
    node.dispatchEvent(miniEvent('change', { target: node }))
  })
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)) })
}

describe('ImportBackupDialog 两步导入', () => {
  it('第 1 步缺文件/口令时提示且不调用预览', async () => {
    const mounted = await renderDialog()
    try {
      await click(button(mounted.container, '下一步：预览'))
      expect(toastError).toHaveBeenCalledWith('请选择备份文件')
      expect(inspectBackupFile).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('预览失败时 toast 提示并停留在第 1 步', async () => {
    inspectBackupFile.mockRejectedValueOnce(new Error('口令错误'))
    const mounted = await renderDialog()
    try {
      await dropFile(dropzone(mounted.container), testFile)
      await typeInto(passphraseInput(mounted.container), 'secret')
      await click(button(mounted.container, '下一步：预览'))
      expect(inspectBackupFile).toHaveBeenCalledOnce()
      expect(toastError).toHaveBeenCalledWith('无法读取备份内容', { description: '口令错误' })
      expect(mounted.container.textContent).toContain('拖入文件，或点击选择')
      expect(mounted.container.textContent).not.toContain('将恢复')
    } finally {
      await mounted.unmount()
    }
  })

  it('预览成功后进入第 2 步，「返回」可回到第 1 步', async () => {
    inspectBackupFile.mockResolvedValueOnce(inspectResult())
    const mounted = await renderDialog()
    try {
      await dropFile(dropzone(mounted.container), testFile)
      await typeInto(passphraseInput(mounted.container), 'secret')
      await click(button(mounted.container, '下一步：预览'))
      expect(mounted.container.textContent).toContain('将恢复')
      expect(mounted.container.textContent).toContain('系统设置')
      await click(button(mounted.container, '返回'))
      expect(mounted.container.textContent).toContain('拖入文件，或点击选择')
    } finally {
      await mounted.unmount()
    }
  })

  it('unavailable 非空时确认按钮禁用', async () => {
    inspectBackupFile.mockResolvedValueOnce(inspectResult({ unavailable: [{ id: 'agent_workspace', reason: 'missing' }] }))
    const onConfirm = vi.fn()
    const mounted = await renderDialog(onConfirm)
    try {
      await dropFile(dropzone(mounted.container), testFile)
      await typeInto(passphraseInput(mounted.container), 'secret')
      await click(button(mounted.container, '下一步：预览'))
      const confirm = button(mounted.container, '确认导入并覆盖')
      expect(confirm.getAttribute('disabled')).toBeDefined()
      await click(confirm)
      expect(onConfirm).not.toHaveBeenCalled()
    } finally {
      await mounted.unmount()
    }
  })

  it('确认后以原文件与口令触发恢复并重置', async () => {
    inspectBackupFile.mockResolvedValueOnce(inspectResult())
    const onConfirm = vi.fn()
    const mounted = await renderDialog(onConfirm)
    try {
      await dropFile(dropzone(mounted.container), testFile)
      await typeInto(passphraseInput(mounted.container), 'secret')
      await click(button(mounted.container, '下一步：预览'))
      await click(button(mounted.container, '确认导入并覆盖'))
      expect(onConfirm).toHaveBeenCalledOnce()
      const [file, passphrase] = onConfirm.mock.calls[0]
      expect(file).toBe(testFile)
      expect(passphrase).toBe('secret')
    } finally {
      await mounted.unmount()
    }
  })
})
