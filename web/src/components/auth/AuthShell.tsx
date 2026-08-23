import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthLocaleSwitcher } from './AuthLocaleSwitcher'

/**
 * The two-pane frame every auth screen renders inside: the form on the left,
 * a marketing panel on the right.
 *
 * The marketing pane is `aria-hidden` and drops out entirely below 900px. It
 * carries no information the user needs to authenticate, so exposing it to a
 * screen reader would just put three paragraphs between the heading and the
 * e-mail field.
 *
 * The language switcher lives HERE rather than on each screen: every auth
 * surface renders through this frame, so a user who lands on the reset or
 * invite screen in a language they do not read gets the same way out as one on
 * login. One mount instead of one per screen — and the next screen added gets
 * it without anyone remembering to.
 */
export function AuthShell({
  kicker,
  title,
  subtitle,
  children,
  footer,
}: {
  kicker?: string
  title: string
  subtitle?: ReactNode
  children: ReactNode
  footer?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className="fx-auth">
      <div className="fx-auth-panel">
        <main className="fx-auth-form-pane">
          <div className="fx-auth-form-inner">
            <div className="fx-auth-brand">
              <span className="fx-auth-logo" aria-hidden="true">
                fx
              </span>
              <span className="fx-auth-brandname">Foldex</span>
              <AuthLocaleSwitcher />
            </div>

            {kicker ? <p className="fx-auth-kicker">{kicker}</p> : null}
            <h1 className="fx-auth-title">{title}</h1>
            {subtitle ? <p className="fx-auth-subtitle">{subtitle}</p> : null}

            {children}

            {footer ? <div className="fx-auth-footer">{footer}</div> : null}
          </div>
        </main>

        <aside className="fx-auth-marketing" aria-hidden="true">
          <div className="fx-auth-marketing-inner">
            <p className="fx-auth-marketing-kicker">{t('auth_marketing.kicker')}</p>
            <h2 className="fx-auth-marketing-title">{t('auth_marketing.title')}</h2>
            <ul className="fx-auth-marketing-list">
              <li>{t('auth_marketing.point_organize')}</li>
              <li>{t('auth_marketing.point_monitor')}</li>
              <li>{t('auth_marketing.point_private')}</li>
            </ul>
          </div>
        </aside>
      </div>
    </div>
  )
}

/** A form-level error banner. `role="alert"` so it is announced on appearance. */
export function AuthError({ message }: { message: string }) {
  if (!message) return null
  return (
    <div className="fx-auth-error" role="alert">
      {message}
    </div>
  )
}

export function AuthField({
  id,
  label,
  hint,
  children,
}: {
  id: string
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="fx-auth-field">
      <label className="fx-auth-label" htmlFor={id}>
        {label}
      </label>
      {children}
      {hint ? <p className="fx-auth-hint">{hint}</p> : null}
    </div>
  )
}

export function AuthSubmit({
  busy,
  disabled,
  children,
}: {
  busy: boolean
  /** Additional reason to block submission, e.g. an incomplete OTP field. */
  disabled?: boolean
  children: ReactNode
}) {
  return (
    <button type="submit" className="fx-auth-submit" disabled={busy || disabled}>
      {busy ? <span className="fx-auth-spinner" aria-hidden="true" /> : null}
      {children}
    </button>
  )
}
