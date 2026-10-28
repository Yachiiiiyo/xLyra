import { useTranslation } from 'react-i18next'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

export function PluginPolicySelect({
  value,
  disabled,
  onChange,
}: {
  value: 'off' | 'notify' | 'auto'
  disabled?: boolean
  onChange: (value: 'off' | 'notify' | 'auto') => void
}) {
  const { t } = useTranslation('settings')
  return (
    <Select
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        if (next === 'off' || next === 'notify' || next === 'auto') onChange(next)
      }}
    >
      <SelectTrigger aria-label={t('jsPlugins.market.policyLabel')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent searchable={false}>
        <SelectItem value="off">{t('jsPlugins.market.policy.off')}</SelectItem>
        <SelectItem value="notify">{t('jsPlugins.market.policy.notify')}</SelectItem>
        <SelectItem value="auto">{t('jsPlugins.market.policy.auto')}</SelectItem>
      </SelectContent>
    </Select>
  )
}
