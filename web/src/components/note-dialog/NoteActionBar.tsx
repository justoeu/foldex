import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'

type Props = Readonly<{
  isEdit: boolean
  canSubmit: boolean
  onCancel: () => void
  onSubmit: () => void
}>

export function NoteActionBar({ isEdit, canSubmit, onCancel, onSubmit }: Props) {
  const { t } = useTranslation()
  return (
    <footer className="fx-modal-foot">
      <button type="button" className="fx-confirm-btn" onClick={onCancel}>{t('common.cancel')}</button>
      <button type="button" className="fx-confirm-btn fx-confirm-btn-primary" onClick={onSubmit} disabled={!canSubmit}>
        <Icon d={isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}
        {isEdit ? t('note_dialog.submit_save') : t('note_dialog.submit_create')}
      </button>
    </footer>
  )
}
