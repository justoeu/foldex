import { useReducer } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  confirmEmailFactor,
  confirmTotp,
  disableEmailFactor,
  disableTotp,
  fetchTwoFactorStatus,
  regenerateRecoveryCodes,
  sendStepUpCode,
  startEmailFactor,
  startTotp,
  type EmailFactorEnrollment,
  type FactorMethod,
  type TotpEnrollment,
} from '../api/twofa'
import { apiErrorCode } from '../lib/apiError'
import { useConfirm } from '../components/ConfirmDialog'
import { useAuth } from '../auth/AuthProvider'

/**
 * An enrollment in flight, discriminated by method.
 *
 * A union rather than two nullable fields: the screen renders a QR for one and
 * a masked address for the other, and two independent nullables make "both set"
 * representable — a state whose only possible rendering is wrong.
 */
export type Enrollment =
  | { method: 'totp'; totp: TotpEnrollment }
  | { method: 'email'; email: EmailFactorEnrollment }

type State = {
  enrollment: Enrollment | null
  password: string
  code: string
  codes: string[] | null
  error: string
  busy: boolean
  /** Set once a step-up code has been mailed, so the UI can say so. */
  codeSent: boolean
}

type Action =
  | { type: 'password'; value: string }
  | { type: 'code'; value: string }
  | { type: 'start' }
  | { type: 'enrollment'; value: Enrollment }
  | { type: 'codes'; value: string[] }
  | { type: 'code-sent' }
  | { type: 'failure'; message: string; clearCode?: boolean }
  | { type: 'reset' }
  | { type: 'codes-done' }
  | { type: 'settled' }

const initialState: State = {
  enrollment: null,
  password: '',
  code: '',
  codes: null,
  error: '',
  busy: false,
  codeSent: false,
}

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case 'password':
      return { ...state, password: action.value }
    case 'code':
      return { ...state, code: action.value }
    case 'start':
      return { ...state, busy: true, error: '' }
    case 'enrollment':
      return { ...state, enrollment: action.value, code: '' }
    case 'codes':
      return {
        ...state,
        codes: action.value,
        enrollment: null,
        password: '',
        code: '',
        codeSent: false,
      }
    case 'code-sent':
      return { ...state, codeSent: true, error: '' }
    case 'failure':
      return { ...state, error: action.message, code: action.clearCode ? '' : state.code }
    case 'reset':
      return { ...state, enrollment: null, password: '', code: '', error: '', codeSent: false }
    case 'codes-done':
      return { ...state, codes: null }
    case 'settled':
      return { ...state, busy: false }
  }
}

export function useTwoFactorController() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const confirmAction = useConfirm()
  const { reload } = useAuth()
  const status = useQuery({ queryKey: ['twofa', 'status'], queryFn: fetchTwoFactorStatus })
  const [state, dispatch] = useReducer(reducer, initialState)

  const finishMutation = async (reloadAuth: boolean) => {
    await queryClient.invalidateQueries({ queryKey: ['twofa'] })
    if (reloadAuth) await reload()
  }

  /** Runs an action with the shared busy/error/settled bookkeeping. */
  const run = async (action: () => Promise<void>, clearCodeOnError = false) => {
    dispatch({ type: 'start' })
    try {
      await action()
    } catch (error) {
      dispatch({ type: 'failure', message: messageFor(error, t), clearCode: clearCodeOnError })
    } finally {
      dispatch({ type: 'settled' })
    }
  }

  const begin = (method: FactorMethod) =>
    run(async () => {
      dispatch({
        type: 'enrollment',
        value:
          method === 'totp'
            ? { method: 'totp', totp: await startTotp(state.password) }
            : { method: 'email', email: await startEmailFactor(state.password) },
      })
    })

  const confirm = () =>
    run(async () => {
      if (!state.enrollment) return
      const result =
        state.enrollment.method === 'totp'
          ? await confirmTotp(state.code)
          : await confirmEmailFactor(state.code)
      dispatch({ type: 'codes', value: result.recovery_codes })
      await finishMutation(true)
    }, true)

  const turnOff = async (method: FactorMethod) => {
    const approved = await confirmAction({
      title: method === 'totp' ? t('twofa.disable') : t('twofa.disable_email'),
      // The warning differs because the consequence does: removing the LAST
      // factor deletes the recovery codes with it, and the server decides which
      // removal that is. `enabled` minus the one going tells us.
      message: lastFactorGoing(status.data, method)
        ? t('twofa.disable_warning')
        : t('twofa.disable_one_warning'),
      destructive: true,
    })
    if (!approved) return
    await run(async () => {
      if (method === 'totp') await disableTotp(state.password, state.code)
      else await disableEmailFactor(state.password, state.code)
      dispatch({ type: 'reset' })
      await finishMutation(true)
    }, true)
  }

  const regenerate = () =>
    run(async () => {
      const codes = await regenerateRecoveryCodes(state.password, state.code)
      dispatch({ type: 'codes', value: codes })
      await finishMutation(false)
    }, true)

  const mailStepUpCode = () =>
    run(async () => {
      await sendStepUpCode()
      dispatch({ type: 'code-sent' })
    })

  return {
    ...state,
    enabled: status.data?.enabled ?? false,
    totpEnabled: status.data?.totp_enabled ?? false,
    emailEnabled: status.data?.email_enabled ?? false,
    emailAvailable: status.data?.email_available ?? false,
    canDisableTotp: status.data?.can_disable_totp ?? false,
    canDisableEmail: status.data?.can_disable_email ?? false,
    required: status.data?.required ?? false,
    remaining: status.data?.recovery_codes_remaining ?? 0,
    setPassword: (value: string) => dispatch({ type: 'password', value }),
    setCode: (value: string) => dispatch({ type: 'code', value }),
    reset: () => dispatch({ type: 'reset' }),
    dismissCodes: () => dispatch({ type: 'codes-done' }),
    begin,
    confirm,
    turnOff,
    regenerate,
    mailStepUpCode,
  }
}

/** True when removing `method` would leave the account with no second factor. */
function lastFactorGoing(status: { totp_enabled: boolean; email_enabled: boolean } | undefined,
  method: FactorMethod): boolean {
  if (!status) return true
  return method === 'totp' ? !status.email_enabled : !status.totp_enabled
}

function messageFor(
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  switch (apiErrorCode(error)) {
    case 'invalid_credentials':
      return t('twofa.wrong_password')
    case 'invalid_code':
      return t('auth_errors.invalid_code')
    case 'totp_already_enabled':
    case 'email_factor_already_enabled':
      return t('twofa.already_enabled')
    case 'totp_required_for_admins':
    case 'admin_2fa_required':
      return t('twofa.required_note')
    case 'email_factor_unavailable':
      return t('twofa.email_unavailable')
    case 'email_factor_not_enabled':
      return t('twofa.email_not_enrolled')
    default:
      return t('auth_errors.generic')
  }
}
