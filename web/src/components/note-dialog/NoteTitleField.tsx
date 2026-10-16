import { useTranslation } from 'react-i18next'

type Props = Readonly<{
  title: string
  onChange: (value: string) => void
}>

export function NoteTitleField({ title, onChange }: Props) {
  const { t } = useTranslation()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('note_dialog.title_label')}</span>
      <div className="fx-input">
        <input autoFocus value={title} onChange={(event) => onChange(event.target.value)} placeholder={t('note_dialog.title_placeholder')} aria-label={t('common.title_aria')} />
      </div>
    </label>
  )
}
