import { act } from 'react'
import type { ComponentProps } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { BackupSectionInfo } from '@/features/settings/api/settings'
import { BackupSectionsField } from '@/features/settings/components/backup/backup-sections-field'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import common from '@/locales/zh/common.json'
import zhSettings from '@/locales/zh/settings.json'

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
    defaultNS: 'settings',
    resources: { zh: { settings: zhSettings, common } },
    interpolation: { escapeValue: false },
  })
})

function section(id: string): BackupSectionInfo {
  return {
    id,
    group: 'system',
    requires: [],
    recommends: [],
    default_manual: false,
    default_auto: false,
    available: true,
  }
}

const sections = [
  section('settings'),
  section('gateway_sites'),
  section('gateway_keys'),
  section('gateway_logs'),
  section('agent_config'),
]

async function renderField(overrides: Partial<ComponentProps<typeof BackupSectionsField>> = {}) {
  return mountMini(
    <I18nextProvider i18n={i18n}>
      <BackupSectionsField
        title="备份内容"
        sections={sections}
        loading={false}
        error={false}
        value={[]}
        onChange={() => {}}
        {...overrides}
      />
    </I18nextProvider>,
  )
}

function button(container: MiniNode, text: string) {
  const [node] = findMiniNodes(container, (candidate) => candidate.tagName === 'BUTTON' && candidate.textContent?.trim() === text)
  if (!node) throw new Error(`找不到按钮: ${text}`)
  return node
}

function checkbox(container: MiniNode, id: string) {
  const [node] = findMiniNodes(container, (candidate) => (
    candidate.tagName === 'BUTTON'
    && candidate.getAttribute('role') === 'checkbox'
    && candidate.getAttribute('id')?.includes(id)
  ))
  if (!node) throw new Error(`找不到类别复选框: ${id}`)
  return node
}

async function click(node: MiniNode) {
  await act(async () => { node.dispatchEvent(miniEvent('click')) })
}

describe('BackupSectionsField', () => {
  it('显示已选摘要，超过三项时末尾加等', async () => {
    const mounted = await renderField({
      hint: '选择自动备份包含的内容',
      value: ['settings', 'gateway_sites', 'gateway_keys', 'gateway_logs'],
    })
    try {
      expect(mounted.container.textContent).toContain('已选 4 项：系统设置、站点与模型、API 密钥与权限等')
      expect(mounted.container.textContent).toContain('选择自动备份包含的内容')
    } finally {
      await mounted.unmount()
    }
  })

  it('未选择时显示摘要与空选择错误', async () => {
    const mounted = await renderField({ emptyError: true })
    try {
      expect(mounted.container.textContent).toContain('未选择')
      expect(mounted.container.textContent).toContain('至少选择一个备份内容')
    } finally {
      await mounted.unmount()
    }
  })

  it('加载中时禁用选择按钮', async () => {
    const mounted = await renderField({ sections: undefined, loading: true })
    try {
      expect(mounted.container.textContent).toContain('正在读取备份内容目录...')
      expect(button(mounted.container, '选择内容').getAttribute('disabled')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('加载失败时禁用选择按钮', async () => {
    const mounted = await renderField({ sections: undefined, error: true })
    try {
      expect(mounted.container.textContent).toContain('备份内容目录读取失败')
      expect(button(mounted.container, '选择内容').getAttribute('disabled')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('点击选择内容打开弹窗', async () => {
    const mounted = await renderField({ value: ['settings'] })
    try {
      await click(button(mounted.container, '选择内容'))
      expect(mounted.container.textContent).toContain('选择备份内容')
      expect(mounted.container.textContent).toContain('勾选需要备份的内容；有依赖关系的内容会被自动一并选中。')
      expect(checkbox(mounted.container, 'settings')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('取消弹窗不触发 onChange', async () => {
    const onChange = vi.fn()
    const mounted = await renderField({ value: ['settings'], onChange })
    try {
      await click(button(mounted.container, '选择内容'))
      await click(checkbox(mounted.container, 'gateway_sites'))
      await click(button(mounted.container, '取消'))
      expect(onChange).not.toHaveBeenCalled()
      expect(mounted.container.textContent).not.toContain('选择备份内容')
    } finally {
      await mounted.unmount()
    }
  })

  it('勾选只修改草稿，点击完成才回调草稿', async () => {
    const onChange = vi.fn()
    const mounted = await renderField({ value: ['settings'], onChange })
    try {
      await click(button(mounted.container, '选择内容'))
      await click(checkbox(mounted.container, 'gateway_sites'))
      expect(onChange).not.toHaveBeenCalled()
      await click(button(mounted.container, '完成'))
      expect(onChange).toHaveBeenCalledTimes(1)
      expect(onChange).toHaveBeenCalledWith(['settings', 'gateway_sites'])
      expect(mounted.container.textContent).not.toContain('选择备份内容')
    } finally {
      await mounted.unmount()
    }
  })

  it('草稿为空时禁用完成并显示错误', async () => {
    const mounted = await renderField()
    try {
      await click(button(mounted.container, '选择内容'))
      expect(button(mounted.container, '完成').getAttribute('disabled')).toBeDefined()
      expect(mounted.container.textContent).toContain('至少选择一个备份内容')
    } finally {
      await mounted.unmount()
    }
  })
})
