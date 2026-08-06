import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { errorCode, errorStatus, verifyEmail } from '../../api/auth'
import { AuthShell, AuthError } from './AuthShell'

type State = 'working' | 'done' | 'invalid' | 'failed'

/**
 * Consumes the `?verify=` token from a confirmation e-mail.
 *
 * It runs UNAUTHENTICATED and immediately on mount, because the link is
 * followed from a mail client — often on a device that has never signed in.
 * Asking for a password first would meet the common case with a login form and
 * a token quietly expiring behind it.
 */
export function VerifyEmailScreen({ token, onDone }: { token: string; onDone: () => void }) {
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
    verifyEmail(token)
      .then(() => setState('done'))
      .catch((err) => {
        // A dead link and an unreachable server are different problems: the
        // first needs a new e-mail, the second needs a retry.
        if (errorCode(err) === 'verify_invalid' || errorStatus(err) === 404) setState('invalid')
        else setState('failed')
      })
  }, [token])

  if (state === 'working') {
    return (
      <AuthShell kicker={t('auth_verify.kicker')} title={t('auth_verify.working_title')}>
        <p className="fx-auth-notice" role="status">
          <span className="fx-auth-spinner" aria-hidden="true" /> {t('auth.loading')}
        </p>
      </AuthShell>
    )
  }

  const copy = {
    done: { title: 'auth_verify.done_title', body: 'auth_verify.done_body' },
    invalid: { title: 'auth_verify.invalid_title', body: 'auth_verify.invalid_body' },
    failed: { title: 'auth_verify.failed_title', body: 'auth_verify.failed_body' },
  }[state]

  return (
    <AuthShell kicker={t('auth_verify.kicker')} title={t(copy.title)}>
      <div className="fx-auth-form">
        {state === 'done' ? (
          <p className="fx-auth-notice" role="status">
            {t(copy.body)}
          </p>
        ) : (
          <AuthError message={t(copy.body)} />
        )}
        <button type="button" className="fx-auth-submit" onClick={onDone}>
          {t('auth_verify.continue')}
        </button>
      </div>
    </AuthShell>
  )
}
