import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { forgotPassword } from '../../api/auth'
import { AuthShell, AuthField, AuthSubmit } from './AuthShell'

/**
 * "Forgot password" and its confirmation, in one component.
 *
 * The confirmation is a state of this screen rather than a separate one
 * because it must be reachable by EXACTLY the same path for every address. A
 * distinct "sent" route, or any difference in what is shown, would rebuild on
 * the client the account-existence oracle the backend removes with an
 * unconditional 202 and a duration floor.
 *
 * The copy is careful for the same reason: "if an account exists" rather than
 * "we sent you an e-mail", because the second is a claim the product cannot
 * honestly make about an address it will not confirm.
 */
export function ForgotScreen({ onBack }: { onBack: () => void }) {
  const { t, i18n } = useTranslation()
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    try {
      // The language this screen is speaking travels with the request, so the
      // reset e-mail speaks it too. Without it the server falls back to
      // Accept-Language — a different browser setting, and the reason a
      // Portuguese login screen produced an English reset link.
      await forgotPassword(email, i18n.resolvedLanguage ?? i18n.language)
    } catch {
      // Swallowed on purpose. The endpoint is contractually 202 for every
      // input, so the only errors reaching here are transport failures — and
      // showing one for a malformed address while staying silent for a valid
      // one would leak precisely what the 202 exists to hide.
    } finally {
      setBusy(false)
      setSent(true)
    }
  }

  if (sent) {
    return (
      <AuthShell
        kicker={t('auth_sent.kicker')}
        title={t('auth_sent.title')}
        subtitle={t('auth_sent.subtitle')}
      >
        <div className="fx-auth-form">
          <p className="fx-auth-notice" role="status">
            {t('auth_sent.body')}
          </p>
          <div className="fx-auth-alt">
            <button type="button" className="fx-auth-link" onClick={onBack}>
              {t('auth_sent.back_to_login')}
            </button>
            <button type="button" className="fx-auth-link" onClick={() => setSent(false)}>
              {t('auth_sent.try_again')}
            </button>
          </div>
        </div>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      kicker={t('auth_forgot.kicker')}
      title={t('auth_forgot.title')}
      subtitle={t('auth_forgot.subtitle')}
    >
      <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
        <AuthField id="fx-forgot-email" label={t('auth.email')}>
          <input
            id="fx-forgot-email"
            className="fx-auth-input"
            type="email"
            name="email"
            autoComplete="username"
            autoFocus
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </AuthField>

        <AuthSubmit busy={busy}>{t('auth_forgot.submit')}</AuthSubmit>

        <div className="fx-auth-alt">
          <button type="button" className="fx-auth-link" onClick={onBack}>
            {t('auth_forgot.back_to_login')}
          </button>
        </div>
      </form>
    </AuthShell>
  )
}
