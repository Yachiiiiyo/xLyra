import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  getJSPluginSettings,
  jsPluginQueryKeys,
  updateJSPluginSettings,
  updateJSPluginSiteSettings,
  type JSPluginSettingSpec,
} from '@/features/settings/api/js-plugins'
import { resolveText } from '@/features/settings/lib/localized-text'
import { sitesQueryKeys, type Site } from '@/features/sites/api/sites'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

type SettingValues = Record<string, unknown>

function sameValue(spec: JSPluginSettingSpec, value: unknown) {
  return value === spec.default
}

/** Overrides worth saving: anything the admin set that differs from the manifest default. */
function overridesOf(schema: JSPluginSettingSpec[], draft: SettingValues) {
  const out: SettingValues = {}
  for (const spec of schema) {
    if (!(spec.key in draft)) continue
    const value = draft[spec.key]
    if (value === undefined || sameValue(spec, value)) continue
    out[spec.key] = value
  }
  return out
}

function SettingControl({
  spec,
  value,
  placeholder,
  onChange,
}: {
  spec: JSPluginSettingSpec
  value: unknown
  placeholder?: string
  onChange: (value: unknown | undefined) => void
}) {
  const { i18n } = useTranslation()
  const label = resolveText(spec.label, i18n.language) || spec.key
  const description = resolveText(spec.description, i18n.language)

  if (spec.type === 'boolean') {
    return (
      <Switch
        checked={value === true}
        onCheckedChange={(checked) => onChange(checked)}
        label={label}
        description={description}
      />
    )
  }

  const control = spec.type === 'string' && spec.options && spec.options.length > 0 ? (
    <Select value={typeof value === 'string' ? value : undefined} onValueChange={(next) => onChange(next)}>
      <SelectTrigger>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent searchable={false}>
        {spec.options.map((option) => (
          <SelectItem key={option} value={option}>{option}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  ) : (
    <Input
      type={spec.type === 'number' ? 'number' : 'text'}
      min={spec.type === 'number' ? spec.min : undefined}
      max={spec.type === 'number' ? spec.max : undefined}
      value={value === undefined || value === null ? '' : String(value)}
      placeholder={placeholder}
      onChange={(event) => {
        const text = event.target.value
        if (text === '') {
          onChange(undefined)
          return
        }
        if (spec.type === 'number') {
          const parsed = Number(text)
          onChange(Number.isFinite(parsed) ? parsed : undefined)
          return
        }
        onChange(text)
      }}
    />
  )

  return (
    <FormField label={label} description={description}>
      {control}
    </FormField>
  )
}

function SettingsFields({
  schema,
  draft,
  placeholderFor,
  onChange,
}: {
  schema: JSPluginSettingSpec[]
  draft: SettingValues
  placeholderFor: (spec: JSPluginSettingSpec) => string | undefined
  onChange: (key: string, value: unknown | undefined) => void
}) {
  return (
    <div className="space-y-3">
      {schema.map((spec) => (
        <SettingControl
          key={spec.key}
          spec={spec}
          value={draft[spec.key]}
          placeholder={placeholderFor(spec)}
          onChange={(value) => onChange(spec.key, value)}
        />
      ))}
    </div>
  )
}

function errorText(error: unknown, fallback: string) {
  return error instanceof APIError && error.message ? error.message : fallback
}

export function JSPluginSettingsSection({
  pluginId,
  sites,
}: {
  pluginId: string
  /** Sites that mount this plugin, offered as per-site overrides. */
  sites: Site[]
}) {
  const { t } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({
    queryKey: jsPluginQueryKeys.settings(pluginId),
    queryFn: ({ signal }) => getJSPluginSettings(pluginId, signal),
  })
  const schema = settingsQuery.data?.schema ?? []
  const [draft, setDraft] = useState<{ pluginId: string; values: SettingValues } | null>(null)
  const [error, setError] = useState('')
  const values = draft && draft.pluginId === pluginId ? draft.values : (settingsQuery.data?.values ?? {})

  const saveMutation = useMutation({
    mutationFn: (next: SettingValues) => updateJSPluginSettings(pluginId, next),
    onSuccess: () => {
      setError('')
      setDraft(null)
      toast.success(t('settings:jsPlugins.settings.saved'))
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.settings(pluginId) })
    },
    onError: (failure: unknown) => setError(errorText(failure, t('settings:jsPlugins.settings.saveFailed'))),
  })

  if (settingsQuery.isLoading) {
    return (
      <section className="flex items-center gap-2 border-t border-separator pt-4 text-body text-label-2">
        <LoaderCircle className="h-4 w-4 animate-spin" />
        {t('settings:jsPlugins.loading')}
      </section>
    )
  }
  if (settingsQuery.isError || schema.length === 0) return null

  const setValue = (key: string, value: unknown | undefined) => {
    setError('')
    const next = { ...values }
    if (value === undefined) delete next[key]
    else next[key] = value
    setDraft({ pluginId, values: next })
  }

  return (
    <section className="space-y-4 border-t border-separator pt-4">
      <div className="space-y-1">
        <h4 className="text-headline text-label">{t('settings:jsPlugins.settings.title')}</h4>
        <p className="text-footnote text-label-2">{t('settings:jsPlugins.settings.hint')}</p>
      </div>
      <SettingsFields
        schema={schema}
        draft={values}
        placeholderFor={(spec) => spec.default === undefined || spec.default === null
          ? undefined
          : t('settings:jsPlugins.settings.defaultPlaceholder', { value: String(spec.default) })}
        onChange={setValue}
      />
      {error ? <p role="alert" className="text-footnote text-danger">{error}</p> : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          size="sm"
          disabled={saveMutation.isPending}
          onClick={() => saveMutation.mutate(overridesOf(schema, values))}
        >
          {saveMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : t('common:actions.save')}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          disabled={saveMutation.isPending}
          onClick={() => {
            setError('')
            setDraft({ pluginId, values: {} })
          }}
        >
          {t('settings:jsPlugins.settings.reset')}
        </Button>
      </div>

      <div className="space-y-2">
        <h5 className="text-subhead text-label">{t('settings:jsPlugins.settings.siteTitle')}</h5>
        {sites.length === 0 ? (
          <p className="text-footnote text-label-2">{t('settings:jsPlugins.settings.noSites')}</p>
        ) : (
          <div className="divide-y divide-separator border-t border-separator">
            {sites.map((site) => (
              <SiteOverride
                key={site.id}
                pluginId={pluginId}
                site={site}
                schema={schema}
                resolved={settingsQuery.data?.resolved ?? {}}
              />
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

function SiteOverride({
  pluginId,
  site,
  schema,
  resolved,
}: {
  pluginId: string
  site: Site
  schema: JSPluginSettingSpec[]
  resolved: SettingValues
}) {
  const { t } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const stored = site.gateway_config?.plugin_settings?.[pluginId] ?? {}
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<SettingValues | null>(null)
  const [error, setError] = useState('')
  const values = draft ?? stored

  const saveMutation = useMutation({
    mutationFn: (next: SettingValues) => updateJSPluginSiteSettings(pluginId, site.id, next),
    onSuccess: () => {
      setError('')
      setDraft(null)
      toast.success(t('settings:jsPlugins.settings.siteSaved', { name: site.name }))
      void queryClient.invalidateQueries({ queryKey: jsPluginQueryKeys.settings(pluginId) })
      void queryClient.invalidateQueries({ queryKey: sitesQueryKeys.all })
    },
    onError: (failure: unknown) => setError(errorText(failure, t('settings:jsPlugins.settings.saveFailed'))),
  })

  return (
    <div className="py-2">
      <button
        type="button"
        className="flex w-full items-center justify-between gap-3 py-1 text-left"
        onClick={() => setOpen((current) => !current)}
      >
        <span className="truncate text-body text-label">{site.name}</span>
        <span className="shrink-0 text-footnote text-label-2">
          {open ? t('settings:jsPlugins.settings.collapse') : t('settings:jsPlugins.settings.expand')}
        </span>
      </button>
      {open ? (
        <div className="space-y-3 pt-2">
          <SettingsFields
            schema={schema}
            draft={values}
            placeholderFor={(spec) => {
              const global = resolved[spec.key]
              return global === undefined || global === null
                ? undefined
                : t('settings:jsPlugins.settings.globalPlaceholder', { value: String(global) })
            }}
            onChange={(key, value) => {
              setError('')
              const next = { ...values }
              if (value === undefined) delete next[key]
              else next[key] = value
              setDraft(next)
            }}
          />
          {error ? <p role="alert" className="text-footnote text-danger">{error}</p> : null}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              disabled={saveMutation.isPending}
              onClick={() => saveMutation.mutate(values)}
            >
              {saveMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : t('common:actions.save')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              disabled={saveMutation.isPending}
              onClick={() => {
                setError('')
                setDraft({})
              }}
            >
              {t('settings:jsPlugins.settings.clearOverride')}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}
