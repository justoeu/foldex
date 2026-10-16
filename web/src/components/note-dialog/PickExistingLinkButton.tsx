import { useTranslation } from 'react-i18next'
import type { Link } from '../../api/types'

type Props = Readonly<{
  link: Link
  onOpenExisting: (link: Link) => void
}>

export function PickExistingLinkButton({ link, onOpenExisting }: Props) {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      className="fx-confirm-btn"
      onClick={() => onOpenExisting(link)}
    >
      {t('link_dialog.open_existing')}
    </button>
  )
}
