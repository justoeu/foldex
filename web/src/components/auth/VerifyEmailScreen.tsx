import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { confirmEmailChange, errorCode, errorStatus, verifyEmail } from '../../api/auth'
import { AuthShell, AuthError } from './AuthShell'

type State = 'working' | 'done' | 'invalid' | 'failed' | 'taken'

/**
 * What the link in the reader's inbox is for.
 *
 * `verify` proves an address the account already has; `email-change` MOVES the
 * account to the address the message was sent to. One screen for both because
 * the mechanics are identical — a single-use token consumed on mount with no
 * session — and the ref-guard below is a trap nobody should have to get right
 * twice.
 */
export type ConfirmKind = 'verify' | 'email-change'

const CONSUME: Record<ConfirmKind, (token: string) => Promise<unknown>> = {
  verify: verifyEmail,
  'email-change': confirmEmailChange,
}

const COPY: Record<ConfirmKind, string> = {
  verify: 'auth_verify',
  'email-change': 'auth_email_change',
}

/**
 * Consumes a `#verify=` or `#email-change=` token from a confirmation e-mail.
 *
 * It runs UNAUTHENTICATED and immediately on mount, because the link is
 * followed from a mail client — often on a device that has never signed in.
 * Asking for a password first would meet the common case with a login form and
 * a token quietly expiring behind it.
 */
export function VerifyEmailScreen({
  token,
  onDone,
  kind = 'verify',
}: {
  token: string
  onDone: () => void
  kind?: ConfirmKind
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<State>('working')

  // A ref, and DELIBERATELY no per-effect `alive` flag.
  //
  // The ref alone is what stops the second request: the token is single-use, so
  // StrictMode's double mount would otherwise spend it on the first call and
  // render "no longer valid" from the second. Pairing it with an `alive` flag
  // breaks the screen entirely, and subtly — StrictMode runs effect → cleanup →
  // effect, the second pass returns early at the ref so no new closure exists,
  // and the resolved promise is judged against the FIRST closure whose cleanup
  // already set alive = false. Nothing renders. The user watches a spinner
  // forever over a token that was spent and stripped from the URL, so a reload
  // cannot retry either.
  //
  // Setting state after unmount is a no-op since React 18 (the warning was
  // removed precisely because guarding it caused bugs like that one).
  const sent = useRef(false)
  useEffect(() => {
    if (sent.current) return
    sent.current = true
    CONSUME[kind](token)
      .then(() => setState('done'))
      .catch((err) => {
        // A dead link and an unreachable server are different problems: the
        // first needs a new e-mail, the second needs a retry.
        const code = errorCode(err)
        if (code === 'verify_invalid' || code === 'email_change_invalid' || errorStatus(err) === 404) {
          setState('invalid')
        } else if (code === 'email_taken') {
          // The one failure worth telling apart: somebody claimed the address
          // between the request and the click, and the user can fix it by
          // choosing another. "Invalid link" would send them to support.
          setState('taken')
        } else {
          setState('failed')
        }
      })
  }, [token, kind])

  if (state === 'working') {
    return (
      <AuthShell kicker={t(`${COPY[kind]}.kicker`)} title={t(`${COPY[kind]}.working_title`)}>
        <p className="fx-auth-notice" role="status">
          <span className="fx-auth-spinner" aria-hidden="true" /> {t('auth.loading')}
        </p>
      </AuthShell>
    )
  }

  const ns = COPY[kind]
  const copy = {
    done: { title: `${ns}.done_title`, body: `${ns}.done_body` },
    invalid: { title: `${ns}.invalid_title`, body: `${ns}.invalid_body` },
    failed: { title: `${ns}.failed_title`, body: `${ns}.failed_body` },
    taken: { title: `${ns}.invalid_title`, body: 'account.err_email_taken' },
  }[state]

  return (
    <AuthShell kicker={t(ns + '.kicker')} title={t(copy.title)}>
      <div className="fx-auth-form">
        {state === 'done' ? (
          <p className="fx-auth-notice" role="status">
            {t(copy.body)}
          </p>
        ) : (
          <AuthError message={t(copy.body)} />
        )}
        <button type="button" className="fx-auth-submit" onClick={onDone}>
          {t(`${ns}.continue`)}
        </button>
      </div>
    </AuthShell>
  )
}
