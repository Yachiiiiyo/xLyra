import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { StatusPageScreen } from '@/components/common/status-page-screen'
import { Button } from '@/components/ui/button'

export function NotFoundPage() {
  const { t } = useTranslation('common')

  return (
    <StatusPageScreen
      code={404}
      title={t('notFound.title')}
      description={t('notFound.description')}
      action={
        <Button asChild size="lg" className="w-full">
          <Link to="/">{t('notFound.backHome')}</Link>
        </Button>
      }
    />
  )
}
