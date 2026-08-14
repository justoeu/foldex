import { useReducer } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  confirmTotp,
  disableTotp,
  fetchTwoFactorStatus,
  regenerateRecoveryCodes,
  startTotp,
  type TotpEnrollment,
} from '../api/twofa'
import { apiErrorCode } from '../lib/apiError'
import { useConfirm } from '../components/ConfirmDialog'
import { useAuth } from '../auth/AuthProvider'

type State = {
  enrollment: TotpEnrollment | null
  password: string
  code: string
  codes: string[] | null
  error: string
  busy: boolean
}

type Action =
  | { type: 'password'; value: string }
  | { type: 'code'; value: string }
  | { type: 'start' }
  | { type: 'enrollment'; value: TotpEnrollment }
  | { type: 'codes'; value: string[] }
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
      return { ...state, codes: action.value, enrollment: null, password: '', code: '' }
    case 'failure':
      return { ...state, error: action.message, code: action.clearCode ? '' : state.code }
    case 'reset':
      return { ...state, enrollment: null, password: '', code: '', error: '' }
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

  const begin = async () => {
    dispatch({ type: 'start' })
    try {
      dispatch({ type: 'enrollment', value: await startTotp(state.password) })
    } catch (error) {
      dispatch({ type: 'failure', message: messageFor(error, t) })
    } finally {
      dispatch({ type: 'settled' })
    }
  }

  const confirm = async () => {
    dispatch({ type: 'start' })
    try {
      const result = await confirmTotp(state.code)
      dispatch({ type: 'codes', value: result.recovery_codes })
      await finishMutation(true)
    } catch (error) {
      dispatch({ type: 'failure', message: messageFor(error, t), clearCode: true })
    } finally {
      dispatch({ type: 'settled' })
    }
  }

  const turnOff = async () => {
    const approved = await confirmAction({
      title: t('twofa.disable'),
      message: t('twofa.disable_warning'),
      destructive: true,
    })
    if (!approved) return
    dispatch({ type: 'start' })
    try {
      await disableTotp(state.password, state.code)
      dispatch({ type: 'reset' })
      await finishMutation(true)
    } catch (error) {
      dispatch({ type: 'failure', message: messageFor(error, t), clearCode: true })
    } finally {
      dispatch({ type: 'settled' })
    }
  }

  const regenerate = async () => {
    dispatch({ type: 'start' })
    try {
      const codes = await regenerateRecoveryCodes(state.password, state.code)
      dispatch({ type: 'codes', value: codes })
      await finishMutation(false)
    } catch (error) {
      dispatch({ type: 'failure', message: messageFor(error, t), clearCode: true })
    } finally {
      dispatch({ type: 'settled' })
    }
  }

  return {
    ...state,
    enabled: status.data?.enabled ?? false,
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
  }
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
      return t('twofa.already_enabled')
    case 'totp_required_for_admins':
      return t('twofa.required_note')
    default:
      return t('auth_errors.generic')
  }
}
