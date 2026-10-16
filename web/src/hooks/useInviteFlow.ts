import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { acceptInvite, lookupInvite, type InvitePreview } from '../api/auth'
import { apiErrorCode, apiErrorStatus } from '../lib/apiError'
import { useAuth } from '../auth/AuthProvider'
import { passwordGateLen, usePasswordFloor } from './useInstancePolicy'

type LookupState = 'loading' | 'ready' | 'invalid' | 'failed'

export function useInviteFlow(token: string) {
  const { t } = useTranslation()
  const { adopt, session } = useAuth()
  const minLen = usePasswordFloor()
  const [preview, setPreview] = useState<InvitePreview | null>(null)
  const [lookupState, setLookupState] = useState<LookupState>('loading')
  const [lookupError, setLookupError] = useState<'network' | 'generic'>('network')
  const [lookupAttempt, setLookupAttempt] = useState(0)
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [googleBusy, setGoogleBusy] = useState(false)
  const submitting = useRef(false)
  const googleBusyRef = useRef(false)
  const tokenRef = useRef(token)
  const acceptAbortRef = useRef<AbortController | null>(null)
  tokenRef.current = token
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth

  useEffect(() => {
    acceptAbortRef.current?.abort()
    acceptAbortRef.current = null
    submitting.current = false
    googleBusyRef.current = false
    setBusy(false)
    setGoogleBusy(false)
    setName('')
    setPassword('')
    setConfirm('')
    setError('')
  }, [token])

  useEffect(() => () => acceptAbortRef.current?.abort(), [])

  useEffect(() => {
    const controller = new AbortController()
    setPreview(null)
    setLookupState('loading')
    lookupInvite(token, controller.signal)
      .then((p) => {
        if (controller.signal.aborted) return
        setPreview(p)
        setLookupState('ready')
      })
      .catch((err) => {
        if (controller.signal.aborted) return
        // Expired, revoked, already used and never-existed are one
        // indistinguishable 404 by design — so there is exactly one message.
        const code = apiErrorCode(err)
        if (code === 'invite_invalid' || code === 'not_found' || apiErrorStatus(err) === 404) {
          setLookupState('invalid')
          return
        }
        setLookupError((apiErrorStatus(err) ?? 0) === 0 ? 'network' : 'generic')
        setLookupState('failed')
      })
    return () => controller.abort()
  }, [token, lookupAttempt])

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting.current || googleBusyRef.current) return
    if (password !== confirm) {
      setError(t('auth_errors.password_mismatch'))
      return
    }
    submitting.current = true
    const submittedToken = token
    const controller = new AbortController()
    acceptAbortRef.current = controller
    setBusy(true)
    setError('')
    try {
      const response = await acceptInvite(token, name, password, controller.signal)
      if (!controller.signal.aborted && tokenRef.current === submittedToken) adopt(response)
    } catch (err) {
      if (!controller.signal.aborted && tokenRef.current === submittedToken) {
        setError(messageFor(err, t, minLen))
      }
    } finally {
      if (acceptAbortRef.current === controller) acceptAbortRef.current = null
      if (tokenRef.current === submittedToken) {
        submitting.current = false
        setBusy(false)
      }
    }
  }

  function onGoogleBeforeStart(): boolean {
    if (submitting.current || googleBusyRef.current) return false
    googleBusyRef.current = true
    setGoogleBusy(true)
    return true
  }

  function onGoogleBusyChange(pending: boolean) {
    googleBusyRef.current = pending
    setGoogleBusy(pending)
  }

  return {
    lookupState,
    lookupError,
    preview,
    retryLookup: () => setLookupAttempt((attempt) => attempt + 1),
    name,
    setName,
    password,
    setPassword,
    confirm,
    setConfirm,
    error,
    busy,
    googleBusy,
    googleEnabled,
    minLen,
    submitDisabled: googleBusy || (password.length > 0 && password.length < passwordGateLen(minLen)),
    onSubmit,
    onGoogleBeforeStart,
    onGoogleBusyChange,
  }
}

function messageFor(
  err: unknown,
  t: (k: string, o?: Record<string, unknown>) => string,
  minLen: number,
): string {
  switch (apiErrorCode(err)) {
    case 'invite_invalid':
      return t('auth_invite.invalid_body')
    case 'email_taken':
      return t('auth_errors.email_taken')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: minLen })
    case 'password_too_long':
      return t('auth_errors.password_too_long')
    case 'too_many_attempts':
      return t('auth_errors.too_many_attempts')
    default:
      return (apiErrorStatus(err) ?? 0) === 0 ? t('auth_errors.network') : t('auth_errors.generic')
  }
}
