import type { RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { PasswordInput } from './PasswordInput'
import { formatCountdown } from '../hooks/usePasswordGateMachine'

type Props = Readonly<{
  dialogRef: RefObject<HTMLDivElement | null>
  folderName: string
  passwordHint?: string | null
  password: string
  error: string | null
  locked: boolean
  remainingSecs: number
  showHint: boolean
  submitting: boolean
  onPasswordChange: (value: string) => void
  onCancel: () => void
  onSubmit: () => void
}>

export function PasswordGateForm({
  dialogRef,
  folderName,
  passwordHint,
  password,
  error,
  locked,
  remainingSecs,
  showHint,
  submitting,
  onPasswordChange,
  onCancel,
  onSubmit,
}: Props) {
  const { t } = useTranslation()
  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('folder_lock.dialog_aria', { name: folderName })}
    >
      <form
        className="fx-modal fx-confirm fx-lockmodal"
        onSubmit={(e) => {
          e.preventDefault()
          onSubmit()
        }}
      >
        <header className="fx-modal-head">
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <span className="fx-lock-badge">
              <Icon d={I.lock} size={18} stroke={2} />
            </span>
            <div>
              <div className="fx-modal-kicker">{t('folder_lock.kicker')}</div>
              <h2 className="fx-modal-title">{t('folder_lock.title', { name: folderName })}</h2>
            </div>
          </div>
          <button type="button" className="fx-confirm-x" onClick={onCancel} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-confirm-body">
          {locked ? (
            <div className="fx-lock-banner">
              <Icon d={I.clock} size={15} />
              <div>
                <strong>{t('folder_lock.locked_title')}</strong>
                <div>{t('folder_lock.locked', { time: formatCountdown(remainingSecs) })}</div>
              </div>
            </div>
          ) : (
            <>
              <p style={{ margin: 0 }}>{t('folder_lock.body')}</p>
              <div className="fx-input" style={{ marginTop: 12 }}>
                <PasswordInput
                  autoFocus
                  autoComplete="off"
                  value={password}
                  onChange={(e) => onPasswordChange(e.target.value)}
                  placeholder={t('folder_lock.input_placeholder')}
                  aria-label={t('folder_lock.input_label')}
                />
              </div>
              {error && (
                <div className="fx-lock-error">
                  <Icon d={I.alert} size={12} /> {error}
                </div>
              )}
              {showHint && (
                <div className="fx-lock-hint">
                  <Icon d={I.info} size={12} /> {t('folder_lock.hint', { hint: passwordHint })}
                </div>
              )}
            </>
          )}
        </div>

        <footer className="fx-confirm-foot">
          <button type="button" className="fx-confirm-btn" onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button
            type="submit"
            className="fx-confirm-btn fx-confirm-btn-primary"
            disabled={!password || submitting || locked}
          >
            <Icon d={I.lock} size={13} stroke={2.2} /> {t('folder_lock.submit')}
          </button>
        </footer>
      </form>
    </div>
  )
}
