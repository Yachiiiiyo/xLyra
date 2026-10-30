import { useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { format, formatDistanceToNow, parseISO } from 'date-fns'
import { enUS, ja, zhCN } from 'date-fns/locale'
import type { Locale } from 'date-fns'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'
import { EmptyState } from '@/components/common/empty-state'
import { MobileFilterBar } from '@/components/common/mobile-filter-bar'
import { StatusBadge } from '@/components/common/status-badge'
import { TableToolbar } from '@/components/common/table-toolbar'
import { Button } from '@/components/ui/button'
import { CardFooter, CardMetric, CardMetrics, ItemCard } from '@/components/ui/card'
import { useOverlayMode } from '@/components/ui/overlay-mode'
import { SplitButton, type SplitButtonItem } from '@/components/ui/split-button'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  fetchPluginMarket,
  fetchPluginMarketPlugin,
  pluginMarketQueryKeys,
  syncPluginMarket,
  type PluginMarketItem,
  type PluginMarketPlugin,
  type PluginMarketSnapshot,
  type PluginMarketVersion,
} from '@/features/settings/api/plugin-market'
import { jsPluginKindLabel } from '@/features/settings/components/js-plugins/kind-label'
import { MobileCardList } from '@/features/settings/components/js-plugins/js-plugins-mobile'
import { PluginAvatar } from '@/features/settings/components/js-plugins/plugin-avatar'
import { useMobileLayout } from '@/hooks/use-media-query'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'
import { filterMarketPlugins } from './filter'
import { reasonText, useMarketInstall, type MarketInstallState } from './install-control'
import { updateReasonText } from './update-reason'
import { PlainText } from './plain-text'
import { sanitizeErrorText } from './sanitize-error'

const EMPTY_PLUGINS: PluginMarketItem[] = []

const KIND_ORDER = [
  'quota_probe',
  'protocol',
  'model_list',
  'credential_check',
  'site_detect',
  'error_classifier',
  'model_metadata',
  'pricing_parse',
  'automation',
  'site_provider',
]

