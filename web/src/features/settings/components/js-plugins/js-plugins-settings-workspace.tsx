import { useCallback, useEffect, useRef, useState, type ChangeEvent, type DragEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { FlaskConical, KeyRound, LoaderCircle, Power, PowerOff, RotateCcw, Trash2, Upload } from 'lucide-react'
import { Card, CardFooter, CardMetric, CardMetrics, ItemCard } from '@/components/ui/card'
import { useMobileLayout } from '@/hooks/use-media-query'
import { EmptyState } from '@/components/common/empty-state'
import { PageHeader } from '@/components/common/page-header'
import { JSPluginVersionMenu, type VersionMenuItem } from '@/features/settings/components/js-plugins/js-plugin-version-menu'
import { PluginAvatar } from '@/features/settings/components/js-plugins/plugin-avatar'
import { PluginMetrics24h } from '@/features/settings/components/js-plugins/plugin-metrics-24h'
import { JSPluginAutomationSection } from '@/features/settings/components/js-plugins/js-plugin-automation'
import {
  acceptEnableDialog,
  beginEnable,
  cancelEnable,
  enableFlowOffersUntrusted,
  failEnable,
  resumeEnableFlow,
  type EnableFlowState,
  type EnableFlowStep,
  type EnableTrust,
} from '@/features/settings/components/js-plugins/enable-flow'
import { JSPluginSiteProviderSection } from '@/features/settings/components/js-plugins/js-plugin-site-provider'
import { jsPluginErrorNotice, readJSPluginErrorFields } from '@/features/settings/components/js-plugins/js-plugin-errors'
import { permissionLabelKey } from '@/features/settings/components/js-plugins/permissions'
import { JSPluginSiteBindingSection } from '@/features/settings/components/js-plugins/js-plugin-site-binding'
import { jsPluginKindLabel } from '@/features/settings/components/js-plugins/kind-label'
import { useSiteBinding } from '@/features/settings/components/js-plugins/use-site-binding'
import { PluginMarketPanel } from '@/features/settings/components/plugin-market/market-panel'
import { parseEnableParam } from '@/features/settings/components/plugin-market/enable-param'
import { usePluginsTab } from '@/features/settings/components/plugin-market/tab-state'
import { PluginTabBar } from '@/features/settings/components/plugin-market/tabs'
import { installPluginMarketPlugin, pluginMarketQueryKeys } from '@/features/settings/api/plugin-market'
import { marketInstallErrorText } from '@/features/settings/components/plugin-market/install-error'
import { PluginPolicySelect } from '@/features/settings/components/plugin-market/policy-select'
import { isUpdatePolicy, updateReasonText } from '@/features/settings/components/plugin-market/update-reason'
import { fetchCloudDeveloper, cloudDeveloperQueryKeys, submitCloudPlugin } from '@/features/settings/api/cloud-developer'
import { MarketSubmitActions, MarketSubmitDialog } from '@/features/settings/components/js-plugins/market-submit'
import { submissionStatusLabel, submitErrorText } from '@/features/settings/components/js-plugins/market-submit-model'
import {
  MobileCardList,
  MobileRowList,
  MobileVersionCard,
} from '@/features/settings/components/js-plugins/js-plugins-mobile'
import { FormField } from '@/components/ui/form-field'
import { StatusBadge } from '@/components/common/status-badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  bindJSPluginProtocolSlug,
  createJSPluginTrustedKey,
  deleteJSPluginTrustedKey,
  deleteJSPlugin,
  deleteJSPluginVersion,
  disableJSPlugin,
  automationManifest,
  siteProviderOf,
  enableJSPluginVersion,
  getJSPlugin,
  JS_PLUGIN_MAX_PACKAGE_BYTES,
  jsPluginQueryKeys,
  listJSPluginTrustedKeys,
  listUploadedJSPlugins,
  tryJSPlugin,
  updateJSPluginSubscription,
  uploadJSPlugin,
  type JSPluginListItem,
  type JSPluginTrustedKey,
  type JSPluginVersion,
} from '@/features/settings/api/js-plugins'
import { listSites, sitesQueryKeys } from '@/features/sites/api/sites'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

function versionStatusBadge(status: string) {
  switch (status) {
    case 'enabled':
      return <StatusBadge status="success">{status}</StatusBadge>
    case 'broken':
      return <StatusBadge status="error">{status}</StatusBadge>
    case 'disabled':
      return <StatusBadge status="disabled">{status}</StatusBadge>
    default:
      return <StatusBadge status="idle">{status}</StatusBadge>
  }
}

function kindLabel(t: TFunction, kind: string) {
  return jsPluginKindLabel(t, kind)
}

function toastPluginFailure(t: TFunction, error: unknown, fallbackKey: string) {
  if (!(error instanceof APIError)) {
    toast.error(t(fallbackKey))
    return
  }
  const notice = jsPluginErrorNotice(error.code, readJSPluginErrorFields(error.details))
  toast.error(notice ? t(notice.key, notice.params) : error.message || t(fallbackKey))
}

type EnableVariables = {
  pluginId: string
  version: string
  confirmUntrusted?: boolean
  grantPermissions?: string[]
  confirmHosts?: string[]
}

function enableTrustFor(version: JSPluginVersion | undefined, fallback: EnableTrust, confirmUntrusted?: boolean): EnableTrust {
  if (confirmUntrusted) return version?.signer ? 'untrusted_signer' : (fallback === 'trusted' ? 'unsigned' : fallback)
  return version?.trust ?? fallback
}

