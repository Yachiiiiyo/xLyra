import type { TFunction } from 'i18next'

const KIND_LABEL_KEYS: Record<string, string> = {
  quota_probe: 'quotaProbe',
  protocol: 'protocol',
  model_list: 'modelList',
  credential_check: 'credentialCheck',
  site_detect: 'siteDetect',
  error_classifier: 'errorClassifier',
  model_metadata: 'modelMetadata',
  pricing_parse: 'pricingParse',
  automation: 'automation',
  site_provider: 'siteProvider',
}

export function jsPluginKindLabel(t: TFunction, kind: string) {
  const key = KIND_LABEL_KEYS[kind]
  if (key) return t(`settings:jsPlugins.kind.${key}`)
  return kind || '—'
}
