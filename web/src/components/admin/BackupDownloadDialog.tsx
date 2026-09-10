import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import { PasswordInput } from '../PasswordInput'
import { useCopy } from '../../hooks/useCopy'
import { useEscape } from '../../hooks/useEscape'
import { useFocusTrap } from '../../hooks/useFocusTrap'
import { generatePassword } from '../../lib/generatePassword'
import type { BackupDownloadBudget } from '../../api/admin'

/** Mirrors the server's floor (backupstatus.minDownloadPassphrase). */
export const MIN_DOWNLOAD_PASSWORD = 12

/**
 * Asks for the password that will encrypt the artifact (ADR-48).
 *
 * Deliberately NOT the account password. The file leaves the instance and
 * lands on someone's disk; if it carried the administrator's login password,
 * whoever ended up with it could brute-force that password offline — no rate
 * limit, no lockout, none of what INV-184 and INV-041 protect. A password
 * chosen here protects the file and nothing else.
 *
 * The generator is the same one an administrator installing a user's password
 * gets (INV-166): typing a strong passphrase twice is the step people skip.
 */
export function BackupDownloadDialog({
  filename,
  budget,
  onCancel,
  onConfirm,
}: {
  filename: string
  budget: BackupDownloadBudget | null
  onCancel: () => void
  onConfirm: (password: string) => Promise<void>
}) {
  const { t } = useTranslation()
  const dialogRef = useRef<HTMLDivElement>(null)
  const copier = useCopy()
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEscape(onCancel)
  useFocusTrap(dialogRef, true)

  const tooShort = password.length > 0 && password.length < MIN_DOWNLOAD_PASSWORD

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (busy || password.length < MIN_DOWNLOAD_PASSWORD) return
    setBusy(true)
    setError('')
    try {
      await onConfirm(password)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('admin.backup_download_title')}
    >
      <form className="fx-modal fx-confirm" onSubmit={(e) => void submit(e)}>
        <header className="fx-modal-head">
          <div>
            {/* No modifier: .fx-confirm.fx-modal .fx-modal-kicker is already
                the danger colour — `-info` is the one that opts OUT of it. */}
            <div className="fx-modal-kicker">{t('admin.backup_download_kicker')}</div>
            <h2 className="fx-modal-title">{t('admin.backup_download_title')}</h2>
          </div>
          <button type="button" className="fx-confirm-x" onClick={onCancel} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-confirm-body" style={{ display: 'grid', gap: 12 }}>
          {/* What is actually leaving the instance, named before the click. */}
          <p style={{ margin: 0 }}>{t('admin.backup_download_desc')}</p>
          <code className="fx-bkp-key" title={filename}>{filename}</code>

          {budget && (
            <div className="fx-bkp-budget">
              {t('admin.backup_download_budget', { available: budget.available, limit: budget.limit })}
            </div>
          )}

          {error && (
            <div className="fx-inline-error" role="alert">
              {error}
            </div>
          )}

          <label className="fx-field">
            <span className="fx-field-label">{t('admin.backup_download_password')}</span>
            <PasswordInput
              className="fx-input"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
            />
          </label>
          <div className="fx-bkp-download-tools">
            <button
              type="button"
              className="fx-btn"
              onClick={() => setPassword(generatePassword(MIN_DOWNLOAD_PASSWORD))}
            >
              {t('admin.backup_download_generate')}
            </button>
            {password !== '' && (
              <button type="button" className="fx-btn" onClick={() => void copier.copy(password)}>
                {copier.copied(password) ? t('admin.backup_copied') : t('admin.backup_copy')}
              </button>
            )}
          </div>
          {/* The one thing nobody can recover for them. */}
          <p className="fx-bkp-download-warn">{t('admin.backup_download_lost')}</p>
          {tooShort && (
            <div className="fx-inline-error" role="alert">
              {t('admin.backup_download_too_short', { min: MIN_DOWNLOAD_PASSWORD })}
            </div>
          )}
        </div>

        <footer className="fx-confirm-foot">
          <button type="button" className="fx-confirm-btn" onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button
            type="submit"
            className="fx-confirm-btn fx-confirm-btn-primary"
            disabled={busy || password.length < MIN_DOWNLOAD_PASSWORD}
          >
            {busy ? t('admin.backup_download_working') : t('admin.backup_download_action')}
          </button>
        </footer>
      </form>
    </div>
  )
}
