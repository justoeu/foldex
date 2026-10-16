import { useTranslation } from 'react-i18next'
import { AuthShell, AuthError } from './AuthShell'
import { InviteForm } from './InviteForm'
import { useInviteFlow } from '../../hooks/useInviteFlow'

/**
 * Accepts an invitation.
 *
 * The e-mail is read-only and comes from the SERVER's view of the token, never
 * from anything the visitor can type. Letting the address be edited here would
 * turn a `user` invitation into "create an account for any address I like",
 * which is the whole reason self-signup is off.
 */
export function InviteScreen({ token, onGiveUp }: Readonly<{ token: string; onGiveUp: () => void }>) {
  const { t } = useTranslation()
  const flow = useInviteFlow(token)

  if (flow.lookupState === 'invalid') {
    return (
      <AuthShell title={t('auth_invite.invalid_title')} subtitle={t('auth_invite.invalid_body')}>
        <button type="button" className="fx-auth-submit" onClick={onGiveUp}>
          {t('auth_invite.back_to_login')}
        </button>
      </AuthShell>
    )
  }

  if (flow.lookupState === 'failed') {
    return (
      <AuthShell title={t('auth_invite.title')}>
        <div className="fx-auth-form">
          <AuthError message={t(`auth_errors.${flow.lookupError}`)} />
          <button
            type="button"
            className="fx-auth-submit"
            onClick={flow.retryLookup}
          >
            {t('auth_invite.retry')}
          </button>
        </div>
      </AuthShell>
    )
  }

  if (flow.lookupState === 'loading' || !flow.preview) {
    return (
      <AuthShell title={t('auth_invite.title')}>
        <div className="fx-auth-loading" role="status" aria-live="polite">
          <span className="fx-auth-spinner" aria-hidden="true" />
          {t('auth.loading')}
        </div>
      </AuthShell>
    )
  }

  return (
    <AuthShell
      kicker={t('auth_invite.kicker')}
      title={t('auth_invite.title')}
      subtitle={t('auth_invite.subtitle')}
    >
      <InviteForm
        token={token}
        email={flow.preview.email}
        googleEnabled={flow.googleEnabled}
        name={flow.name}
        password={flow.password}
        confirm={flow.confirm}
        error={flow.error}
        busy={flow.busy}
        minLen={flow.minLen}
        submitDisabled={flow.submitDisabled}
        onNameChange={flow.setName}
        onPasswordChange={flow.setPassword}
        onConfirmChange={flow.setConfirm}
        onSubmit={(e) => void flow.onSubmit(e)}
        onGoogleBeforeStart={flow.onGoogleBeforeStart}
        onGoogleBusyChange={flow.onGoogleBusyChange}
      />
    </AuthShell>
  )
}
