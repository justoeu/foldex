import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'

export function MutationAlert({ message, onClose }: { message: string; onClose: () => void }) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      style={{
        position: 'fixed',
        top: 16,
        right: 16,
        zIndex: 12000,
        maxWidth: 440,
        padding: '12px 14px',
        border: '1px solid color-mix(in srgb, var(--fx-danger) 40%, var(--fx-border))',
        borderRadius: 10,
        background: 'var(--fx-surface)',
        color: 'var(--fx-danger)',
        boxShadow: 'var(--fx-shadow-2)',
        display: 'flex',
        alignItems: 'flex-start',
        gap: 8,
      }}
    >
      <Icon d={I.alert} size={14} />
      <span style={{ flex: 1, fontSize: 12, lineHeight: 1.5 }}>{message}</span>
      <button type="button" className="fx-iconbtn" onClick={onClose} aria-label={t('common.close')}>
        <Icon d={I.x} size={12} />
      </button>
    </div>
  )
}
