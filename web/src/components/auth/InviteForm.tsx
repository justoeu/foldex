import type { FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { AuthError, AuthField, AuthSubmit } from './AuthShell'
import { AuthDivider, GoogleButton } from './GoogleButton'
import { PasswordBlock } from './PasswordBlock'

type Props = Readonly<{
  token: string
  email: string
  googleEnabled: boolean
  name: string
  password: string
  confirm: string
  error: string
  busy: boolean
  minLen: number
  submitDisabled: boolean
  onNameChange: (value: string) => void
  onPasswordChange: (value: string) => void
  onConfirmChange: (value: string) => void
  onSubmit: (e: FormEvent) => void
  onGoogleBeforeStart: () => boolean
  onGoogleBusyChange: (pending: boolean) => void
}>

export function InviteForm({
  token,
  email,
  googleEnabled,
  name,
  password,
  confirm,
  error,
  busy,
  minLen,
  submitDisabled,
  onNameChange,
  onPasswordChange,
  onConfirmChange,
  onSubmit,
  onGoogleBeforeStart,
  onGoogleBusyChange,
}: Props) {
  const { t } = useTranslation()
  return (
    <form className="fx-auth-form" onSubmit={onSubmit} noValidate>
      <AuthError message={error} />

      {googleEnabled && (
        <>
          <GoogleButton
            purpose="accept_invite"
            invite={token}
            disabled={busy}
            onBeforeStart={onGoogleBeforeStart}
            onBusyChange={onGoogleBusyChange}
          />
          <AuthDivider />
        </>
      )}

      <AuthField id="fx-invite-email" label={t('auth.email')} hint={t('auth_invite.email_locked')}>
        <input
          id="fx-invite-email"
          className="fx-auth-input"
          type="email"
          value={email}
          readOnly
          aria-readonly="true"
        />
      </AuthField>

      <AuthField id="fx-invite-name" label={t('auth.name')}>
        <input
          id="fx-invite-name"
          className="fx-auth-input"
          type="text"
          autoComplete="name"
          autoFocus
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
        />
      </AuthField>

      <PasswordBlock
        passwordId="fx-invite-password"
        confirmId="fx-invite-confirm"
        password={password}
        confirm={confirm}
        minLen={minLen}
        onPasswordChange={onPasswordChange}
        onConfirmChange={onConfirmChange}
      />

      <AuthSubmit busy={busy} disabled={submitDisabled}>
        {t('auth_invite.submit')}
      </AuthSubmit>
    </form>
  )
}