function manifestString(manifest: Record<string, unknown> | undefined, key: string) {
  const value = manifest?.[key]
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function shortSha256(sha?: string) {
  if (!sha) return '—'
  if (sha.length <= 14) return sha
  return `${sha.slice(0, 8)}…${sha.slice(-6)}`
}

export function JSPluginsSettingsWorkspace() {
  const { t } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [managePlugin, setManagePlugin] = useState<JSPluginListItem | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ pluginId: string; version: string } | null>(null)
  const [uninstallTarget, setUninstallTarget] = useState<{ pluginId: string; name: string; versions: number } | null>(null)
  const enableFlowRef = useRef<EnableFlowState | null>(null)
  const requestEnableRef = useRef<(row: JSPluginVersion) => void>(() => {})
  const handledEnable = useRef<string | null>(null)
  const enableRequest = useRef(0)
  const [enableFlow, setEnableFlow] = useState<EnableFlowState | null>(null)
  const [enableConfirmed, setEnableConfirmed] = useState(false)
  const rememberEnableFlow = (state: EnableFlowState | null) => {
    enableFlowRef.current = state
    setEnableFlow(state)
    setEnableConfirmed(false)
  }
  const [uploadOpen, setUploadOpen] = useState(false)
  const [submitPlugin, setSubmitPlugin] = useState<JSPluginListItem | null>(null)
  const [submitError, setSubmitError] = useState('')
  const [uploadDragging, setUploadDragging] = useState(false)
  const [trustedKeysOpen, setTrustedKeysOpen] = useState(false)
  const [trustedKeyAddOpen, setTrustedKeyAddOpen] = useState(false)
  const [trustedKeyDeleteTarget, setTrustedKeyDeleteTarget] = useState<{ id: string; name: string } | null>(null)
  const [trustedKeyName, setTrustedKeyName] = useState('')
  const [trustedKeyPublic, setTrustedKeyPublic] = useState('')
  // Last trial run of each version, shown in its row until the dialog closes.
  const [trials, setTrials] = useState<Record<string, TrialState>>({})
  const [protocolSlug, setProtocolSlug] = useState('')
  const isMobile = useMobileLayout()
  const { tab, selectTab, params, setParams } = usePluginsTab()

  const developerQuery = useQuery({
    queryKey: cloudDeveloperQueryKeys.developer(),
    queryFn: ({ signal }) => fetchCloudDeveloper(false, signal),
  })
  const uploadedQuery = useQuery({
    queryKey: jsPluginQueryKeys.uploaded(),
    queryFn: ({ signal }) => listUploadedJSPlugins(signal),
  })
  useEffect(() => {
    if (developerQuery.data?.status !== 'active') return
    void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.uploaded() })
  }, [developerQuery.data?.status, developerQuery.dataUpdatedAt, queryClient])
  const detailQuery = useQuery({
    queryKey: managePlugin ? jsPluginQueryKeys.detail(managePlugin.id) : ['settings', 'js-plugins', 'detail', 'none'],
    queryFn: ({ signal }) => getJSPlugin(managePlugin!.id, signal),
    enabled: managePlugin != null,
  })
  const submitDetailQuery = useQuery({
    queryKey: submitPlugin ? [...jsPluginQueryKeys.detail(submitPlugin.id), 'submit'] : ['settings', 'js-plugins', 'detail', 'submit-none'],
    queryFn: ({ signal }) => getJSPlugin(submitPlugin!.id, signal),
    enabled: submitPlugin != null,
  })
  const sitesQuery = useQuery({
    // 与其他页面共用 list('all') 前缀，但缓存的是 items 数组而不是 { items, meta }，必须用独立的 key，
    // 否则先访问过站点分组、模型、API 密钥页时，这里会读到对象而不是数组
    queryKey: [...sitesQueryKeys.listWithOAuth(), 'items'],
    queryFn: async () => {
      const result = await listSites({ oauth: 'all' })
      return result.items ?? []
    },
    enabled: managePlugin?.scope === 'site',
  })

  const invalidateAll = () => {
    void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.all })
  }

  const uploadMutation = useMutation({
    mutationFn: uploadJSPlugin,
    onSuccess: (row) => {
      toast.success(t('settings:jsPlugins.uploadSuccess', { id: row.plugin_id, version: row.version }))
      setUploadOpen(false)
      setUploadDragging(false)
      invalidateAll()
    },
    onError: (error: unknown) => {
      if (!(error instanceof APIError)) {
        toast.error(t('settings:jsPlugins.uploadFailed'))
        return
      }
      switch (error.code) {
        case 'js_plugin_version_conflict':
          toast.error(t('settings:jsPlugins.uploadVersionConflict'))
          return
        case 'request_body_too_large':
          toast.error(t('settings:jsPlugins.uploadTooLarge'))
          return
        case 'js_plugin_package_invalid':
          toast.error(t('settings:jsPlugins.uploadInvalid', { message: error.message }))
          return
        default:
          toast.error(t('settings:jsPlugins.uploadFailed'))
      }
    },
  })

  const enableMutation = useMutation({
    mutationFn: ({ pluginId, version, confirmUntrusted, grantPermissions, confirmHosts }: EnableVariables) =>
      enableJSPluginVersion(pluginId, version, {
        confirm_untrusted: confirmUntrusted,
        grant_permissions: grantPermissions,
        confirm_hosts: confirmHosts,
      }),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.enableSuccess'))
      rememberEnableFlow(null)
      invalidateAll()
      if (managePlugin) {
        void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.detail(managePlugin.id) })
      }
    },
    onError: (error: unknown, variables) => {
      const version = detailQuery.data?.versions.find((item) => item.version === variables.version)
      const current = enableFlowRef.current
      const base = current && current.pluginId === variables.pluginId && current.version === variables.version
        ? current
        : resumeEnableFlow({
            pluginId: variables.pluginId,
            version: variables.version,
            trust: enableTrustFor(version, 'trusted', variables.confirmUntrusted),
            signer: version?.signer,
            permissions: automationManifest(version?.manifest)?.permissions ?? variables.grantPermissions ?? [],
            siteProvider: manifestString(version?.manifest, 'kind') === 'site_provider',
            confirmUntrusted: variables.confirmUntrusted,
            grantPermissions: variables.grantPermissions,
            confirmHosts: variables.confirmHosts,
          })
      const manifestKind = manifestString(version?.manifest, 'kind')
      const code = error instanceof APIError ? error.code : undefined
      const step = failEnable(
        {
          ...base,
          signer: version?.signer ?? base.signer,
          trust: code === 'js_plugin_confirm_required'
            ? (version?.signer ? 'untrusted_signer' : 'unsigned')
            : enableTrustFor(version, base.trust, variables.confirmUntrusted),
          permissions: version ? (automationManifest(version.manifest)?.permissions ?? []) : base.permissions,
          siteProvider: manifestKind ? manifestKind === 'site_provider' : base.siteProvider,
        },
        { code, fields: error instanceof APIError ? readJSPluginErrorFields(error.details) : {} },
      )
      if (step.type === 'cancel') {
        rememberEnableFlow(null)
        return
      }
      rememberEnableFlow(step.state)
      if (step.type === 'dialog') return
      if (step.type === 'toast') {
        const notice = jsPluginErrorNotice(step.code, step.fields)
        toast.error(notice ? t(notice.key, notice.params) : t('settings:jsPlugins.enableFailed'))
        return
      }
      if (error instanceof APIError && error.code === 'js_plugin_kind_not_connected') {
        toast.error(t('settings:jsPlugins.kindNotConnected'))
        return
      }
      toastPluginFailure(t, error, 'settings:jsPlugins.enableFailed')
    },
  })

  const addTrustedKeyMutation = useMutation({
    mutationFn: () =>
      createJSPluginTrustedKey({
        name: trustedKeyName.trim(),
        public_key: trustedKeyPublic.trim(),
      }),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.trustedKeyAddSuccess'))
      setTrustedKeyName('')
      setTrustedKeyPublic('')
      setTrustedKeyAddOpen(false)
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.trustedKeys() })
      invalidateAll()
    },
    onError: (error: unknown) => {
      if (error instanceof APIError && error.code === 'js_plugin_trusted_key_exists') {
        toast.error(t('settings:jsPlugins.trustedKeyExists'))
        return
      }
      if (error instanceof APIError && error.code === 'js_plugin_trusted_key_invalid') {
        toast.error(error.message)
        return
      }
      toast.error(t('settings:jsPlugins.trustedKeyAddFailed'))
    },
  })

  const deleteTrustedKeyMutation = useMutation({
    mutationFn: (id: string) => deleteJSPluginTrustedKey(id),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.trustedKeyDeleteSuccess'))
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.trustedKeys() })
      invalidateAll()
    },
    onError: (error: unknown) => {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.trustedKeyDeleteFailed'))
    },
  })

  const submitMutation = useMutation({
    mutationFn: (changelog: string) => {
      if (!submitPlugin?.id || !submitPlugin.local_version) throw new Error('missing plugin')
      return submitCloudPlugin({ pluginId: submitPlugin.id, version: submitPlugin.local_version, changelog })
    },
    onSuccess: (result) => {
      const pluginId = submitPlugin?.id
      toast.success(submissionStatusLabel(t, result.status))
      setSubmitError('')
      setSubmitPlugin(null)
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.uploaded() })
      if (pluginId) void queryClient.invalidateQueries({ queryKey: cloudDeveloperQueryKeys.submissions(pluginId) })
    },
    onError: (error: unknown) => {
      if (developerQuery.data?.unavailable && error instanceof APIError && error.code === 'not_developer') {
        setSubmitError(t('settings:jsPlugins.submit.unavailable'))
        return
      }
      setSubmitError(submitErrorText(t, error))
    },
  })

  const disableMutation = useMutation({
    mutationFn: (pluginId: string) => disableJSPlugin(pluginId),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.disableSuccess'))
      invalidateAll()
      if (managePlugin) {
        void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.detail(managePlugin.id) })
      }
    },
    onError: (error: unknown) => {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.disableFailed'))
    },
  })

  const deleteMutation = useMutation({
    mutationFn: ({ pluginId, version }: { pluginId: string; version: string }) =>
      deleteJSPluginVersion(pluginId, version),
    onSuccess: (_result, deleted) => {
      toast.success(t('settings:jsPlugins.deleteSuccess'))
      setDeleteTarget(null)
      invalidateAll()
      if (managePlugin) {
        // Uninstalling the last version removes the plugin itself, so there is nothing left to manage.
        const remaining = (detailQuery.data?.versions ?? []).filter((row) => row.version !== deleted.version)
        if (remaining.length === 0) closeManage()
        else void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.detail(managePlugin.id) })
      }
    },
    onError: (error: unknown) => {
      toastPluginFailure(t, error, 'settings:jsPlugins.deleteFailed')
    },
  })

  const uninstallMutation = useMutation({
    mutationFn: ({ pluginId }: { pluginId: string }) => deleteJSPlugin(pluginId),
    onSuccess: (_result, { pluginId }) => {
      toast.success(t('settings:jsPlugins.uninstallAllSuccess'))
      setUninstallTarget(null)
      invalidateAll()
      if (managePlugin?.id === pluginId) closeManage()
    },
    onError: (error: unknown) => {
      toastPluginFailure(t, error, 'settings:jsPlugins.deleteFailed')
    },
  })

  const tryMutation = useMutation({
    mutationFn: ({ pluginId, version }: { pluginId: string; version: string }) => tryJSPlugin(pluginId, version),
    onSuccess: (result, { pluginId, version }) => {
      setTrials((current) => ({
        ...current,
        [trialKey(pluginId, version)]: { ok: result.ok, durationUs: result.duration_us, error: result.error },
      }))
    },
    onError: (error: unknown, { pluginId, version }) => {
      setTrials((current) => ({
        ...current,
        [trialKey(pluginId, version)]: {
          ok: false,
          error: error instanceof APIError ? error.message : t('settings:jsPlugins.tryFailed'),
        },
      }))
    },
  })

  const bindProtocolMutation = useMutation({
    mutationFn: ({ pluginId, version, slug }: { pluginId: string; version: string; slug: string }) =>
      bindJSPluginProtocolSlug(pluginId, version, slug),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.bindProtocolSuccess'))
      setProtocolSlug('')
    },
    onError: (error: unknown) => {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.bindProtocolFailed'))
    },
  })

  const updateMutation = useMutation({
    mutationFn: (plugin: JSPluginListItem) => installPluginMarketPlugin(plugin.id, plugin.update_version),
    onSuccess: async (row) => {
      await queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.all })
      await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.all })
      requestEnableRef.current(row)
    },
    onError: (error: unknown) => {
      toast.error(marketInstallErrorText((key) => t(key), error))
    },
  })

  const subscriptionMutation = useMutation({
    mutationFn: ({ pluginId, policy }: { pluginId: string; policy: 'off' | 'notify' | 'auto' }) =>
      updateJSPluginSubscription(pluginId, policy),
    onSuccess: (result) => {
      setManagePlugin((current) => current && current.id === result.plugin_id
        ? { ...current, update_policy: result.update_policy, paused_reason: result.paused_reason }
        : current)
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.uploaded() })
    },
    onError: (error: unknown) => {
      toast.error(error instanceof APIError ? error.message : t('settings:jsPlugins.market.saveFailed'))
    },
  })

  const busy =
    uploadMutation.isPending ||
    enableMutation.isPending ||
    disableMutation.isPending ||
    deleteMutation.isPending ||
    uninstallMutation.isPending ||
    tryMutation.isPending ||
    bindProtocolMutation.isPending ||
    addTrustedKeyMutation.isPending ||
    deleteTrustedKeyMutation.isPending ||
    updateMutation.isPending ||
    subscriptionMutation.isPending

  const submitUploadFile = (file: File | null | undefined) => {
    if (!file || uploadMutation.isPending) return
    if (!/\.(xlp|zip)$/i.test(file.name)) {
      toast.error(t('settings:jsPlugins.uploadWrongType'))
      return
    }
    if (file.size > JS_PLUGIN_MAX_PACKAGE_BYTES) {
      toast.error(t('settings:jsPlugins.uploadTooLarge'))
      return
    }
    uploadMutation.mutate(file)
  }

  const onFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    submitUploadFile(file)
  }

  const onUploadDrop = (event: DragEvent) => {
    event.preventDefault()
    setUploadDragging(false)
    const file = event.dataTransfer.files?.[0]
    submitUploadFile(file)
  }

  const uploadedUnavailable =
    uploadedQuery.isError &&
    uploadedQuery.error instanceof APIError &&
    uploadedQuery.error.code === 'js_plugin_unavailable'

  const trustedKeysQuery = useQuery({
    queryKey: jsPluginQueryKeys.trustedKeys(),
    queryFn: ({ signal }) => listJSPluginTrustedKeys(signal),
    enabled: !uploadedUnavailable,
  })

  const trustedKeyCount = trustedKeysQuery.data?.length ?? 0

  const refresh = () => {
    void fetchCloudDeveloper(true).then((profile) => {
      queryClient.setQueryData(cloudDeveloperQueryKeys.developer(), profile)
      if (profile.status !== 'active') void uploadedQuery.refetch()
    }).catch(() => {
      void uploadedQuery.refetch()
    })
    if (!uploadedUnavailable) {
      void trustedKeysQuery.refetch()
    }
    void queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.all })
  }

  const openManage = useCallback((plugin: JSPluginListItem) => {
    setManagePlugin(plugin)
    setProtocolSlug('')
  }, [])

  const enabledVersionRow = (detailQuery.data?.versions ?? []).find((row) => row.status === 'enabled')
  const bindsToSites = managePlugin?.scope === 'site'
  const siteBinding = useSiteBinding(
    managePlugin && enabledVersionRow && bindsToSites
      ? { pluginId: managePlugin.id, version: enabledVersionRow.version, kind: managePlugin.kind ?? '' }
      : null,
    sitesQuery.data ?? [],
  )
  const closeManage = () => {
    setManagePlugin(null)
    setProtocolSlug('')
    setTrials({})
    siteBinding.reset()
  }

  const disablePlugin = disableMutation.mutate
  const openSubmit = useCallback((plugin: JSPluginListItem) => {
    setSubmitError('')
    setSubmitPlugin(plugin)
  }, [])
  const requestUninstall = useCallback(
    (plugin: JSPluginListItem) =>
      setUninstallTarget({ pluginId: plugin.id, name: plugin.name || plugin.id, versions: plugin.version_count }),
    [],
  )

  const uploadedCards = (uploadedQuery.data ?? []).map((plugin) => (
    <InstalledPluginCard
      key={plugin.id}
      plugin={plugin}
      busy={busy}
      developerUnavailable={developerQuery.data?.unavailable === true}
      onSubmit={openSubmit}
      onUpdate={() => updateMutation.mutate(plugin)}
      onManage={() => openManage(plugin)}
      onDisable={() => disablePlugin(plugin.id)}
      onUninstall={() => requestUninstall(plugin)}
    />
  ))

  const applyEnableStep = (step: EnableFlowStep) => {
    if (step.type === 'cancel') {
      rememberEnableFlow(null)
      return
    }
    rememberEnableFlow(step.state)
    if (step.type !== 'request') return
    enableMutation.mutate({
      pluginId: step.request.pluginId,
      version: step.request.version,
      confirmUntrusted: step.request.confirmUntrusted,
      grantPermissions: step.request.grantPermissions,
      confirmHosts: step.request.confirmHosts,
    })
  }

  const requestEnable = (row: JSPluginVersion) => {
    applyEnableStep(beginEnable({
      pluginId: row.plugin_id,
      version: row.version,
      trust: row.trust ?? 'trusted',
      signer: row.signer,
      permissions: automationManifest(row.manifest)?.permissions ?? [],
      siteProvider: manifestString(row.manifest, 'kind') === 'site_provider',
    }))
  }
  useEffect(() => {
    requestEnableRef.current = requestEnable
  })

  const enableParam = params.get('enable')
  useEffect(() => {
    if (!enableParam) {
      handledEnable.current = null
      return
    }
    if (handledEnable.current === enableParam) return
    handledEnable.current = enableParam
    const request = ++enableRequest.current
    const parsed = parseEnableParam(enableParam)
    const next = new URLSearchParams(params)
    next.delete('enable')
    setParams(next, { replace: true })
    if (!parsed) {
      toast.error(t('settings:jsPlugins.market.enableMissing'))
      return
    }
    const { id, version } = parsed
    void getJSPlugin(id)
      .then((detail) => {
        if (enableRequest.current !== request) return
        const row = (detail.versions ?? []).find((item) => item.version === version)
        if (!row) {
          toast.error(t('settings:jsPlugins.market.enableMissing'))
          return
        }
        requestEnableRef.current(row)
      })
      .catch(() => {
        if (enableRequest.current !== request) return
        toast.error(t('settings:jsPlugins.market.enableMissing'))
      })
  }, [enableParam, params, setParams, t])

  const dismissEnableDialog = () => {
    if (cancelEnable().type === 'cancel') rememberEnableFlow(null)
  }

  const submitEnableDialog = () => {
    if (!enableFlow?.dialog) return
    if (enableFlow.dialog === 'trust' && !enableConfirmed) return
    applyEnableStep(acceptEnableDialog(enableFlow))
  }

  const offerUntrusted = enableFlow ? enableFlowOffersUntrusted(enableFlow) : false
  const submitVersion = (submitDetailQuery.data?.versions ?? []).find((row) => row.version === submitPlugin?.local_version)

  return (
    <div className="space-y-7">
      <PageHeader
        eyebrow={t('settings:jsPlugins.eyebrow')}
        title={t('settings:jsPlugins.title')}
        description={t('settings:jsPlugins.description')}
      />

      <PluginTabBar
        tab={tab}
        installedLabel={t('settings:jsPlugins.tabs.installed')}
        marketLabel={t('settings:jsPlugins.tabs.market')}
        onTab={selectTab}
        actions={
          <Button
            type="button"
            variant="secondary"
            size={isMobile ? 'icon-sm' : 'sm'}
            onClick={refresh}
            disabled={busy}
            aria-label={isMobile ? t('common:actions.refresh') : undefined}
            title={isMobile ? t('common:actions.refresh') : undefined}
          >
            <RotateCcw
              className={cn(
                isMobile ? 'h-4 w-4' : 'mr-2 h-4 w-4',
                (uploadedQuery.isFetching || trustedKeysQuery.isFetching) && 'animate-spin',
              )}
            />
            {isMobile ? null : t('common:actions.refresh')}
          </Button>
        }
      />

      {tab === 'market' ? <PluginMarketPanel /> : (
      <section className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <h3 className="text-headline text-label">{t('settings:jsPlugins.uploadedTitle')}</h3>
            <p className="text-body text-label-2">{t('settings:jsPlugins.uploadedHint')}</p>
            {developerQuery.data?.unavailable ? (
              <p className="text-body text-label-2">{t('settings:jsPlugins.submit.unavailable')}</p>
            ) : null}
          </div>
          {!uploadedUnavailable ? (
            <div className="flex shrink-0 flex-wrap gap-2">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                disabled={busy}
                onClick={() => setTrustedKeysOpen(true)}
              >
                <KeyRound className="mr-2 h-4 w-4" />
                {t('settings:jsPlugins.trustedKeysButton', { count: trustedKeyCount })}
              </Button>
              <Button type="button" size="sm" onClick={() => setUploadOpen(true)} disabled={busy}>
                <Upload className="mr-2 h-4 w-4" />
                {t('settings:jsPlugins.upload')}
              </Button>
            </div>
          ) : null}
        </div>
        {uploadedUnavailable || uploadedCards.length === 0 ? (
          <Card material="thick" className="overflow-hidden p-0">
            <EmptyState
              title={
                uploadedUnavailable
                  ? t('settings:jsPlugins.unavailable')
                  : uploadedQuery.isLoading
                    ? t('settings:jsPlugins.loading')
                    : t('settings:jsPlugins.emptyUploaded')
              }
              description={t('settings:jsPlugins.emptyUploadedHint')}
            />
          </Card>
        ) : isMobile ? (
          <MobileCardList>{uploadedCards}</MobileCardList>
        ) : (
          <div className="grid grid-cols-2 gap-3 2xl:grid-cols-3">{uploadedCards}</div>
        )}
      </section>
      )}

      <Dialog
        open={managePlugin != null}
        onOpenChange={(open) => {
          if (!open) closeManage()
        }}
      >
        <DialogContent size="lg">
          <DialogHeader>
            <div className="flex items-start gap-3">
              {managePlugin ? (
                <PluginAvatar size="md" iconUrl={managePlugin.icon_url} name={managePlugin.name || managePlugin.id} />
              ) : null}
              <div className="min-w-0 flex-1">
                <DialogTitle>{managePlugin?.name || t('settings:jsPlugins.versionsDialogTitle')}</DialogTitle>
                <DialogDescription className="space-y-1">
                  <span className="block font-mono text-footnote">{managePlugin?.id}</span>
                  {managePlugin?.kind ? (
                    <span className="block text-footnote">{kindLabel(t, managePlugin.kind)}</span>
                  ) : null}
                  {managePlugin?.enabled_version && managePlugin.metrics_24h ? (
                    <span className="flex flex-wrap items-center gap-1.5 text-footnote text-label-2">
                      <span>{t('settings:jsPlugins.metricsLabel')}:</span>
                      <PluginMetrics24h metrics={managePlugin.metrics_24h} />
                    </span>
                  ) : null}
                </DialogDescription>
              </div>
            </div>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {managePlugin && isUpdatePolicy(managePlugin.update_policy) ? (
              <FormField label={t('settings:jsPlugins.market.policyLabel')}>
                <PluginPolicySelect
                  value={managePlugin.update_policy}
                  disabled={busy}
                  onChange={(policy) => subscriptionMutation.mutate({ pluginId: managePlugin.id, policy })}
                />
                {managePlugin.paused_reason ? (
                  <p className="text-footnote text-label-2">{t('settings:jsPlugins.market.pausedAuto')}</p>
                ) : null}
                <InstalledUpdateMarks plugin={managePlugin} />
                {managePlugin.update_available ? (
                  <Button type="button" size="sm" className="w-fit" disabled={busy} onClick={() => updateMutation.mutate(managePlugin)}>
                    {t('settings:jsPlugins.market.update')}
                  </Button>
                ) : null}
              </FormField>
            ) : null}
            {detailQuery.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-8 text-body text-label-2">
                <LoaderCircle className="h-4 w-4 animate-spin" />
                {t('settings:jsPlugins.loading')}
              </div>
            ) : detailQuery.isError ? (
              <p className="text-body text-destructive">{t('settings:jsPlugins.loadFailed')}</p>
            ) : (
              <>
                {isMobile ? (
                  <MobileRowList>
                    {(detailQuery.data?.versions ?? []).map((row) => (
                      <VersionCard
                        key={row.version}
                        row={row}
                        busy={busy}
                        trial={trials[trialKey(row.plugin_id, row.version)]}
                        tryPending={tryMutation.isPending && tryMutation.variables?.version === row.version}
                        onEnable={() => requestEnable(row)}
                        onDisable={() => disablePlugin(row.plugin_id)}
                        onTry={() => tryMutation.mutate({ pluginId: row.plugin_id, version: row.version })}
                        onDelete={() => setDeleteTarget({ pluginId: row.plugin_id, version: row.version })}
                        t={t}
                      />
                    ))}
                  </MobileRowList>
                ) : (
                  <table className="w-full min-w-full border-collapse text-left text-body">
                    <thead>
                      <tr className="border-b border-separator text-label-2">
                        <th className="px-3 py-2 font-medium">{t('settings:jsPlugins.columns.version')}</th>
                        <th className="px-3 py-2 font-medium">{t('settings:jsPlugins.columns.status')}</th>
                        <th className="px-3 py-2 font-medium">{t('settings:jsPlugins.columns.package')}</th>
                        <th className="px-3 py-2 font-medium">{t('settings:jsPlugins.columns.selftest')}</th>
                        <th className="px-3 py-2 text-right font-medium">{t('settings:jsPlugins.columns.actions')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {(detailQuery.data?.versions ?? []).map((row) => (
                        <VersionRow
                          key={row.version}
                          row={row}
                          busy={busy}
                          trial={trials[trialKey(row.plugin_id, row.version)]}
                        tryPending={tryMutation.isPending && tryMutation.variables?.version === row.version}
                          onEnable={() => requestEnable(row)}
                        onDisable={() => disablePlugin(row.plugin_id)}
                          onTry={() => tryMutation.mutate({ pluginId: row.plugin_id, version: row.version })}
                          onDelete={() => setDeleteTarget({ pluginId: row.plugin_id, version: row.version })}
                          t={t}
                        />
                      ))}
                    </tbody>
                  </table>
                )}

                {enabledVersionRow && managePlugin?.scope === 'endpoint' ? (
                  <section className="space-y-3 border-t border-separator pt-4">
                    <FormField
                      label={t('settings:jsPlugins.bindProtocolTitle')}
                      description={t('settings:jsPlugins.bindProtocolHint')}
                    >
                      <div className="flex flex-wrap gap-2">
                        <Input
                          className="max-w-xs font-mono text-body"
                          placeholder={t('settings:jsPlugins.protocolSlugPlaceholder')}
                          value={protocolSlug}
                          onChange={(e) => setProtocolSlug(e.target.value)}
                        />
                        <Button
                          type="button"
                          size="sm"
                          disabled={busy || !protocolSlug.trim()}
                          onClick={() =>
                            bindProtocolMutation.mutate({
                              pluginId: managePlugin.id,
                              version: enabledVersionRow.version,
                              slug: protocolSlug.trim(),
                            })
                          }
                        >
                          {t('settings:jsPlugins.bindProtocol')}
                        </Button>
                      </div>
                    </FormField>
                  </section>
                ) : null}

                <JSPluginSiteProviderSection versions={detailQuery.data?.versions ?? []} />

                {siteBinding.applicable ? (
                  <JSPluginSiteBindingSection
                    binding={siteBinding}
                    sites={sitesQuery.data ?? []}
                    loading={sitesQuery.isLoading}
                  />
                ) : null}

                {enabledVersionRow && managePlugin?.scope === 'subject' && automationManifest(enabledVersionRow.manifest) ? (
                  <JSPluginAutomationSection
                    pluginId={managePlugin.id}
                    manifest={automationManifest(enabledVersionRow.manifest)!}
                  />
                ) : null}

                {enabledVersionRow && managePlugin?.scope === 'global' ? (
                  <p className="border-t border-separator pt-4 text-body text-label-2">
                    {t(`settings:jsPlugins.globalKindHint.${managePlugin.kind}`)}
                  </p>
                ) : null}
              </>
            )}
          </DialogBody>
          <DialogFooter
            cancel={
              <Button type="button" variant="secondary" onClick={closeManage}>
                {siteBinding.applicable ? t('common:actions.cancel') : t('common:actions.close')}
              </Button>
            }
            confirm={
              siteBinding.applicable ? (
                <Button
                  type="button"
                  disabled={!siteBinding.canApply}
                  onClick={() =>
                    void siteBinding.apply().then((applied) => {
                      if (applied) closeManage()
                    })
                  }
                >
                  {siteBinding.applying ? (
                    <LoaderCircle className="h-4 w-4 animate-spin" />
                  ) : (
                    t('settings:jsPlugins.siteBinding.apply')
                  )}
                </Button>
              ) : undefined
            }
          />
        </DialogContent>
      </Dialog>

      <Dialog
        open={trustedKeysOpen}
        onOpenChange={(open) => {
          setTrustedKeysOpen(open)
          if (!open) setTrustedKeyAddOpen(false)
        }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('settings:jsPlugins.trustedKeysDialogTitle')}</DialogTitle>
            <DialogDescription>{t('settings:jsPlugins.trustedKeysHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {trustedKeysQuery.isLoading ? (
              <div className="flex items-center justify-center gap-2 py-6 text-body text-label-2">
                <LoaderCircle className="h-4 w-4 animate-spin" />
                {t('settings:jsPlugins.loading')}
              </div>
            ) : (trustedKeysQuery.data ?? []).length === 0 ? (
              <p className="text-body text-label-2">{t('settings:jsPlugins.trustedKeysEmpty')}</p>
            ) : (
              <ul className="divide-y divide-separator text-body">
                {(trustedKeysQuery.data ?? []).map((key: JSPluginTrustedKey) => (
                  <li
                    key={key.id}
                    className="flex flex-wrap items-center justify-between gap-2 py-3"
                  >
                    <div className="min-w-0">
                      <div className="font-medium">{key.name}</div>
                      <div className="truncate font-mono text-footnote text-label-2" title={key.fingerprint}>
                        {key.fingerprint}
                      </div>
                    </div>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      className="text-danger hover:text-danger"
                      disabled={busy}
                      aria-label={t('common:actions.delete')}
                      title={t('common:actions.delete')}
                      onClick={() => setTrustedKeyDeleteTarget({ id: key.id, name: key.name })}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={() => setTrustedKeysOpen(false)}>
              {t('common:actions.close')}
            </Button>
            <Button type="button" disabled={busy} onClick={() => setTrustedKeyAddOpen(true)}>
              {t('settings:jsPlugins.trustedKeyAdd')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={uploadOpen}
        onOpenChange={(open) => {
          setUploadOpen(open)
          if (!open) setUploadDragging(false)
        }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('settings:jsPlugins.uploadDialogTitle')}</DialogTitle>
            <DialogDescription>{t('settings:jsPlugins.uploadedHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div
              role="button"
              tabIndex={0}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  fileInputRef.current?.click()
                }
              }}
              onDrop={onUploadDrop}
              onDragOver={(event) => {
                event.preventDefault()
                setUploadDragging(true)
              }}
              onDragLeave={(event) => {
                event.preventDefault()
                setUploadDragging(false)
              }}
              onClick={() => !uploadMutation.isPending && fileInputRef.current?.click()}
              className={cn(
                'flex cursor-pointer flex-col items-center justify-center gap-2 rounded-inner border-2 border-dashed px-4 py-10 text-center transition-colors',
                uploadMutation.isPending && 'pointer-events-none opacity-70',
                uploadDragging
                  ? 'border-tint bg-tint/5'
                  : 'border-separator bg-fill-4 hover:border-tint/50 hover:bg-fill-3',
              )}
            >
              {uploadMutation.isPending ? (
                <LoaderCircle className="h-8 w-8 animate-spin text-label-2" />
              ) : (
                <Upload className="h-8 w-8 text-label-2" />
              )}
              <div className="text-body font-medium text-label">{t('settings:jsPlugins.uploadDropzone')}</div>
              <div className="text-footnote text-label-2">{t('settings:jsPlugins.uploadFileHint')}</div>
              <input
                ref={fileInputRef}
                type="file"
                accept=".xlp,application/zip,application/octet-stream"
                className="hidden"
                onChange={onFileChange}
              />
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={() => setUploadOpen(false)} disabled={uploadMutation.isPending}>
              {t('common:actions.cancel')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={trustedKeyAddOpen}
        onOpenChange={(open) => {
          setTrustedKeyAddOpen(open)
          if (!open) {
            setTrustedKeyName('')
            setTrustedKeyPublic('')
          }
        }}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('settings:jsPlugins.trustedKeyAddDialogTitle')}</DialogTitle>
            <DialogDescription>{t('settings:jsPlugins.trustedKeysHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <FormField
              label={t('settings:jsPlugins.trustedKeyNameLabel')}
              htmlFor="js-plugin-trusted-key-name"
              required
            >
              <Input
                id="js-plugin-trusted-key-name"
                placeholder={t('settings:jsPlugins.trustedKeyNamePlaceholder')}
                value={trustedKeyName}
                onChange={(e) => setTrustedKeyName(e.target.value)}
              />
            </FormField>
            <FormField
              label={t('settings:jsPlugins.trustedKeyPublicLabel')}
              description={t('settings:jsPlugins.trustedKeyPublicHint')}
              htmlFor="js-plugin-trusted-key-public"
              required
            >
              <Input
                id="js-plugin-trusted-key-public"
                className="font-mono text-footnote"
                placeholder={t('settings:jsPlugins.trustedKeyPublicPlaceholder')}
                value={trustedKeyPublic}
                onChange={(e) => setTrustedKeyPublic(e.target.value)}
              />
            </FormField>
          </DialogBody>
          <DialogFooter
            cancel={
              <Button type="button" variant="secondary" onClick={() => setTrustedKeyAddOpen(false)}>
                {t('common:actions.cancel')}
              </Button>
            }
            confirm={
              <Button
                type="button"
                disabled={busy || !trustedKeyName.trim() || !trustedKeyPublic.trim() || addTrustedKeyMutation.isPending}
                onClick={() => addTrustedKeyMutation.mutate()}
              >
                {addTrustedKeyMutation.isPending ? (
                  <LoaderCircle className="h-4 w-4 animate-spin" />
                ) : (
                  t('common:actions.save')
                )}
              </Button>
            }
          />
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget != null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent presentation="center">
          <DialogHeader>
            <DialogTitle>{t('settings:jsPlugins.deleteConfirmTitle')}</DialogTitle>
            <DialogDescription>
              {t('settings:jsPlugins.deleteConfirmBody', {
                id: deleteTarget?.pluginId,
                version: deleteTarget?.version,
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={() => setDeleteTarget(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={deleteMutation.isPending || !deleteTarget}
              onClick={() => deleteTarget && deleteMutation.mutate(deleteTarget)}
            >
              {deleteMutation.isPending ? (
                <LoaderCircle className="h-4 w-4 animate-spin" />
              ) : (
                <Trash2 className="mr-2 h-4 w-4" />
              )}
              {t('settings:jsPlugins.uninstall')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={uninstallTarget != null} onOpenChange={(open) => !open && setUninstallTarget(null)}>
        <DialogContent presentation="center">
          <DialogHeader>
            <DialogTitle>{t('settings:jsPlugins.uninstallAllTitle')}</DialogTitle>
            <DialogDescription>
              {t('settings:jsPlugins.uninstallAllBody', { name: uninstallTarget?.name, count: uninstallTarget?.versions })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={() => setUninstallTarget(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={uninstallMutation.isPending || !uninstallTarget}
              onClick={() => uninstallTarget && uninstallMutation.mutate({ pluginId: uninstallTarget.pluginId })}
            >
              {uninstallMutation.isPending ? (
                <LoaderCircle className="h-4 w-4 animate-spin" />
              ) : (
                <Trash2 className="mr-2 h-4 w-4" />
              )}
              {t('settings:jsPlugins.uninstall')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={trustedKeyDeleteTarget != null}
        onOpenChange={(open) => {
          if (!open && !deleteTrustedKeyMutation.isPending) setTrustedKeyDeleteTarget(null)
        }}
      >
        <DialogContent presentation="center" className="w-[min(92vw,520px)] overflow-hidden">
          <DialogHeader className="border-b-0 pb-2">
            <DialogTitle>{t('settings:jsPlugins.trustedKeyDeleteTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody className="pt-0 text-body text-label-2">
            <DialogDescription className="mt-0">
              {t('settings:jsPlugins.trustedKeyDeleteBody', { name: trustedKeyDeleteTarget?.name })}
            </DialogDescription>
          </DialogBody>
          <DialogFooter
            className="border-t-0 pt-2"
            cancel={
              <Button variant="outline" onClick={() => setTrustedKeyDeleteTarget(null)} disabled={deleteTrustedKeyMutation.isPending}>
                {t('common:actions.cancel')}
              </Button>
            }
            confirm={
              <Button
                variant="destructive"
                disabled={deleteTrustedKeyMutation.isPending}
                onClick={() => {
                  if (!trustedKeyDeleteTarget) return
                  deleteTrustedKeyMutation.mutate(trustedKeyDeleteTarget.id, {
                    onSettled: () => setTrustedKeyDeleteTarget(null),
                  })
                }}
              >
                {deleteTrustedKeyMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : null}
                {t('common:actions.delete')}
              </Button>
            }
          />
        </DialogContent>
      </Dialog>

      <Dialog
        open={enableFlow?.dialog != null}
        onOpenChange={(open) => {
          if (!open) dismissEnableDialog()
        }}
      >
        <DialogContent presentation="center">
          {enableFlow?.dialog === 'hosts' ? (
            <>
              <DialogHeader>
                <DialogTitle>{t('settings:jsPlugins.hostsTitle')}</DialogTitle>
                <DialogDescription>{t('settings:jsPlugins.hostsBody')}</DialogDescription>
              </DialogHeader>
              <DialogBody>
                <ul className="space-y-1 font-mono text-footnote text-label">
                  {enableFlow.hosts.map((host) => (
                    <li key={host} className="break-all">{host}</li>
                  ))}
                </ul>
              </DialogBody>
              <DialogFooter>
                <Button type="button" variant="secondary" onClick={dismissEnableDialog}>
                  {t('common:actions.cancel')}
                </Button>
                <Button type="button" disabled={enableMutation.isPending} onClick={submitEnableDialog}>
                  {t('common:actions.confirm')}
                </Button>
              </DialogFooter>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>
                  {offerUntrusted
                    ? enableFlow?.trust === 'untrusted_signer'
                      ? t('settings:jsPlugins.untrustedTitle')
                      : t('settings:jsPlugins.unsignedTitle')
                    : t('settings:jsPlugins.permissionsTitle')}
                </DialogTitle>
                <DialogDescription>
                  {offerUntrusted
                    ? enableFlow?.trust === 'untrusted_signer'
                      ? t('settings:jsPlugins.untrustedBody')
                      : t('settings:jsPlugins.unsignedBody')
                    : t('settings:jsPlugins.permissionsBody')}
                  {enableFlow?.signer ? (
                    <span className="mt-2 block font-mono text-footnote">{enableFlow.signer}</span>
                  ) : null}
                </DialogDescription>
              </DialogHeader>
              <DialogBody>
                {enableFlow && enableFlow.permissions.length > 0 ? (
                  <div className="mb-4 space-y-2">
                    <p className="text-body font-medium text-label">{t('settings:jsPlugins.permissionsList')}</p>
                    <ul className="space-y-1.5 text-body">
                      {enableFlow.permissions.map((permission) => {
                        const key = permissionLabelKey(permission)
                        return (
                          <li key={permission} className="flex flex-col">
                            <span className="font-mono text-footnote text-label">{permission}</span>
                            {key ? <span className="text-footnote text-label-2">{t(key)}</span> : null}
                          </li>
                        )
                      })}
                    </ul>
                    <p className="text-footnote text-label-2">{t('settings:jsPlugins.permissionsScopeHint')}</p>
                  </div>
                ) : null}
                <label className="flex items-start gap-3 text-body">
                  <Checkbox checked={enableConfirmed} onCheckedChange={(v) => setEnableConfirmed(v === true)} />
                  <span>
                    {offerUntrusted
                      ? t('settings:jsPlugins.enableConfirm')
                      : t('settings:jsPlugins.permissionsConfirm')}
                  </span>
                </label>
              </DialogBody>
              <DialogFooter>
                <Button type="button" variant="secondary" onClick={dismissEnableDialog}>
                  {t('common:actions.cancel')}
                </Button>
                <Button
                  type="button"
                  disabled={!enableConfirmed || !enableFlow || enableMutation.isPending}
                  onClick={submitEnableDialog}
                >
                  {t('settings:jsPlugins.enable')}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>

      <MarketSubmitDialog
        key={submitPlugin?.id ?? 'closed'}
        plugin={submitPlugin}
        hosts={siteProviderOf(submitVersion?.manifest)?.hosts ?? []}
        permissions={automationManifest(submitVersion?.manifest)?.permissions ?? []}
        signer={submitVersion?.signer ?? ''}
        summaryLoading={submitDetailQuery.isLoading}
        submitting={submitMutation.isPending}
        error={submitError}
        onClose={() => {
          setSubmitPlugin(null)
          setSubmitError('')
        }}
        onSubmit={(changelog) => {
          setSubmitError('')
          submitMutation.mutate(changelog)
        }}
      />

    </div>
  )
}

type VersionRowProps = {
  row: JSPluginVersion
  trial?: TrialState
  busy: boolean
  tryPending: boolean
  onEnable: () => void
  onDisable: () => void
  onTry: () => void
  onDelete: () => void
  t: TFunction
}

type TrialState = { ok: boolean; durationUs?: number; error?: string }

function trialKey(pluginId: string, version: string) {
  return `${pluginId}@${version}`
}

// A fixture run takes well under a millisecond, so milliseconds alone would
// always read 0. Show microseconds up to 1 ms, then milliseconds to the microsecond.
function formatDurationUs(us: number) {
  if (us < 1000) return `${us} µs`
  if (us < 1_000_000) return `${(us / 1000).toFixed(3)} ms`
  return `${(us / 1_000_000).toFixed(3)} s`
}

function TrialStatus({ trial, t }: { trial: TrialState; t: TFunction }) {
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={trial.ok ? 'success' : 'error'}>
          {trial.ok ? t('settings:jsPlugins.trialPassed') : t('settings:jsPlugins.trialFailed')}
        </StatusBadge>
        {trial.durationUs != null ? (
          <span className="font-mono tabular-nums text-label-2">{formatDurationUs(trial.durationUs)}</span>
        ) : null}
      </div>
      {trial.error ? <p className="whitespace-pre-wrap break-words text-destructive">{trial.error}</p> : null}
    </div>
  )
}

function versionTrustBadge(row: JSPluginVersion, t: TFunction) {
  let badge = <StatusBadge status="idle">{t('settings:jsPlugins.unsigned')}</StatusBadge>
  if (row.trust === 'marketplace') badge = <StatusBadge status="success">{t('settings:jsPlugins.trustMarketplace')}</StatusBadge>
  else if (row.trust === 'trusted') badge = <StatusBadge status="success">{t('settings:jsPlugins.trustTrusted')}</StatusBadge>
  else if (row.trust === 'untrusted_signer') badge = <StatusBadge status="warning">{t('settings:jsPlugins.trustUntrusted')}</StatusBadge>
  else if (row.signed) badge = <StatusBadge status="idle">{t('settings:jsPlugins.signed')}</StatusBadge>
  return (
    <div className="flex flex-wrap items-center gap-1">
      {badge}
      {row.signer_revoked ? <span className="text-footnote text-danger">{t('settings:jsPlugins.market.signerRevoked')}</span> : null}
    </div>
  )
}

function InstalledUpdateMarks({ plugin }: { plugin: JSPluginListItem }) {
  const { t } = useTranslation('settings')
  const reason = updateReasonText((key, options) => t(`settings:${key}`, options), plugin.auto_block_reason, plugin.auto_block_detail)
  if (!plugin.update_available && !plugin.auto_rolled_back && !reason && !plugin.deprecated) return null
  return (
    <div className="mt-1 space-y-1">
      {plugin.update_available ? (
        <StatusBadge status="warning">
          {plugin.update_version
            ? t('settings:jsPlugins.market.updateTo', { version: plugin.update_version })
            : t('settings:jsPlugins.market.badgeUpdate')}
        </StatusBadge>
      ) : null}
      {plugin.auto_rolled_back ? <StatusBadge status="error">{t('settings:jsPlugins.market.autoRolledBack')}</StatusBadge> : null}
      {plugin.deprecated ? <StatusBadge status="warning">{t('settings:jsPlugins.market.level.deprecated')}</StatusBadge> : null}
      {reason ? <p className="line-clamp-2 text-footnote text-label-2">{reason}</p> : null}
    </div>
  )
}

function installedTrustBadge(plugin: JSPluginListItem, t: TFunction) {
  if (plugin.trust === 'marketplace') return <StatusBadge status="success">{t('settings:jsPlugins.trustMarketplace')}</StatusBadge>
  if (plugin.trust === 'trusted') return <StatusBadge status="success">{t('settings:jsPlugins.trustTrusted')}</StatusBadge>
  if (plugin.trust === 'untrusted_signer') return <StatusBadge status="warning">{t('settings:jsPlugins.trustUntrusted')}</StatusBadge>
  return null
}

function InstalledPluginCard({
  plugin,
  busy,
  developerUnavailable,
  onSubmit,
  onUpdate,
  onManage,
  onDisable,
  onUninstall,
}: {
  plugin: JSPluginListItem
  busy: boolean
  developerUnavailable: boolean
  onSubmit: (plugin: JSPluginListItem) => void
  onUpdate: () => void
  onManage: () => void
  onDisable: () => void
  onUninstall: () => void
}) {
  const { t } = useTranslation('settings')
  const reason = updateReasonText((key, options) => t(`settings:${key}`, options), plugin.auto_block_reason, plugin.auto_block_detail)
  return (
    <ItemCard>
      <div className="flex min-w-0 items-start gap-3">
        <PluginAvatar size="md" iconUrl={plugin.icon_url} name={plugin.name || plugin.id} />
        <div className="flex h-10 min-w-0 flex-1 flex-col justify-between">
          <div className="flex min-w-0 items-center gap-2">
            <h3 className="min-w-0 flex-1 truncate text-headline leading-5 text-label" title={plugin.name}>{plugin.name || '—'}</h3>
            <div className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
              {installedTrustBadge(plugin, t)}
              {plugin.update_available ? (
                <StatusBadge status="warning">
                  {plugin.update_version
                    ? t('settings:jsPlugins.market.updateTo', { version: plugin.update_version })
                    : t('settings:jsPlugins.market.badgeUpdate')}
                </StatusBadge>
              ) : null}
              {plugin.auto_rolled_back ? <StatusBadge status="error">{t('settings:jsPlugins.market.autoRolledBack')}</StatusBadge> : null}
              {plugin.deprecated ? <StatusBadge status="warning">{t('settings:jsPlugins.market.level.deprecated')}</StatusBadge> : null}
              {plugin.signer_revoked ? <StatusBadge status="warning">{t('settings:jsPlugins.market.signerRevoked')}</StatusBadge> : null}
            </div>
          </div>
          <p className="truncate font-mono text-footnote leading-4 text-label-2" title={plugin.id}>{plugin.id}</p>
        </div>
      </div>
      {reason ? <p className="line-clamp-2 text-footnote text-label-2">{reason}</p> : null}
      <CardMetrics columns={3}>
        <CardMetric label={t('settings:jsPlugins.columns.kind')} value={kindLabel(t, plugin.kind ?? '')} />
        <CardMetric
          label={t('settings:jsPlugins.columns.enabledVersion')}
          value={plugin.enabled_version ? <StatusBadge status="success">{plugin.enabled_version}</StatusBadge> : <span className="text-label-2">—</span>}
        />
        <CardMetric label={t('settings:jsPlugins.columns.metrics24h')} value={<PluginMetrics24h metrics={plugin.metrics_24h} />} />
      </CardMetrics>
      <CardFooter className="flex-wrap gap-2">
        <MarketSubmitActions plugin={plugin} busy={busy} developerUnavailable={developerUnavailable} onOpen={onSubmit} />
        {plugin.update_available ? (
          <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={onUpdate}>
            {t('settings:jsPlugins.market.update')}
          </Button>
        ) : null}
        <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={onManage}>
          {t('settings:jsPlugins.manageVersions')}
        </Button>
        {plugin.enabled_version ? (
          <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={onDisable}>
            {t('settings:jsPlugins.disable')}
          </Button>
        ) : (
          <Button type="button" size="sm" variant="secondary" className="text-danger hover:text-danger" disabled={busy} onClick={onUninstall}>
            {t('settings:jsPlugins.uninstall')}
          </Button>
        )}
      </CardFooter>
    </ItemCard>
  )
}

function selftestSummary(row: JSPluginVersion, t: TFunction) {
  const ok = row.selftest?.ok
  const error = row.selftest?.error
  if (ok === true) return t('settings:jsPlugins.selftestOk')
  return error || (ok === false ? t('settings:jsPlugins.selftestFailed') : '—')
}

function VersionActions({ row, busy, tryPending, onEnable, onDisable, onTry, onDelete, t }: VersionRowProps) {
  return (
    <>
      <Button type="button" size="sm" variant="secondary" disabled={busy || tryPending} onClick={onTry}>
        {tryPending ? <LoaderCircle className="mr-1 h-3.5 w-3.5 animate-spin" /> : null}
        {t('settings:jsPlugins.try')}
      </Button>
      {row.status === 'enabled' ? (
        <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={onDisable}>
          {t('settings:jsPlugins.disable')}
        </Button>
      ) : null}
      {row.status !== 'enabled' ? (
        <Button type="button" size="sm" disabled={busy || row.status === 'broken'} onClick={onEnable}>
          {t('settings:jsPlugins.enable')}
        </Button>
      ) : null}
      {row.status !== 'enabled' ? (
        <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={onDelete}>
          {t('settings:jsPlugins.uninstall')}
        </Button>
      ) : null}
    </>
  )
}

/** Desktop: every action of a version behind one vertical-dots button. */
function VersionActionsMenu({ row, busy, tryPending, onEnable, onDisable, onTry, onDelete, t }: VersionRowProps) {
  const items: VersionMenuItem[] = [
    { key: 'try', icon: FlaskConical, label: t('settings:jsPlugins.try'), disabled: busy || tryPending, onSelect: onTry },
  ]
  if (row.status === 'enabled') {
    items.push({ key: 'disable', icon: PowerOff, label: t('settings:jsPlugins.disable'), disabled: busy, onSelect: onDisable })
  } else {
    items.push(
      { key: 'enable', icon: Power, label: t('settings:jsPlugins.enable'), disabled: busy || row.status === 'broken', onSelect: onEnable },
      { key: 'uninstall', icon: Trash2, label: t('settings:jsPlugins.uninstall'), disabled: busy, destructive: true, onSelect: onDelete },
    )
  }
  return (
    <div className="flex justify-end">
      <JSPluginVersionMenu items={items} pending={tryPending} label={t('settings:jsPlugins.columns.actions')} />
    </div>
  )
}

function VersionRow(props: VersionRowProps) {
  const { row, t } = props
  const manifestName = manifestString(row.manifest, 'name')
  const manifestKind = manifestString(row.manifest, 'kind')

  return (
    <tr className="border-b border-separator/60 last:border-0">
      <td className="px-3 py-2">
        <div className="font-mono text-body">{row.version}</div>
        {row.auto_rolled_back ? <StatusBadge status="error">{t('settings:jsPlugins.market.autoRolledBack')}</StatusBadge> : null}
        {row.revocation?.revoked ? (
          <StatusBadge status={row.revocation.level === 'security' ? 'error' : 'warning'}>
            {row.revocation.level === 'deprecated' ? t('settings:jsPlugins.market.level.deprecated') : t('settings:jsPlugins.market.revoked')}
          </StatusBadge>
        ) : null}
        {manifestName ? <div className="text-footnote text-label-2">{manifestName}</div> : null}
        {manifestKind ? <div className="text-footnote text-label-2">{kindLabel(t, manifestKind)}</div> : null}
      </td>
      <td className="px-3 py-2">{versionStatusBadge(row.status)}</td>
      <td className="px-3 py-2 text-footnote">
        <div>{versionTrustBadge(row, t)}</div>
        <div className="mt-1 font-mono text-label-2" title={row.package_sha256}>
          {shortSha256(row.package_sha256)}
        </div>
      </td>
      <td className="max-w-[180px] px-3 py-2 text-footnote text-label-2" title={row.selftest?.error ?? undefined}>
        <span className="line-clamp-2">{selftestSummary(row, t)}</span>
        {props.trial ? (
          <div className="mt-2">
            <TrialStatus trial={props.trial} t={t} />
          </div>
        ) : null}
      </td>
      <td className="w-px px-3 py-2 text-right">
        <VersionActionsMenu {...props} />
      </td>
    </tr>
  )
}

function VersionCard(props: VersionRowProps) {
  const { row, t } = props
  const manifestName = manifestString(row.manifest, 'name')
  const manifestKind = manifestString(row.manifest, 'kind')

  return (
    <MobileVersionCard
      version={row.version}
      name={manifestName || undefined}
      kind={manifestKind ? kindLabel(t, manifestKind) : undefined}
      status={(
        <div className="flex flex-wrap items-center gap-1">
          {versionStatusBadge(row.status)}
          {row.auto_rolled_back ? <StatusBadge status="error">{t('settings:jsPlugins.market.autoRolledBack')}</StatusBadge> : null}
          {row.revocation?.revoked ? (
            <StatusBadge status={row.revocation.level === 'security' ? 'error' : 'warning'}>
              {row.revocation.level === 'deprecated' ? t('settings:jsPlugins.market.level.deprecated') : t('settings:jsPlugins.market.revoked')}
            </StatusBadge>
          ) : null}
        </div>
      )}
      trust={versionTrustBadge(row, t)}
      sha={shortSha256(row.package_sha256)}
      selftest={selftestSummary(row, t)}
      trial={props.trial ? <TrialStatus trial={props.trial} t={t} /> : undefined}
      labels={{
        package: t('settings:jsPlugins.columns.package'),
        selftest: t('settings:jsPlugins.columns.selftest'),
        trial: t('settings:jsPlugins.columns.trial'),
      }}
      actions={<VersionActions {...props} />}
    />
  )
}
