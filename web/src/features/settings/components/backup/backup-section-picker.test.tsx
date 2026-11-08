import { act, useState } from 'react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import type { BackupSectionInfo } from '@/features/settings/api/settings'
import { BackupSectionPicker } from '@/features/settings/components/backup/backup-section-picker'
import { findMiniNodes, miniEvent, mountMini, type MiniNode } from '@/test/mini-dom'
import zhSettings from '@/locales/zh/settings.json'

const i18n = createInstance()
beforeAll(async () => {
  await i18n.init({ lng: 'zh', defaultNS: 'settings', resources: { zh: { settings: zhSettings } } })
})

function section(overrides: Partial<BackupSectionInfo> & { id: string }): BackupSectionInfo {
  return {
    group: 'system',
    requires: [],
    recommends: [],
    default_manual: false,
    default_auto: false,
    available: true,
    ...overrides,
  }
}

const sections: BackupSectionInfo[] = [
  section({ id: 'settings' }),
  section({ id: 'gateway_sites', group: 'gateway' }),
  section({ id: 'gateway_keys', group: 'gateway', requires: ['gateway_sites'] }),
  section({ id: 'gateway_logs', group: 'gateway', requires: ['gateway_sites', 'gateway_keys'] }),
  section({ id: 'agent_config', group: 'agent', recommends: ['agent_capabilities'] }),
  section({ id: 'agent_capabilities', group: 'agent' }),
  section({ id: 'agent_workspace', group: 'agent', available: false, unavailable_reason: 'workspace missing', size_bytes: 4096 }),
]

function PickerHarness({ initial, onChange = () => {} }: { initial: string[]; onChange?: (next: string[]) => void }) {
  const [value, setValue] = useState(initial)
  return (
    <>
      <BackupSectionPicker
        sections={sections}
        value={value}
        onChange={(next) => {
          setValue(next)
          onChange(next)
        }}
      />
      <output data-value={value.join(',')}>{value.join(',')}</output>
    </>
  )
}

async function renderPicker(initial: string[], onChange?: (next: string[]) => void) {
  const mounted = await mountMini(
    <I18nextProvider i18n={i18n}>
      <PickerHarness initial={initial} onChange={onChange} />
    </I18nextProvider>,
  )
  return mounted
}

function checkboxButton(container: MiniNode, id: string) {
  const [node] = findMiniNodes(container, (candidate) => candidate.tagName === 'BUTTON' && candidate.getAttribute('role') === 'checkbox' && candidate.getAttribute('id')?.includes(id))
  if (!node) throw new Error(`找不到类别复选框: ${id}`)
  return node
}

async function click(node: MiniNode) {
  await act(async () => { node.dispatchEvent(miniEvent('click')) })
}

describe('BackupSectionPicker', () => {
  it('勾选 gateway_logs 连带勾上 gateway_sites/gateway_keys 并回调 onChange', async () => {
    const onChange = vi.fn()
    const mounted = await renderPicker(['settings'], onChange)
    try {
      await click(checkboxButton(mounted.container, 'gateway_logs'))
      expect(onChange).toHaveBeenCalledWith(['settings', 'gateway_sites', 'gateway_keys', 'gateway_logs'])
      const output = findMiniNodes(mounted.container, (node) => node.tagName === 'OUTPUT')[0]
      expect(output?.getAttribute('data-value')).toBe('settings,gateway_sites,gateway_keys,gateway_logs')
    } finally {
      await mounted.unmount()
    }
  })

  it('被依赖项禁用并显示依赖提示', async () => {
    const onChange = vi.fn()
    const mounted = await renderPicker(['gateway_sites', 'gateway_keys'], onChange)
    try {
      const siteCheckbox = checkboxButton(mounted.container, 'gateway_sites')
      expect(siteCheckbox.getAttribute('disabled')).toBeDefined()
      expect(mounted.container.textContent).toContain('请求日志与用量')
      await click(siteCheckbox)
      expect(onChange).not.toHaveBeenCalled()
      const keyCheckbox = checkboxButton(mounted.container, 'gateway_keys')
      expect(keyCheckbox.getAttribute('disabled')).toBeDefined()
    } finally {
      await mounted.unmount()
    }
  })

  it('不可用项禁用并显示原因与体积', async () => {
    const mounted = await renderPicker([], () => {})
    try {
      const workspaceCheckbox = checkboxButton(mounted.container, 'agent_workspace')
      expect(workspaceCheckbox.getAttribute('disabled')).toBeDefined()
      expect(mounted.container.textContent).toContain('workspace missing')
      expect(mounted.container.textContent).toContain('4 KB')
    } finally {
      await mounted.unmount()
    }
  })

  it('显示软依赖提示与分组标题', async () => {
    const mounted = await renderPicker(['agent_config'])
    try {
      expect(mounted.container.textContent).toContain('Agent 能力文件')
      const html = String(mounted.container.textContent)
      expect(html).toContain('系统')
      expect(html).toContain('网关数据')
      expect(html).toContain('Agent')
    } finally {
      await mounted.unmount()
    }
  })
})
