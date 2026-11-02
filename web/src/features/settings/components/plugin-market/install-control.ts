import { useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { jsPluginQueryKeys } from '@/features/settings/api/js-plugins'
import {
  installPluginMarketPlugin,
  pluginMarketQueryKeys,
  type PluginMarketPlugin,
  type PluginMarketSnapshot,
} from '@/features/settings/api/plugin-market'
import { toast } from '@/lib/toast'
import { marketInstallErrorText } from './install-error'
import { sanitizeErrorText } from './sanitize-error'
import { usePluginsTab } from './tab-state'

type InstallBlock =
  | { code: 'revocations_expired' }
  | { code: 'incompatible'; reason?: string }
  | { code: 'revoked' }

type InstallTranslate = (key: string, options?: { version?: string }) => string

export type InstallMenuItem = {
  key: string
  label: string
}

export type InstallControlModel = {
  /** split：安装/更新 + 菜单；enable：只启用；none：已是最新且已启用 */
  mode: 'split' | 'enable' | 'none'
  primaryLabel: string
  menuItem: InstallMenuItem | null
  disabled: boolean
  blocked: boolean
  /** 不可用原因；卡片上放到按钮 title */
  reason: string | null
}

function installBlock(plugin: PluginMarketPlugin, snapshot?: PluginMarketSnapshot): InstallBlock | null {
  if (snapshot?.revocations_expired) return { code: 'revocations_expired' }
  if (!plugin.has_compatible_version) return { code: 'incompatible', reason: plugin.incompatible_reason }
  const latest = plugin.latest_compatible_version
  const row = latest ? plugin.versions?.find((item) => item.version === latest) : undefined
  if (row?.revocation?.revoked && row.revocation.level === 'security') return { code: 'revoked' }
  return null
}

function installKind(plugin: PluginMarketPlugin): 'update' | 'current' | 'install' {
  const installed = plugin.local_status === 'installed' || plugin.local_status === 'enabled'
  if (installed && plugin.update_available) return 'update'
  if (installed && plugin.has_compatible_version) return 'current'
  return 'install'
}

export function reasonText(t: (key: string) => string, reason?: string) {
  if (reason === 'xlyra_version' || reason === 'api_version' || reason === 'host_api') {
    return t(`jsPlugins.market.reason.${reason}`)
  }
  return sanitizeErrorText(reason || '—')
}

function installBlockText(t: (key: string) => string, block: InstallBlock) {
  if (block.code === 'revocations_expired') return t('jsPlugins.market.installPaused')
  if (block.code === 'revoked') return t('jsPlugins.market.versionRevoked')
  return reasonText(t, block.reason)
}

export function installControlModel(
  plugin: PluginMarketPlugin,
  snapshot: PluginMarketSnapshot | undefined,
  t: InstallTranslate,
): InstallControlModel {
  const block = installBlock(plugin, snapshot)
  const kind = installKind(plugin)
  const reason = block ? installBlockText(t, block) : null
  const blocked = block != null

  if (kind === 'current' && plugin.local_status === 'installed') {
    return {
      mode: 'enable',
      primaryLabel: t('jsPlugins.enable'),
      menuItem: null,
      disabled: blocked,
      blocked,
      reason,
    }
  }

  if (kind === 'current') {
    return {
      mode: 'none',
      primaryLabel: t('jsPlugins.market.badgeInstalled'),
      menuItem: null,
      disabled: true,
      blocked,
      reason,
    }
  }

  const update = kind === 'update'
  return {
    mode: 'split',
    primaryLabel: t(update ? 'jsPlugins.market.update' : 'jsPlugins.market.install'),
    menuItem: {
      key: update ? 'update-enable' : 'install-enable',
      label: t(update ? 'jsPlugins.market.updateEnable' : 'jsPlugins.market.installEnable'),
    },
    disabled: blocked,
    blocked,
    reason,
  }
}

export function useMarketInstall(plugin: PluginMarketPlugin, snapshot?: PluginMarketSnapshot) {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const { selectInstalledEnable } = usePluginsTab()
  const lock = useRef(false)
  const [pending, setPending] = useState<'stay' | 'enable' | null>(null)
  const model = installControlModel(plugin, snapshot, (key, options) => t(key, options))

  async function run(enableAfter: boolean) {
    if (lock.current || model.blocked) return
    lock.current = true
    setPending(enableAfter ? 'enable' : 'stay')
    try {
      const version = plugin.latest_compatible_version || undefined
      const row = await installPluginMarketPlugin(plugin.id, version)
      await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.list() })
      await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.detail(plugin.id) })
      await queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.all })
      if (enableAfter) {
        toast.success(t('jsPlugins.market.installedDone'))
        selectInstalledEnable(row.plugin_id || plugin.id, row.version)
      } else {
        toast.success(t('jsPlugins.market.installedStay'))
      }
    } catch (error) {
      toast.error(marketInstallErrorText(t, error))
    } finally {
      lock.current = false
      setPending(null)
    }
  }

  function enableInstalled() {
    if (model.blocked) return
    const version = plugin.local_version || plugin.latest_compatible_version
    if (!version) return
    selectInstalledEnable(plugin.id, version)
  }

  return { model, pending, run, enableInstalled }
}

export type MarketInstallState = ReturnType<typeof useMarketInstall>
