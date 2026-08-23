import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { sendStepUpCode } from '../../api/twofa'

/**
 * The "we can mail you a code instead" hint, as pure markup.
 *
 * Split from the self-managed button below because the two callers own the
 * state differently — the account forms hold their own, the two-factor section
 * has it on its controller — and they had drifted into two identical copies of
 * this markup rendered on the SAME page.
 */
export function MailCodeHint({
  sent,
  busy,
  onSend,
}: {
  sent: boolean
  busy: boolean
  onSend: () => void
}) {
  const { t } = useTranslation()
  return (
    <span className="fx-field-hint" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      {sent ? t('twofa.code_sent') : t('twofa.code_accepts_mailed')}
      {/* Element-qualified class: `.fx-shell button` is (0,1,1) and out-specifies
          a bare component class, which would strip this back to plain prose. */}
      <button type="button" className="fx-field-action" disabled={busy} onClick={onSend}>
        {t('twofa.mail_a_code')}
      </button>
    </span>
  )
}

/**
 * Asks the server to mail a step-up code, owning the request itself.
 *
 * The only proof an e-mail-only account can produce on these forms: it has no
 * authenticator to read six digits from, and the alternative — a recovery code —
 * is a lockout credential, too expensive to spend on a settings change.
 */
export function MailCodeButton({ disabled }: { disabled: boolean }) {
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)
  return (
    <MailCodeHint
      sent={sent}
      busy={disabled || busy}
      onSend={() => {
        setBusy(true)
        sendStepUpCode()
          .then(() => setSent(true))
          .catch(() => setSent(false))
          .finally(() => setBusy(false))
      }}
    />
  )
}
