import { useTranslation } from 'react-i18next'
import { unreachableResources, useDepStatus } from '../api/status'

export function DepStatusBar() {
  const { t } = useTranslation()
  const { data } = useDepStatus()
  const down = unreachableResources(data)
  if (down.length === 0) return null
  const names = down.map((r) => t(`status.resource_${r.id}`)).join(t('status.list_sep'))
  return (
    <div className="fx-dep-status" role="status" aria-live="polite">
      {t('status.unreachable', { names })}
    </div>
  )
}