export function PluginMarketPanel() {
  const { t, i18n } = useTranslation('settings')
  const queryClient = useQueryClient()
  const isMobile = useMobileLayout()
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState('all')
  const [showIncompatible, setShowIncompatible] = useState(false)
  const [openId, setOpenId] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: pluginMarketQueryKeys.list(),
    queryFn: ({ signal }) => fetchPluginMarket(signal),
  })
  const detailQuery = useQuery({
    queryKey: pluginMarketQueryKeys.detail(openId ?? ''),
    queryFn: ({ signal }) => fetchPluginMarketPlugin(openId!, signal),
    enabled: openId != null,
  })
  const sync = useMutation({
    mutationFn: syncPluginMarket,
    onSuccess: async (status) => {
      if (status.error) toast.error(sanitizeErrorText(status.error))
      await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.list() })
      if (openId) await queryClient.invalidateQueries({ queryKey: pluginMarketQueryKeys.detail(openId) })
    },
    onError: (error) => {
      toast.error(error instanceof APIError ? sanitizeErrorText(error.message) : t('jsPlugins.market.syncFailed'))
    },
  })

  const plugins = listQuery.data?.plugins ?? EMPTY_PLUGINS
  const snapshot = listQuery.data?.snapshot
  const visible = useMemo(
    () => filterMarketPlugins(plugins, { query, kind, showIncompatible }),
    [plugins, query, kind, showIncompatible],
  )
  const kinds = useMemo(() => {
    const present = new Set(plugins.map((plugin) => plugin.kind))
    const known = KIND_ORDER.filter((item) => present.has(item))
    const extra = [...present].filter((item) => !KIND_ORDER.includes(item)).sort()
    return [...known, ...extra]
  }, [plugins])

  const openPlugin = plugins.find((plugin) => plugin.id === openId)
  const detail = detailQuery.data ?? (openPlugin ? { ...openPlugin, versions: [] } : null)
  const statusBar = snapshot ? (
    <MarketStatusBar
      snapshot={snapshot}
      language={i18n.language}
      syncing={sync.isPending}
      onSync={() => sync.mutate()}
    />
  ) : undefined

  return (
    <div className="space-y-4">
      {listQuery.isError ? <p className="text-body text-danger">{t('jsPlugins.market.loadFailed')}</p> : null}
      {snapshot && !snapshot.root_keys_configured ? (
        <>
          {statusBar}
          <p className="rounded-card border border-separator bg-fill-4 px-4 py-3 text-body text-label">{t('jsPlugins.market.closed')}</p>
        </>
      ) : null}
      {snapshot?.revocations_expired ? (
        <p className="rounded-card border border-danger/40 bg-danger/10 px-4 py-3 text-body text-danger">
          {t('jsPlugins.market.revocationsExpired')}
        </p>
      ) : null}
      {snapshot?.root_keys_configured ? (
        <>
          <MarketFilters
            mobile={isMobile}
            query={query}
            kind={kind}
            kinds={kinds}
            showIncompatible={showIncompatible}
            onQuery={setQuery}
            onKind={setKind}
            onShowIncompatible={setShowIncompatible}
            status={statusBar}
          />
          {listQuery.isLoading ? (
            <p className="text-body text-label-2">{t('jsPlugins.loading')}</p>
          ) : visible.length === 0 ? (
            <EmptyState title={t('jsPlugins.market.empty')} description={t('jsPlugins.market.emptyHint')} />
          ) : (
            <MarketPluginList plugins={visible} snapshot={snapshot} mobile={isMobile} onOpen={setOpenId} />
          )}
        </>
      ) : null}

      <Dialog open={openId != null} onOpenChange={(open) => { if (!open) setOpenId(null) }}>
        <DialogContent size="lg">
          <DialogHeader>
            <div data-testid="market-detail-heading" className="flex min-w-0 items-center gap-3">
              <PluginAvatar size="lg" iconUrl={detail?.icon_url} name={detail?.name || detail?.id || openId || ''} />
              <div className="flex h-12 min-w-0 flex-1 flex-col justify-between">
                <DialogTitle className="truncate leading-6">{detail?.name || openId}</DialogTitle>
                <DialogDescription className="mt-0 truncate font-mono leading-5">{openId}</DialogDescription>
              </div>
            </div>
          </DialogHeader>
          <DialogBody data-testid="market-detail-body">
            {detailQuery.isLoading ? <p className="text-body text-label-2">{t('jsPlugins.loading')}</p> : null}
            {detailQuery.isError ? <p className="text-body text-danger">{t('jsPlugins.market.loadFailed')}</p> : null}
            {detail && !detailQuery.isLoading && !detailQuery.isError ? (
              <MarketPluginDetail plugin={detail} />
            ) : null}
          </DialogBody>
          {detail && !detailQuery.isLoading && !detailQuery.isError ? (
            <DialogFooter data-testid="market-detail-footer">
              <MarketInstallActions plugin={detail} snapshot={snapshot} onClose={() => setOpenId(null)} />
            </DialogFooter>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  )
}

function MarketFilters({
  mobile,
  query,
  kind,
  kinds,
  showIncompatible,
  status,
  onQuery,
  onKind,
  onShowIncompatible,
}: {
  mobile: boolean
  query: string
  kind: string
  kinds: string[]
  showIncompatible: boolean
  status?: ReactNode
  onQuery: (value: string) => void
  onKind: (value: string) => void
  onShowIncompatible: (value: boolean) => void
}) {
  const { t } = useTranslation('settings')
  const kindFilter = (
    <Select value={kind} onValueChange={onKind}>
      <SelectTrigger variant="filter" filterLabel={t('jsPlugins.columns.kind')} active={kind !== 'all'} aria-label={t('jsPlugins.columns.kind')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent searchable={false} widthMode="content">
        <SelectItem value="all">{t('jsPlugins.market.kindAll')}</SelectItem>
        {kinds.map((item) => (
          <SelectItem key={item} value={item}>
            {jsPluginKindLabel(t, item)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
  const incompatibleFilter = (
    <div className="shrink-0 whitespace-nowrap">
      <Checkbox
        checked={showIncompatible}
        label={t('jsPlugins.market.showIncompatible')}
        onCheckedChange={onShowIncompatible}
      />
    </div>
  )

  if (mobile) {
    return (
      <div className="space-y-3">
        <MobileFilterBar
          search={{
            value: query,
            onChange: (event) => onQuery(event.target.value),
            placeholder: t('jsPlugins.market.search'),
          }}
          filters={(
            <>
              {kindFilter}
              {incompatibleFilter}
            </>
          )}
        />
        {status}
      </div>
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-3">
      <TableToolbar
        className="min-w-0 flex-1"
        searchValue={query}
        onSearchChange={(event) => onQuery(event.target.value)}
        searchPlaceholder={t('jsPlugins.market.search')}
        searchClassName="flex-none md:w-52"
        filtersClassName="flex min-w-0 flex-1 flex-wrap items-center gap-3 md:flex md:auto-cols-auto"
        filters={(
          <>
            {kindFilter}
            {incompatibleFilter}
          </>
        )}
      />
      {status ? <div className="ml-auto shrink-0">{status}</div> : null}
    </div>
  )
}

function distanceLocale(language: string): Locale {
  if (language.startsWith('zh')) return zhCN
  if (language.startsWith('ja') || language.startsWith('jp')) return ja
  return enUS
}

export function MarketStatusBar({
  snapshot,
  language,
  syncing,
  onSync,
}: {
  snapshot: PluginMarketSnapshot
  language: string
  syncing: boolean
  onSync: () => void
}) {
  const { t } = useTranslation('settings')
  const synced = snapshot.index_synced_at
    ? formatDistanceToNow(new Date(snapshot.index_synced_at), { addSuffix: true, locale: distanceLocale(language) })
    : t('jsPlugins.market.neverSynced')
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="text-footnote text-label-2">
          {t('jsPlugins.market.lastSync')}：{synced}
        </span>
        <Button type="button" size="sm" variant="secondary" disabled={syncing} onClick={onSync}>
          <RefreshCw className={cn('mr-2 h-4 w-4', syncing && 'animate-spin')} />
          {syncing ? t('jsPlugins.market.syncing') : t('jsPlugins.market.syncNow')}
        </Button>
      </div>
      {snapshot.last_error ? (
        <p className="break-all text-footnote text-danger">{sanitizeErrorText(snapshot.last_error)}</p>
      ) : null}
    </div>
  )
}

export function MarketPluginList({
  plugins,
  snapshot,
  mobile,
  onOpen,
}: {
  plugins: PluginMarketItem[]
  snapshot?: PluginMarketSnapshot
  mobile: boolean
  onOpen: (id: string) => void
}) {
  const cards = plugins.map((plugin) => (
    <MarketPluginCard key={plugin.id} plugin={plugin} snapshot={snapshot} mobile={mobile} onOpen={onOpen} />
  ))
  if (mobile) return <MobileCardList>{cards}</MobileCardList>
  return <div className="grid grid-cols-2 gap-3 2xl:grid-cols-3">{cards}</div>
}

function MarketPluginCard({
  plugin,
  snapshot,
  mobile,
  onOpen,
}: {
  plugin: PluginMarketItem
  snapshot?: PluginMarketSnapshot
  mobile: boolean
  onOpen: (id: string) => void
}) {
  const { t } = useTranslation('settings')
  const install = useMarketInstall(plugin, snapshot)
  const author = plugin.developer.display_name || plugin.developer.namespace || '—'
  const wide = mobile ? 'w-full min-w-0' : undefined
  return (
    <ItemCard>
      <div className="flex min-w-0 items-start gap-3">
        <PluginAvatar size="md" iconUrl={plugin.icon_url} name={plugin.name || plugin.id} />
        <div className="flex h-10 min-w-0 flex-1 flex-col justify-between">
          <div className="flex min-w-0 items-center gap-2">
            <h3 className="min-w-0 flex-1 truncate text-headline leading-5 text-label">{plugin.name || plugin.id}</h3>
            <div className="flex shrink-0 items-center gap-1.5">
              <MarketBadges plugin={plugin} />
            </div>
          </div>
          <p className="truncate text-footnote leading-4 text-label-2">{author}</p>
        </div>
      </div>
      {plugin.summary ? <PlainText text={plugin.summary} className="line-clamp-2 text-body text-label-2" /> : null}
      <MarketUpdateNote plugin={plugin} />
      <CardMetrics>
        <CardMetric label={t('jsPlugins.columns.kind')} value={jsPluginKindLabel(t, plugin.kind)} />
        <CardMetric label={t('jsPlugins.market.latest')} value={plugin.latest_compatible_version || '—'} />
      </CardMetrics>
      <CardFooter className={mobile ? 'grid grid-cols-2 gap-2' : 'gap-2'}>
        <Button type="button" size="sm" variant="secondary" className={wide} onClick={() => onOpen(plugin.id)}>
          {t('jsPlugins.market.viewDetail')}
        </Button>
        <MarketInstallButtons install={install} menuSide="bottom" showInstalled className={wide} />
      </CardFooter>
    </ItemCard>
  )
}

export function MarketBadges({ plugin }: { plugin: PluginMarketItem }) {
  const { t } = useTranslation('settings')
  return (
    <>
      {plugin.local_status === 'installed' ? <StatusBadge status="idle">{t('jsPlugins.market.badgeInstalled')}</StatusBadge> : null}
      {plugin.local_status === 'enabled' ? <StatusBadge status="success">{t('jsPlugins.market.badgeEnabled')}</StatusBadge> : null}
      {plugin.update_available ? (
        <StatusBadge status="warning">
          {plugin.update_version ? t('jsPlugins.market.updateTo', { version: plugin.update_version }) : t('jsPlugins.market.badgeUpdate')}
        </StatusBadge>
      ) : null}
      {plugin.auto_rolled_back ? <StatusBadge status="error">{t('jsPlugins.market.autoRolledBack')}</StatusBadge> : null}
      {!plugin.has_compatible_version ? <StatusBadge status="disabled">{t('jsPlugins.market.badgeIncompatible')}</StatusBadge> : null}
      {plugin.has_revoked_version ? <StatusBadge status="error">{t('jsPlugins.market.badgeRevoked')}</StatusBadge> : null}
      {plugin.signer_revoked ? <StatusBadge status="warning">{t('jsPlugins.market.signerRevoked')}</StatusBadge> : null}
    </>
  )
}

export function MarketPluginDetail({
  plugin,
}: {
  plugin: PluginMarketPlugin
}) {
  const { t, i18n } = useTranslation('settings')
  const featured = plugin.versions?.find((version) => version.version === plugin.latest_compatible_version) ?? plugin.versions?.[0]
  const hosts = featured?.hosts ?? []
  const permissions = featured?.permissions ?? []
  const author = plugin.developer.display_name || '—'
  return (
    <div className="space-y-5">
      {plugin.summary ? <PlainText text={plugin.summary} className="text-body text-label" /> : null}
      <MarketUpdateNote plugin={plugin} />
      {plugin.homepage ? (
        /^https?:\/\//i.test(plugin.homepage) ? (
          <a className="break-all text-body text-tint" href={plugin.homepage} target="_blank" rel="noopener noreferrer">
            {plugin.homepage}
          </a>
        ) : (
          <PlainText text={plugin.homepage} className="text-body text-label" />
        )
      ) : null}
      <div className="text-footnote text-label-2">
        {t('jsPlugins.market.author')}：{author}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <NoticeList title={t('jsPlugins.market.hosts')} items={hosts} empty={t('jsPlugins.market.none')} />
        <NoticeList title={t('jsPlugins.market.permissions')} items={permissions} empty={t('jsPlugins.market.none')} />
      </div>
      <div className="space-y-3">
        <h4 className="text-headline text-label">{t('jsPlugins.market.versions')}</h4>
        {(plugin.versions ?? []).map((version) => (
          <MarketVersionRow key={version.version} version={version} installedVersion={plugin.local_version} language={i18n.language} />
        ))}
      </div>
    </div>
  )
}

function MarketInstallButtons({
  install,
  menuSide,
  showInstalled = false,
  className,
}: {
  install: MarketInstallState
  menuSide: 'top' | 'bottom'
  showInstalled?: boolean
  className?: string
}) {
  const { t } = useTranslation('settings')
  const { model, pending, run, enableInstalled } = install
  const busy = pending != null
  const tip = model.reason || undefined

  if (model.mode === 'enable') {
    return (
      <Button type="button" size="sm" className={className} disabled={model.disabled || busy} title={tip} onClick={enableInstalled}>
        {model.primaryLabel}
      </Button>
    )
  }

  if (model.mode === 'none') {
    if (!showInstalled) return null
    return (
      <Button type="button" size="sm" className={className} disabled title={tip}>
        {model.primaryLabel}
      </Button>
    )
  }

  const menuItem = model.menuItem
  const items: SplitButtonItem[] = menuItem ? [{
    key: menuItem.key,
    label: menuItem.label,
    disabled: model.disabled || busy,
    onSelect: () => { void run(true) },
  }] : []

  return (
    <SplitButton
      size="sm"
      className={className}
      menuLabel={t('jsPlugins.market.moreInstallOptions')}
      disabled={model.disabled || busy}
      loading={busy}
      title={tip}
      side={menuSide}
      align="end"
      items={items}
      onClick={() => { void run(false) }}
    >
      {model.primaryLabel}
    </SplitButton>
  )
}

export function MarketInstallActions({
  plugin,
  snapshot,
  onClose,
}: {
  plugin: PluginMarketPlugin
  snapshot?: PluginMarketSnapshot
  onClose?: () => void
}) {
  const { t } = useTranslation('settings')
  const draw = useOverlayMode() === 'draw'
  const install = useMarketInstall(plugin, snapshot)
  const { model } = install
  const hasAction = model.mode !== 'none'
  const stretch = draw ? 'min-w-0 flex-1' : undefined

  return (
    <div className={cn('flex w-full gap-2', draw ? 'flex-col' : 'flex-wrap items-center justify-end')}>
      {model.reason ? (
        <div className={cn('min-w-0 space-y-1', !draw && 'mr-auto')}>
          <p className="text-body text-label-3">{model.reason}</p>
        </div>
      ) : null}
      <div className={cn('flex min-w-0 gap-2', draw ? 'w-full' : 'shrink-0 items-center')}>
        <Button type="button" size="sm" variant="secondary" className={stretch} onClick={onClose}>
          {t('jsPlugins.market.close')}
        </Button>
        {hasAction ? (
          <MarketInstallButtons install={install} menuSide="top" className={stretch} />
        ) : null}
      </div>
    </div>
  )
}

function MarketUpdateNote({ plugin }: { plugin: PluginMarketItem }) {
  const { t } = useTranslation('settings')
  const reason = updateReasonText(t, plugin.auto_block_reason, plugin.auto_block_detail)
  if (!reason && !plugin.paused_reason) return null
  return (
    <p className="text-footnote text-label-2">
      {plugin.paused_reason ? <span className="mr-2">{t('jsPlugins.market.pausedAuto')}</span> : null}
      {reason || null}
    </p>
  )
}

function NoticeList({ title, items, empty }: { title: string; items: string[]; empty: string }) {
  return (
    <section className="rounded-card bg-fill-3 px-4 py-3">
      <h4 className="text-subhead font-medium text-label">{title}</h4>
      {items.length === 0 ? (
        <p className="mt-2 text-body text-label-2">{empty}</p>
      ) : (
        <ul className="mt-2 space-y-1">
          {items.map((item) => (
            <li key={item} className="break-all font-mono text-body text-label">
              {item}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function MarketVersionRow({ version, installedVersion, language }: { version: PluginMarketVersion; installedVersion?: string; language: string }) {
  const { t } = useTranslation('settings')
  return (
    <article className="border-t border-separator py-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="flex items-center gap-2">
          <span className="font-mono text-headline text-label">{version.version}</span>
          {version.version === installedVersion ? <StatusBadge status="success">{t('jsPlugins.market.badgeInstalled')}</StatusBadge> : null}
        </span>
        <span className="text-footnote text-label-2">{formatPublished(version.published_at, language)}</span>
      </div>
      <p className="mt-1 text-footnote text-label-2">
        {t('jsPlugins.market.compat')}：{version.compatible ? t('jsPlugins.market.compatible') : reasonText(t, version.incompatible_reason)}
      </p>
      {version.revocation?.revoked ? (
        <p className="mt-1 text-footnote text-danger">
          {t('jsPlugins.market.revoked')}
          {version.revocation.level ? ` · ${levelText(t, version.revocation.level)}` : ''}
          {version.revocation.reason ? ` · ${sanitizeErrorText(version.revocation.reason)}` : ''}
        </p>
      ) : null}
      {version.changelog ? (
        <PlainText text={version.changelog} preserveNewlines className="mt-2 text-body text-label" />
      ) : null}
    </article>
  )
}

function levelText(t: (key: string) => string, level: string) {
  if (level === 'security' || level === 'deprecated') return t(`jsPlugins.market.level.${level}`)
  return level
}

function formatPublished(value: string, language: string) {
  if (!value) return '—'
  const parsed = parseISO(value)
  if (Number.isNaN(parsed.getTime())) return value
  return format(parsed, 'yyyy-MM-dd HH:mm', { locale: distanceLocale(language) })
}
