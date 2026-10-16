import { useTranslation } from 'react-i18next'
import { PasswordStrength } from '../PasswordStrength'
import { PasswordInput } from '../PasswordInput'
import { AuthField } from './AuthShell'

type Props = Readonly<{
  passwordId: string
  confirmId: string
  password: string
  confirm: string
  minLen: number
  passwordInvalid?: boolean
  showMismatchHint?: boolean
  onPasswordChange: (value: string) => void
  onConfirmChange: (value: string) => void
}>

export function PasswordBlock({
  passwordId,
  confirmId,
  password,
  confirm,
  minLen,
  passwordInvalid = false,
  showMismatchHint = false,
  onPasswordChange,
  onConfirmChange,
}: Props) {
  const { t } = useTranslation()
  const mismatch = confirm.length > 0 && password !== confirm

  return (
    <>
      <AuthField
        id={passwordId}
        label={t('auth.password')}
        hint={t('auth.password_min', { count: minLen })}
      >
        <PasswordInput
          id={passwordId}
          className="fx-auth-input"
          autoComplete="new-password"
          required
          aria-invalid={passwordInvalid || undefined}
          value={password}
          onChange={(e) => onPasswordChange(e.target.value)}
        />
      </AuthField>
      <PasswordStrength value={password} />

      <AuthField id={confirmId} label={t('auth.confirm_password')}>
        <PasswordInput
          id={confirmId}
          className="fx-auth-input"
          autoComplete="new-password"
          required
          aria-invalid={mismatch || undefined}
          value={confirm}
          onChange={(e) => onConfirmChange(e.target.value)}
        />
      </AuthField>
      {showMismatchHint && mismatch ? <p className="fx-auth-hint fx-auth-hint-error">{t('auth_errors.password_mismatch')}</p> : null}
    </>
  )
}
