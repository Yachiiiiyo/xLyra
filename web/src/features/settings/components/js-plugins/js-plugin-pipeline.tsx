import { useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, FlaskConical, LoaderCircle, Pencil, Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '@/components/common/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { MultiSelect, type MultiSelectOption } from '@/components/ui/multi-select'
import { Radio, RadioGroup } from '@/components/ui/radio'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { TextArea } from '@/components/ui/textarea'
import { siteTypeIconPath } from '@/components/common/brand-utils'
import { downstreamAPIKeyQueryKeys } from '@/features/api-keys/api/api-keys'
import {
  createJSPluginPipelineBinding,
  deleteJSPluginPipelineBinding,
  dryRunJSPluginPipelineBinding,
  jsPluginPipelineQueryKeys,
  listJSPluginPipelineBindings,
  listJSPluginPipelineInputOptions,
  updateJSPluginPipelineBinding,
  type JSPluginAutomationInput,
  type JSPluginAutomationOption,
  type JSPluginAutomationValue,
  type JSPluginGatewayPipelineManifest,
  type JSPluginPipelineBinding,
  type JSPluginPipelineDryRunResult,
  type JSPluginPipelineHostOptionSpec,
} from '@/features/settings/api/js-plugins'
import { resolveText } from '@/features/settings/lib/localized-text'
import { APIError } from '@/lib/http'
import { toast } from '@/lib/toast'

type PipelineFormState = {
  editing: string | null
  name: string
  values: Record<string, JSPluginAutomationValue | undefined>
  hostOptions: Record<string, boolean | string>
}

type DryRunState = {
  binding: JSPluginPipelineBinding
  mode: 'text' | 'json'
  endpoint: string
  model: string
  text: string
  payloadJSON: string
  jsonError: string
  result: JSPluginPipelineDryRunResult | null
}

const DRY_RUN_ENDPOINTS = [
  '/v1/chat/completions',
  '/v1/responses',
  '/v1/messages',
  '/v1/embeddings',
  '/v1/images/generations',
] as const

const DEFAULT_DRY_RUN_JSON = JSON.stringify(
  {
    model: 'dry-run',
    messages: [{ role: 'user', content: 'Hello from dry-run preview.' }],
  },
  null,
  2,
)

const isObjectInput = (input: JSPluginAutomationInput) =>
  input.type === 'oauth_connection' ||
  input.type === 'api_key' ||
  input.type === 'site'

const isRequired = (input: JSPluginAutomationInput) => input.required ?? isObjectInput(input)

function initialInputValues(
  manifest: JSPluginGatewayPipelineManifest,
  item?: JSPluginPipelineBinding,
): PipelineFormState['values'] {
  const values: PipelineFormState['values'] = {}
  for (const input of manifest.inputs) {
    if (item) {
      const saved = item.inputs.find((candidate) => candidate.name === input.name)
      if (isObjectInput(input)) {
        const ids = (saved?.entities ?? []).map((entity) => entity.id)
        values[input.name] = input.multiple ? ids : ids[0]
      } else {
        values[input.name] = saved?.value ?? input.default
      }
    } else if (isObjectInput(input)) {
      values[input.name] = input.multiple ? [] : undefined
    } else {
      values[input.name] = input.default
    }
  }
  return values
}

function initialHostOptions(
  specs: JSPluginPipelineHostOptionSpec[],
  item?: JSPluginPipelineBinding,
): Record<string, boolean | string> {
  const out: Record<string, boolean | string> = {}
  const saved = (item?.host_options ?? {}) as Record<string, boolean | string | undefined>
  for (const spec of specs) {
    const existing = saved[spec.key]
    if (spec.type === 'boolean') {
      out[spec.key] = typeof existing === 'boolean' ? existing : Boolean(spec.default)
    } else {
      out[spec.key] = typeof existing === 'string' ? existing : String(spec.default ?? '')
    }
  }
  return out
}

function filled(value: JSPluginAutomationValue | undefined) {
  if (value === undefined || value === '') return false
  return Array.isArray(value) ? value.length > 0 : true
}

function bindingStatusKind(status: string): 'success' | 'error' | 'warning' | 'idle' {
  switch (status) {
    case 'active':
      return 'success'
    case 'unavailable':
      return 'error'
    case 'bypassed':
      return 'warning'
    default:
      return 'idle'
  }
}

function dryRunOutcomeKind(outcome: string): 'success' | 'error' | 'warning' | 'idle' {
  switch (outcome) {
    case 'transformed':
      return 'success'
    case 'rejected':
    case 'failed':
      return 'error'
    case 'bypassed':
      return 'warning'
    default:
      return 'idle'
  }
}

export function JSPluginPipelineSection({
  pluginId,
  manifest,
  enabled,
}: {
  pluginId: string
  manifest: JSPluginGatewayPipelineManifest
  enabled: boolean
}) {
  const { t, i18n } = useTranslation(['settings', 'common'])
  const queryClient = useQueryClient()
  const [form, setForm] = useState<PipelineFormState | null>(null)
  const [dryRun, setDryRun] = useState<DryRunState | null>(null)
  const text = (value: Parameters<typeof resolveText>[0]) => resolveText(value, i18n.language)

  const objectInputs = manifest.inputs.filter(isObjectInput)
  const bindingsQuery = useQuery({
    queryKey: jsPluginPipelineQueryKeys.list(pluginId),
    queryFn: ({ signal }) => listJSPluginPipelineBindings(pluginId, signal),
  })

  const items = bindingsQuery.data?.items ?? []
  const hostOptionSpecs = bindingsQuery.data?.host_options ?? []

  const optionQueries = useQueries({
    queries: objectInputs.map((input) => ({
      queryKey: jsPluginPipelineQueryKeys.options(pluginId, input.name, form?.editing ?? undefined),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listJSPluginPipelineInputOptions(pluginId, input.name, form?.editing ?? undefined, signal),
      enabled: form != null && enabled,
    })),
  })

  const optionsByInput: Record<string, { options: JSPluginAutomationOption[]; loading: boolean }> = {}
  objectInputs.forEach((input, index) => {
    optionsByInput[input.name] = {
      options: optionQueries[index]?.data ?? [],
      loading: optionQueries[index]?.isLoading ?? false,
    }
  })

  const optionIcon = (option: JSPluginAutomationOption) =>
    option.icon_key ? siteTypeIconPath(option.icon_key) : undefined

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: jsPluginPipelineQueryKeys.list(pluginId) })
    void queryClient.invalidateQueries({ queryKey: downstreamAPIKeyQueryKeys.all })
  }

  const errorText = (error: unknown) =>
    error instanceof APIError ? error.message : t('settings:jsPlugins.pipeline.saveFailed')

  const saveMutation = useMutation({
    mutationFn: async (state: PipelineFormState) => {
      const inputs: Record<string, JSPluginAutomationValue> = {}
      for (const [name, value] of Object.entries(state.values)) {
        if (value !== undefined && value !== '') inputs[name] = value
      }
      const hostOptions: Record<string, unknown> = {}
      for (const spec of hostOptionSpecs) {
        if (spec.key in state.hostOptions) {
          hostOptions[spec.key] = state.hostOptions[spec.key]
        }
      }
      const payload = {
        name: state.name.trim(),
        inputs,
        host_options: hostOptions,
      }
      if (state.editing) {
        await updateJSPluginPipelineBinding(pluginId, state.editing, payload)
      } else {
        await createJSPluginPipelineBinding(pluginId, payload)
      }
    },
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.pipeline.saved'))
      setForm(null)
      refresh()
    },
    onError: (error: unknown) => toast.error(errorText(error)),
  })

  const deleteMutation = useMutation({
    mutationFn: (bindingId: string) => deleteJSPluginPipelineBinding(pluginId, bindingId),
    onSuccess: () => {
      toast.success(t('settings:jsPlugins.pipeline.deleted'))
      refresh()
    },
    onError: (error: unknown) => toast.error(errorText(error)),
  })

  const dryRunMutation = useMutation({
    mutationFn: async (state: DryRunState) => {
      if (state.mode === 'json') {
        let parsed: unknown
        try {
          parsed = JSON.parse(state.payloadJSON)
        } catch (err) {
          throw new Error(err instanceof Error ? err.message : t('settings:jsPlugins.pipeline.dryRun.invalidJson'))
        }
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          throw new Error(t('settings:jsPlugins.pipeline.dryRun.invalidJson'))
        }
        return dryRunJSPluginPipelineBinding(pluginId, state.binding.id, {
          endpoint: state.endpoint,
          model: state.model.trim() || undefined,
          payload: parsed as Record<string, unknown>,
        })
      }
      return dryRunJSPluginPipelineBinding(pluginId, state.binding.id, {
        text: state.text,
        endpoint: state.endpoint,
        model: state.model.trim() || undefined,
      })
    },
    onSuccess: (result) => {
      setDryRun((current) => (current ? { ...current, jsonError: '', result } : current))
    },
    onError: (error: unknown) => {
      const msg = error instanceof Error ? error.message : t('settings:jsPlugins.pipeline.dryRun.failed')
      setDryRun((current) => (current ? { ...current, jsonError: msg } : current))
      toast.error(msg)
    },
  })

  const canSave =
    form != null &&
    form.name.trim().length > 0 &&
    manifest.inputs.every((input) => !isRequired(input) || filled(form.values[input.name])) &&
    !saveMutation.isPending

  const title = text(manifest.form?.title) ?? t('settings:jsPlugins.pipeline.title')
  const description = text(manifest.form?.description) ?? t('settings:jsPlugins.pipeline.hint')
  const addLabel = text(manifest.form?.addLabel) ?? t('settings:jsPlugins.pipeline.add')
  const emptyText = text(manifest.form?.emptyText) ?? t('settings:jsPlugins.pipeline.empty')
  const inputTitle = (input: JSPluginAutomationInput) => text(input.title) ?? input.name
  const reasonText = (option: JSPluginAutomationOption) =>
    option.reason_code
      ? t(`settings:jsPlugins.automation.reason.${option.reason_code}`, option.reason ?? option.reason_code)
      : option.reason

  const setInputValue = (name: string, value: JSPluginAutomationValue | undefined) =>
    setForm((current) => (current ? { ...current, values: { ...current.values, [name]: value } } : current))

  const setHostOptionValue = (key: string, value: boolean | string) =>
    setForm((current) => (current ? { ...current, hostOptions: { ...current.hostOptions, [key]: value } } : current))

  const openCreate = () => {
    setForm({
      editing: null,
      name: '',
      values: initialInputValues(manifest),
      hostOptions: initialHostOptions(hostOptionSpecs),
    })
  }

  const openEdit = (item: JSPluginPipelineBinding) => {
    setForm({
      editing: item.id,
      name: item.name,
      values: initialInputValues(manifest, item),
      hostOptions: initialHostOptions(hostOptionSpecs, item),
    })
  }

  const openDryRun = (item: JSPluginPipelineBinding) => {
    setDryRun({
      binding: item,
      mode: 'text',
      endpoint: '/v1/chat/completions',
      model: 'dry-run',
      text: '',
      payloadJSON: DEFAULT_DRY_RUN_JSON,
      jsonError: '',
      result: null,
    })
  }

  const renderInput = (input: JSPluginAutomationInput, state: PipelineFormState) => {
    const label = inputTitle(input)
    const hint = text(input.description)
    const value = state.values[input.name]

    if (isObjectInput(input)) {
      const { options, loading } = optionsByInput[input.name] ?? { options: [], loading: false }
      const emptyOptions = text(input.emptyText) ?? t('settings:jsPlugins.automation.noOptions')
      const placeholder = text(input.placeholder) ?? t('settings:jsPlugins.automation.selectPlaceholder')
      return (
        <FormField key={input.name} label={label} description={hint} required={isRequired(input)}>
          {input.multiple ? (
            <MultiSelect
              value={Array.isArray(value) ? value : []}
              options={options.map<MultiSelectOption>((option) => ({
                value: option.id,
                label: option.name,
                icon: optionIcon(option),
                disabled: option.disabled,
                description: option.disabled ? reasonText(option) : option.description,
              }))}
              placeholder={placeholder}
              searchPlaceholder={t('settings:jsPlugins.siteBinding.search')}
              emptyText={emptyOptions}
              selectedText={t('settings:jsPlugins.automation.selectedUnit')}
              maxVisibleTags={4}
              onChange={(ids) => setInputValue(input.name, ids)}
            />
          ) : (
            <Select value={typeof value === 'string' ? value : undefined} onValueChange={(id) => setInputValue(input.name, id)}>
              <SelectTrigger>
                <SelectValue placeholder={placeholder} />
              </SelectTrigger>
              <SelectContent searchable={false}>
                {options.map((option) => (
                  <SelectItem key={option.id} value={option.id} disabled={option.disabled}>
                    {option.name}
                    {option.disabled ? ` — ${reasonText(option)}` : option.description ? ` · ${option.description}` : ''}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {!loading && options.length === 0 ? <p className="text-footnote text-label-2">{emptyOptions}</p> : null}
        </FormField>
      )
    }

    if (input.type === 'boolean') {
      if (input.control === 'switch') {
        return (
          <Switch
            key={input.name}
            checked={value === true}
            onCheckedChange={(checked) => setInputValue(input.name, checked)}
            label={label}
            description={hint}
          />
        )
      }
      return (
        <Checkbox
          key={input.name}
          checked={value === true}
          onCheckedChange={(checked) => setInputValue(input.name, checked === true)}
          label={label}
          description={hint}
        />
      )
    }

    if (input.enum && input.enum.length > 0 && input.control === 'radio') {
      return (
        <FormField key={input.name} label={label} description={hint} required={isRequired(input)}>
          <RadioGroup
            value={value === undefined ? undefined : String(value)}
            onValueChange={(next) =>
              setInputValue(input.name, input.enum?.find((option) => String(option) === next) as JSPluginAutomationValue)
            }
          >
            {input.enum.map((option) => (
              <Radio key={String(option)} value={String(option)} label={String(option)} />
            ))}
          </RadioGroup>
        </FormField>
      )
    }

    if (input.enum && input.enum.length > 0) {
      return (
        <FormField key={input.name} label={label} description={hint} required={isRequired(input)}>
          <Select
            value={value === undefined ? undefined : String(value)}
            onValueChange={(next) =>
              setInputValue(input.name, input.enum?.find((option) => String(option) === next) as JSPluginAutomationValue)
            }
          >
            <SelectTrigger>
              <SelectValue placeholder={text(input.placeholder)} />
            </SelectTrigger>
            <SelectContent searchable={false}>
              {input.enum.map((option) => (
                <SelectItem key={String(option)} value={String(option)}>
                  {String(option)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormField>
      )
    }

    const numeric = input.type === 'number' || input.type === 'integer'
    if (input.control === 'textarea') {
      return (
        <FormField key={input.name} label={label} description={hint} required={isRequired(input)}>
          <TextArea
            placeholder={text(input.placeholder)}
            value={value === undefined || Array.isArray(value) ? '' : String(value)}
            onChange={(event) => setInputValue(input.name, event.target.value === '' ? undefined : event.target.value)}
          />
        </FormField>
      )
    }

    return (
      <FormField key={input.name} label={label} description={hint} required={isRequired(input)}>
        <Input
          type={numeric ? 'number' : input.control === 'password' ? 'password' : 'text'}
          autoComplete={input.control === 'password' ? 'off' : undefined}
          min={input.minimum}
          max={input.maximum}
          placeholder={text(input.placeholder)}
          value={value === undefined || Array.isArray(value) ? '' : String(value)}
          onChange={(event) => {
            const raw = event.target.value
            if (raw === '') return setInputValue(input.name, undefined)
            setInputValue(input.name, numeric ? Number(raw) : raw)
          }}
        />
      </FormField>
    )
  }

  const renderHostOption = (spec: JSPluginPipelineHostOptionSpec, state: PipelineFormState) => {
    const label = t(`settings:jsPlugins.pipeline.hostOptions.${spec.key}.label`, spec.key)
    const hint = t(`settings:jsPlugins.pipeline.hostOptions.${spec.key}.hint`, '')

    if (spec.type === 'boolean') {
      return (
        <Switch
          key={spec.key}
          checked={state.hostOptions[spec.key] === true}
          onCheckedChange={(checked) => setHostOptionValue(spec.key, checked)}
          label={label}
          description={hint || undefined}
        />
      )
    }

    return null
  }

  const describeBinding = (item: JSPluginPipelineBinding) => {
    const lines: string[] = []
    for (const input of manifest.inputs) {
      const saved = item.inputs.find((candidate) => candidate.name === input.name)
      const val = isObjectInput(input)
        ? (saved?.entities ?? []).map((entity) => entity.name || entity.id).join(', ')
        : saved?.value === undefined
          ? ''
          : input.control === 'password'
            ? '••••••'
            : String(saved.value)
      if (val) lines.push(`${inputTitle(input)}: ${val}`)
    }
    return lines
  }

  const statusLabel = (status: string) => t(`settings:jsPlugins.pipeline.status.${status}`, status)

  return (
    <section className="space-y-4 border-t border-separator pt-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h4 className="text-headline text-label">{title}</h4>
            {manifest.failurePolicy ? (
              <Badge variant={manifest.failurePolicy === 'fail_closed' ? 'warning' : 'neutral'}>
                {t(`settings:jsPlugins.pipeline.policy.${manifest.failurePolicy}`, manifest.failurePolicy)}
              </Badge>
            ) : null}
          </div>
          <p className="text-footnote text-label-2">{description}</p>
        </div>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          className="shrink-0"
          disabled={!enabled}
          title={!enabled ? t('settings:jsPlugins.pipeline.disabledHint') : undefined}
          onClick={openCreate}
        >
          <Plus className="mr-1.5 h-4 w-4" />
          {addLabel}
        </Button>
      </div>

      {manifest.failurePolicy === 'fail_closed' ? (
        <div className="flex items-start gap-2.5 rounded-inner border border-warning/30 bg-warning/10 px-3 py-2.5 text-footnote text-label">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
          <p>{t('settings:jsPlugins.pipeline.failClosedWarning')}</p>
        </div>
      ) : null}

      {!enabled && items.length > 0 ? (
        <p className="text-footnote text-label-2">{t('settings:jsPlugins.pipeline.disabledBindingsHint')}</p>
      ) : null}

      {bindingsQuery.isLoading ? (
        <div className="flex items-center gap-2 py-3 text-body text-label-2">
          <LoaderCircle className="h-4 w-4 animate-spin" />
          {t('settings:jsPlugins.loading')}
        </div>
      ) : items.length === 0 ? (
        <p className="text-body text-label-2">{emptyText}</p>
      ) : (
        <ul className="divide-y divide-separator">
          {items.map((item) => {
            const lines = describeBinding(item)
            return (
              <li key={item.id} className="flex items-start justify-between gap-3 py-3">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-body font-medium text-label">{item.name}</span>
                    <StatusBadge status={bindingStatusKind(item.status)}>{statusLabel(item.status)}</StatusBadge>
                  </div>
                  {item.unavailable_reason ? (
                    <p className="break-words text-footnote text-danger">{item.unavailable_reason}</p>
                  ) : null}
                  {lines.map((line) => (
                    <p key={line} className="break-words text-footnote text-label-2">
                      {line}
                    </p>
                  ))}
                </div>
                <div className="flex shrink-0 gap-1">
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    aria-label={t('settings:jsPlugins.pipeline.dryRun.button')}
                    title={t('settings:jsPlugins.pipeline.dryRun.button')}
                    disabled={!enabled || item.status !== 'active'}
                    onClick={() => openDryRun(item)}
                  >
                    <FlaskConical className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    aria-label={t('settings:jsPlugins.automation.edit')}
                    title={t('settings:jsPlugins.automation.edit')}
                    disabled={!enabled}
                    onClick={() => openEdit(item)}
                  >
                    <Pencil className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="text-danger hover:text-danger"
                    aria-label={t('settings:jsPlugins.automation.delete')}
                    title={t('settings:jsPlugins.automation.delete')}
                    disabled={deleteMutation.isPending}
                    onClick={() => deleteMutation.mutate(item.id)}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </li>
            )
          })}
        </ul>
      )}

      {/* Create / Edit Binding Dialog */}
      <Dialog open={form != null} onOpenChange={(open) => (open ? undefined : setForm(null))}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>
              {form?.editing ? t('settings:jsPlugins.pipeline.editTitle') : addLabel}
            </DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {form ? (
              <>
                <FormField
                  label={t('settings:jsPlugins.pipeline.nameLabel')}
                  description={t('settings:jsPlugins.pipeline.nameHint')}
                  required
                >
                  <Input
                    maxLength={80}
                    placeholder={t('settings:jsPlugins.pipeline.namePlaceholder')}
                    value={form.name}
                    onChange={(event) => setForm((current) => (current ? { ...current, name: event.target.value } : current))}
                  />
                </FormField>

                {manifest.inputs.map((input) => renderInput(input, form))}

                {hostOptionSpecs.length > 0 ? (
                  <div className="space-y-3 border-t border-separator pt-3">
                    <h5 className="text-footnote font-semibold text-label-2">
                      {t('settings:jsPlugins.pipeline.hostOptions.title')}
                    </h5>
                    {hostOptionSpecs.map((spec) => renderHostOption(spec, form))}
                  </div>
                ) : null}
              </>
            ) : null}
          </DialogBody>
          <DialogFooter
            cancel={
              <Button type="button" variant="secondary" onClick={() => setForm(null)} disabled={saveMutation.isPending}>
                {t('common:actions.cancel')}
              </Button>
            }
            confirm={
              <Button type="button" disabled={!canSave} onClick={() => form && saveMutation.mutate(form)}>
                {saveMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : t('common:actions.save')}
              </Button>
            }
          />
        </DialogContent>
      </Dialog>

      {/* Dry-Run Preview Dialog */}
      <Dialog open={dryRun != null} onOpenChange={(open) => (open ? undefined : setDryRun(null))}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>
              {t('settings:jsPlugins.pipeline.dryRun.title', { name: dryRun?.binding.name ?? '' })}
            </DialogTitle>
            <DialogDescription>{t('settings:jsPlugins.pipeline.dryRun.description')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {dryRun ? (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant={dryRun.mode === 'text' ? 'default' : 'secondary'}
                    onClick={() => setDryRun((current) => (current ? { ...current, mode: 'text', jsonError: '' } : current))}
                  >
                    {t('settings:jsPlugins.pipeline.dryRun.modeText')}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant={dryRun.mode === 'json' ? 'default' : 'secondary'}
                    onClick={() => setDryRun((current) => (current ? { ...current, mode: 'json', jsonError: '' } : current))}
                  >
                    {t('settings:jsPlugins.pipeline.dryRun.modeJson')}
                  </Button>
                </div>

                <div className="grid gap-3 sm:grid-cols-2">
                  <FormField label={t('settings:jsPlugins.pipeline.dryRun.endpoint')}>
                    <Select
                      value={dryRun.endpoint}
                      onValueChange={(endpoint) => setDryRun((current) => (current ? { ...current, endpoint } : current))}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent searchable={false}>
                        {DRY_RUN_ENDPOINTS.map((endpoint) => (
                          <SelectItem key={endpoint} value={endpoint}>
                            {endpoint}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </FormField>
                  <FormField label={t('settings:jsPlugins.pipeline.dryRun.model')}>
                    <Input
                      value={dryRun.model}
                      placeholder="dry-run"
                      onChange={(event) => setDryRun((current) => (current ? { ...current, model: event.target.value } : current))}
                    />
                  </FormField>
                </div>

                {dryRun.mode === 'text' ? (
                  <FormField label={t('settings:jsPlugins.pipeline.dryRun.textInput')}>
                    <TextArea
                      rows={4}
                      placeholder={t('settings:jsPlugins.pipeline.dryRun.textPlaceholder')}
                      value={dryRun.text}
                      onChange={(event) => setDryRun((current) => (current ? { ...current, text: event.target.value } : current))}
                    />
                  </FormField>
                ) : (
                  <FormField label={t('settings:jsPlugins.pipeline.dryRun.jsonInput')}>
                    <TextArea
                      rows={8}
                      className="font-mono text-footnote"
                      value={dryRun.payloadJSON}
                      onChange={(event) => setDryRun((current) => (current ? { ...current, payloadJSON: event.target.value, jsonError: '' } : current))}
                    />
                  </FormField>
                )}

                {dryRun.jsonError ? <p className="text-footnote text-danger">{dryRun.jsonError}</p> : null}

                {dryRun.result ? (
                  <div className="space-y-3 border-t border-separator pt-4">
                    <div className="flex flex-wrap items-center gap-2 text-footnote">
                      <StatusBadge status={dryRunOutcomeKind(dryRun.result.outcome)}>
                        {dryRun.result.outcome}
                      </StatusBadge>
                      <span className="tabular-nums text-label-2">
                        {t('settings:jsPlugins.pipeline.dryRun.metricsSummary', {
                          segments: dryRun.result.metrics.segments,
                          detected: dryRun.result.findings.length,
                          latency: dryRun.result.metrics.latency_ms,
                        })}
                      </span>
                      {Object.entries(dryRun.result.metrics.counts ?? {}).map(([key, count]) => (
                        <Badge key={key} variant="neutral">
                          {key}: {count}
                        </Badge>
                      ))}
                    </div>

                    {dryRun.result.failure ? (
                      <div className="rounded-inner border border-danger/30 bg-danger/10 p-3 text-footnote text-danger">
                        <div className="font-mono font-medium">
                          HTTP {dryRun.result.failure.status} · {dryRun.result.failure.code}
                        </div>
                        <p className="mt-1">{dryRun.result.failure.message}</p>
                      </div>
                    ) : null}

                    {dryRun.result.transformed_text !== undefined ? (
                      <div className="space-y-1">
                        <div className="text-footnote font-medium text-label-2">
                          {t('settings:jsPlugins.pipeline.dryRun.output')}
                        </div>
                        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-inner bg-fill-4 p-3 font-mono text-footnote text-label">
                          {dryRun.result.transformed_text}
                        </pre>
                      </div>
                    ) : dryRun.result.transformed_payload ? (
                      <div className="space-y-1">
                        <div className="text-footnote font-medium text-label-2">
                          {t('settings:jsPlugins.pipeline.dryRun.output')}
                        </div>
                        <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-inner bg-fill-4 p-3 font-mono text-footnote text-label">
                          {JSON.stringify(dryRun.result.transformed_payload, null, 2)}
                        </pre>
                      </div>
                    ) : null}

                    <div className="space-y-1.5">
                      <div className="text-footnote font-medium text-label-2">
                        {t('settings:jsPlugins.pipeline.dryRun.findings', { count: dryRun.result.findings.length })}
                      </div>
                      {dryRun.result.findings.length === 0 ? (
                        <p className="text-footnote text-label-2">{t('settings:jsPlugins.pipeline.dryRun.noFindings')}</p>
                      ) : (
                        <ul className="divide-y divide-separator rounded-inner border border-separator">
                          {dryRun.result.findings.map((finding, idx) => (
                            <li key={`${finding.segment_id}-${finding.start}-${finding.end}-${idx}`} className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-3 py-2 text-footnote">
                              <Badge variant={finding.applied ? 'secondary' : 'neutral'}>{finding.type}</Badge>
                              <span className="font-mono text-label-2">
                                {finding.field}[{finding.start}:{finding.end}]
                              </span>
                              {finding.placeholder ? (
                                <span className="font-mono text-label">→ {finding.placeholder}</span>
                              ) : null}
                              {finding.source ? <span className="text-label-2">{finding.source}</span> : null}
                              {finding.score ? <span className="tabular-nums text-label-2">{finding.score.toFixed(2)}</span> : null}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  </div>
                ) : null}
              </>
            ) : null}
          </DialogBody>
          <DialogFooter
            cancel={
              <Button type="button" variant="secondary" onClick={() => setDryRun(null)} disabled={dryRunMutation.isPending}>
                {t('common:actions.close')}
              </Button>
            }
            confirm={
              <Button type="button" disabled={!dryRun || dryRunMutation.isPending} onClick={() => dryRun && dryRunMutation.mutate(dryRun)}>
                {dryRunMutation.isPending ? <LoaderCircle className="mr-1.5 h-4 w-4 animate-spin" /> : <FlaskConical className="mr-1.5 h-4 w-4" />}
                {t('settings:jsPlugins.pipeline.dryRun.run')}
              </Button>
            }
          />
        </DialogContent>
      </Dialog>
    </section>
  )
}
