import { memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthLocaleSwitcher } from './AuthLocaleSwitcher'
import { Icon, I } from '../icons'
import { VERSION, BUILD_DATE, formatBuildDate } from '../../version'

/**
 * The two-pane frame every auth screen renders inside: the form on the left,
 * a product panel on the right.
 *
 * The right pane is `aria-hidden` and drops out entirely below 1024px. It
 * carries no information the user needs to authenticate, so exposing it to a
 * screen reader would just put three paragraphs and a mock window between the
 * heading and the e-mail field.
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
              <span className="fx-auth-brandblock">
                <span className="fx-auth-brandname">foldex</span>
                <span className="fx-auth-brandtag">{t('auth.brand_tagline')}</span>
              </span>
              <AuthLocaleSwitcher />
            </div>

            <div className="fx-auth-form-main">
              {kicker ? <p className="fx-auth-kicker">{kicker}</p> : null}
              <h1 className="fx-auth-title">{title}</h1>
              {subtitle ? <p className="fx-auth-subtitle">{subtitle}</p> : null}

              {children}

              {footer ? <div className="fx-auth-footer">{footer}</div> : null}
            </div>

            {/*
              The build stamp is the same pair the signed-in sidebar shows. It
              is here because the single most common support question about a
              self-hosted instance is "which version is this?", and the login
              screen is the one surface reachable without an account.
            */}
            <p className="fx-auth-build">
              foldex v{VERSION} · {formatBuildDate(BUILD_DATE)}
            </p>
          </div>
        </main>

        <AuthPromo />
      </div>
    </div>
  )
}

/** Sample folders for the mock window. Decorative — the pane is aria-hidden. */
const PROMO_FOLDERS = [
  { key: 'preview_folder_design', swatch: 'fx-auth-preview-swatch-amber', count: 9 },
  { key: 'preview_folder_reading', swatch: 'fx-auth-preview-swatch-accent', count: 4 },
  { key: 'preview_folder_tools', swatch: 'fx-auth-preview-swatch-pink', count: 12 },
  { key: 'preview_folder_ai', swatch: 'fx-auth-preview-swatch-green', count: 3 },
] as const

/*
 * Memoised because it takes no props and can never change, while its parent
 * re-renders on every keystroke in the sign-in form. Without this, typing a
 * password re-runs sixteen `t()` lookups and diffs three SVGs and a four-card
 * grid per character, for a subtree that is decoration and `aria-hidden`.
 */
const AuthPromo = memo(function AuthPromo() {
  const { t } = useTranslation()
  return (
    <aside className="fx-auth-marketing" aria-hidden="true">
      <div className="fx-auth-marketing-inner">
        <p className="fx-auth-badge">
          <span className="fx-auth-badge-dot" />
          {t('auth_marketing.badge')}
        </p>
        <h2 className="fx-auth-marketing-title">{t('auth_marketing.title')}</h2>
        <p className="fx-auth-marketing-lead">{t('auth_marketing.lead')}</p>

        <ul className="fx-auth-marketing-list">
          <li>
            <span className="fx-auth-marketing-icon">
              <Icon d={I.search} size={16} />
            </span>
            {t('auth_marketing.point_search')}
          </li>
          <li>
            <span className="fx-auth-marketing-icon">
              <Icon d={I.folder} size={16} />
            </span>
            {t('auth_marketing.point_organize')}
          </li>
          <li>
            <span className="fx-auth-marketing-icon">
              <Icon d={I.shield} size={16} />
            </span>
            {t('auth_marketing.point_private')}
          </li>
        </ul>

        <div className="fx-auth-preview">
          <div className="fx-auth-preview-bar">
            <span className="fx-auth-preview-dots">
              <i />
              <i />
              <i />
            </span>
            <span className="fx-auth-preview-search">{t('auth_marketing.preview_search')}</span>
            <span className="fx-auth-preview-cta">{t('auth_marketing.preview_new')}</span>
          </div>
          <div className="fx-auth-preview-grid">
            {PROMO_FOLDERS.map((folder) => (
              <div className="fx-auth-preview-card" key={folder.key}>
                <span className={`fx-auth-preview-swatch ${folder.swatch}`} />
                <span className="fx-auth-preview-name">{t(`auth_marketing.${folder.key}`)}</span>
                <span className="fx-auth-preview-count">
                  {t('auth_marketing.preview_links', { count: folder.count })}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </aside>
  )
})

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
  action,
  children,
}: {
  id: string
  label: string
  hint?: string
  /** Rendered on the label's own line, right-aligned — e.g. "forgot password?". */
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="fx-auth-field">
      <div className="fx-auth-label-row">
        <label className="fx-auth-label" htmlFor={id}>
          {label}
        </label>
        {action}
      </div>
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
