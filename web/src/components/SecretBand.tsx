import { useTranslation } from 'react-i18next'
import { useCopy } from '../hooks/useCopy'

type Props = {
  label: string
  /** The plaintext. Rendered as a text node — see the translate note below. */
  value: string
  /** Optional line under the value, for what the reader must do before leaving. */
  hint?: React.ReactNode
  testId?: string
  /** Rendered after the copy button — a "Done" or "Continue" that dismisses. */
  children?: React.ReactNode
}

/**
 * A secret shown exactly once, with a copy button.
 *
 * Three surfaces had grown this independently — the API-token plaintext, the
 * TOTP enrollment key and the generated temporary password — with the same
 * classes, the same inline `alignItems` override and three copies of the
 * swallow-on-failure clipboard handler. They are one shape because they answer
 * one question: the server cannot show this again, so it has to be copied
 * before the panel is dismissed.
 *
 * `translate="no"` is the reason this is a component rather than a convention.
 * Chrome and Edge page translation POST the page's visible text to
 * translate.googleapis.com; an `<input>` value is exempt, which is why
 * PasswordInput needs nothing, but a text node holding a credential is exactly
 * the shape that gets sent. Every surface that renders one has to remember —
 * and none of the three did, until it was found. One component is what makes
 * remembering unnecessary.
 */
export function SecretBand({ label, value, hint, testId, children }: Props) {
  const { t } = useTranslation()
  const { copied, copy } = useCopy()

  return (
    <div className="fx-secretband">
      <span className="fx-sec-block-label">{label}</span>
      <code className="fx-2fa-key-value" translate="no" data-testid={testId}>
        {value}
      </code>
      {hint}
      <div className="fx-sec-actions">
        <button type="button" className="fx-btn" onClick={() => void copy(value)}>
          {copied(value) ? t('tokens.copied') : t('tokens.copy')}
        </button>
        {children}
      </div>
    </div>
  )
}
