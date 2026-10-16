import { useCallback, useEffect, useState } from 'react'
import type { FolderUnlock } from '../components/PasswordPromptDialog'

// The reminder hint stays hidden until this many wrong attempts (ADR-28) —
// nudges the user to try from memory first before the app coughs up the clue.
export const HINT_AFTER_ATTEMPTS = 3

export type PasswordGateState =
  | { kind: 'idle'; error: string | null }
  | { kind: 'awaiting-code' }
  | { kind: 'submitting' }
  | { kind: 'wrong-code'; error: string; failedAttempts: number }
  | { kind: 'locked-out'; lockedUntil: number; failedAttempts: number }

type Messages = Readonly<{
  attemptsRemaining: (count: number) => string
  incorrect: string
  generic: string
}>

type Options = Readonly<{
  unlock: (password: string) => Promise<FolderUnlock>
  onUnlocked: (result: FolderUnlock) => void
  hasHint: boolean
  messages: Messages
}>

// Extracts the unlock endpoint's structured error (code + attempt counters +
// lockout expiry) from an axios rejection.
export function parseUnlockError(e: unknown): {
  code?: string
  failedAttempts?: number
  attemptsRemaining?: number
  lockedUntil?: number
} {
  const data = (e as { response?: { data?: Record<string, unknown> } })?.response?.data
  const err = data?.error as { code?: string } | undefined
  const lu = data?.locked_until
  return {
    code: err?.code,
    failedAttempts: typeof data?.failed_attempts === 'number' ? data.failed_attempts : undefined,
    attemptsRemaining: typeof data?.attempts_remaining === 'number' ? data.attempts_remaining : undefined,
    lockedUntil: typeof lu === 'string' ? new Date(lu).getTime() : undefined,
  }
}

export function formatCountdown(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds))
  const m = Math.floor(s / 60)
  const r = s % 60
  return m > 0 ? `${m}m ${r}s` : `${r}s`
}

export function usePasswordGateMachine({ unlock, onUnlocked, hasHint, messages }: Options) {
  const [password, setPassword] = useState('')
  const [state, setState] = useState<PasswordGateState>({ kind: 'idle', error: null })
  // The attempt counter outlives the lockout state: the hint must still show
  // once the countdown expires, so it cannot be derived from the union alone.
  const [failedAttempts, setFailedAttempts] = useState(0)
  const [nowTs, setNowTs] = useState(() => Date.now())

  const lockedUntil = state.kind === 'locked-out' ? state.lockedUntil : null

  // While locked out, tick every second so the countdown updates and the form
  // re-enables the moment the lockout expires. The tick stops at expiry:
  // lockedUntil staying set would otherwise re-render at 1 Hz forever.
  useEffect(() => {
    if (lockedUntil == null) return
    const iv = setInterval(() => {
      const now = Date.now()
      setNowTs(now)
      if (now >= lockedUntil) {
        clearInterval(iv)
        setState((s) => (s.kind === 'locked-out' ? { kind: 'idle', error: null } : s))
      }
    }, 1000)
    return () => clearInterval(iv)
  }, [lockedUntil])

  const locked = lockedUntil != null && lockedUntil > nowTs
  const remainingSecs = lockedUntil != null ? (lockedUntil - nowTs) / 1000 : 0
  const showHint = hasHint && failedAttempts >= HINT_AFTER_ATTEMPTS
  const error = 'error' in state ? state.error : null

  const updatePassword = useCallback((value: string) => {
    setPassword(value)
    setState((s) => {
      if (s.kind === 'locked-out' || s.kind === 'submitting') return s
      return value ? { kind: 'awaiting-code' } : { kind: 'idle', error: null }
    })
  }, [])

  const submit = useCallback(async () => {
    if (!password || state.kind === 'submitting' || locked) return
    setState({ kind: 'submitting' })
    try {
      const result = await unlock(password)
      onUnlocked(result)
    } catch (e: unknown) {
      const parsed = parseUnlockError(e)
      setPassword('')
      if (parsed.code === 'too_many_attempts' && parsed.lockedUntil) {
        setNowTs(Date.now())
        setFailedAttempts((n) => Math.max(n, HINT_AFTER_ATTEMPTS))
        setState((s) => ({
          kind: 'locked-out',
          lockedUntil: parsed.lockedUntil as number,
          failedAttempts: Math.max(
            s.kind === 'wrong-code' ? s.failedAttempts : 0,
            HINT_AFTER_ATTEMPTS,
          ),
        }))
        return
      }
      if (parsed.code === 'wrong_password') {
        if (typeof parsed.failedAttempts === 'number') setFailedAttempts(parsed.failedAttempts)
        setState((s) => {
          let attempts = 0
          if (typeof parsed.failedAttempts === 'number') attempts = parsed.failedAttempts
          else if (s.kind === 'wrong-code') attempts = s.failedAttempts
          return {
            kind: 'wrong-code',
            error:
              typeof parsed.attemptsRemaining === 'number' && parsed.attemptsRemaining > 0
                ? messages.attemptsRemaining(parsed.attemptsRemaining)
                : messages.incorrect,
            failedAttempts: attempts,
          }
        })
        return
      }
      setState({ kind: 'idle', error: messages.generic })
    }
  }, [password, state.kind, locked, unlock, onUnlocked, messages])

  return { state, password, setPassword: updatePassword, submit, error, failedAttempts, locked, remainingSecs, showHint }
}
