import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useUnlockFolder } from '../api/folders'
import type { Folder } from '../api/types'

// The reminder hint stays hidden until this many wrong attempts (ADR-28) —
// nudges the user to try from memory first before the app coughs up the clue.
const HINT_AFTER_ATTEMPTS = 3

// Extracts the unlock endpoint's structured error (code + attempt counters +
// lockout expiry) from an axios rejection.
function parseUnlockError(e: unknown): {
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

function formatCountdown(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds))
  const m = Math.floor(s / 60)
  const r = s % 60
  return m > 0 ? `${m}m ${r}s` : `${r}s`
}

export type FolderUnlock = { token: string; expiresAt: number }

type Resolver = (result: FolderUnlock | null) => void

type Ctx = (folder: Folder) => Promise<FolderUnlock | null>

const PasswordPromptCtx = createContext<Ctx | null>(null)

// Use this hook anywhere inside <PasswordPromptProvider> to ask for a
// protected folder's password before entering it:
//   const promptPassword = usePasswordPrompt()
//   const result = await promptPassword(folder)  // null on cancel
export function usePasswordPrompt(): Ctx {
  const fn = useContext(PasswordPromptCtx)
  if (!fn) throw new Error('usePasswordPrompt must be used inside <PasswordPromptProvider>')
  return fn
}

export function PasswordPromptProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ folder: Folder; resolve: Resolver } | null>(null)

  const ask = useCallback<Ctx>((folder) => {
    return new Promise<FolderUnlock | null>((resolve) => {
      setState({ folder, resolve })
    })
  }, [])

  const close = (result: FolderUnlock | null) => {
    state?.resolve(result)
    setState(null)
  }

  return (
    <PasswordPromptCtx.Provider value={ask}>
      {children}
      {state && (
        <PasswordPromptModal
          folder={state.folder}
          onCancel={() => close(null)}
          onUnlocked={(result) => close(result)}
        />
      )}
    </PasswordPromptCtx.Provider>
  )
}

// The modal owns the whole unlock attempt (not just password collection) so
// a wrong password can show an inline error and let the user retry without
// the promise resolving/closing — matching FolderDialog/NoteDialog's inline-
// error convention rather than a toast.
function PasswordPromptModal({
  folder,
  onCancel,
  onUnlocked,
}: {
  folder: Folder
  onCancel: () => void
  onUnlocked: (result: FolderUnlock) => void
}) {
  const { t } = useTranslation()
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [failedAttempts, setFailedAttempts] = useState(0)
  const [lockedUntil, setLockedUntil] = useState<number | null>(null)
  const [nowTs, setNowTs] = useState(() => Date.now())
  const unlock = useUnlockFolder()
  useEscape(onCancel)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)

  // While locked out, tick every second so the countdown updates and the form
  // re-enables the moment the lockout expires.
  useEffect(() => {
    if (lockedUntil == null) return
    const iv = setInterval(() => setNowTs(Date.now()), 1000)
    return () => clearInterval(iv)
  }, [lockedUntil])

  const locked = lockedUntil != null && lockedUntil > nowTs
  const remainingSecs = lockedUntil != null ? (lockedUntil - nowTs) / 1000 : 0
  const showHint = !!folder.password_hint && failedAttempts >= HINT_AFTER_ATTEMPTS

  const submit = async () => {
    if (!password || unlock.isPending || locked) return
    setError(null)
    try {
      const data = await unlock.mutateAsync({ id: folder.id, password })
      onUnlocked({ token: data.unlock_token, expiresAt: new Date(data.expires_at).getTime() })
    } catch (e: unknown) {
      const { code, failedAttempts: fa, attemptsRemaining, lockedUntil: lu } = parseUnlockError(e)
      setPassword('')
      if (code === 'too_many_attempts' && lu) {
        setLockedUntil(lu)
        setNowTs(Date.now())
        setFailedAttempts((n) => Math.max(n, HINT_AFTER_ATTEMPTS))
        setError(null)
        return
      }
      if (code === 'wrong_password') {
        if (typeof fa === 'number') setFailedAttempts(fa)
        setError(
          typeof attemptsRemaining === 'number' && attemptsRemaining > 0
            ? t('folder_lock.attempts_remaining', { count: attemptsRemaining })
            : t('folder_lock.error_incorrect'),
        )
        return
      }
      setError(t('folder_lock.error_generic'))
    }
  }

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('folder_lock.dialog_aria', { name: folder.name })}
      onKeyDown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault()
          void submit()
        }
      }}
    >
      <div className="fx-modal fx-confirm fx-lockmodal">
        <header className="fx-modal-head">
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <span className="fx-lock-badge">
              <Icon d={I.lock} size={18} stroke={2} />
            </span>
            <div>
              <div className="fx-modal-kicker">{t('folder_lock.kicker')}</div>
              <h2 className="fx-modal-title">{t('folder_lock.title', { name: folder.name })}</h2>
            </div>
          </div>
          <button className="fx-confirm-x" onClick={onCancel} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-confirm-body">
          {locked ? (
            <div className="fx-lock-banner">
              <Icon d={I.clock} size={15} />
              <div>
                <strong>{t('folder_lock.locked_title')}</strong>
                <div>{t('folder_lock.locked', { time: formatCountdown(remainingSecs) })}</div>
              </div>
            </div>
          ) : (
            <>
              <p style={{ margin: 0 }}>{t('folder_lock.body')}</p>
              <div className="fx-input" style={{ marginTop: 12 }}>
                <input
                  autoFocus
                  type="password"
                  autoComplete="off"
                  value={password}
                  onChange={(e) => {
                    setPassword(e.target.value)
                    setError(null)
                  }}
                  placeholder={t('folder_lock.input_placeholder')}
                  aria-label={t('folder_lock.input_label')}
                />
              </div>
              {error && (
                <div className="fx-lock-error">
                  <Icon d={I.alert} size={12} /> {error}
                </div>
              )}
              {showHint && (
                <div className="fx-lock-hint">
                  <Icon d={I.info} size={12} /> {t('folder_lock.hint', { hint: folder.password_hint })}
                </div>
              )}
            </>
          )}
        </div>

        <footer className="fx-confirm-foot">
          <button className="fx-confirm-btn" onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!password || unlock.isPending || locked}
          >
            <Icon d={I.lock} size={13} stroke={2.2} /> {t('folder_lock.submit')}
          </button>
        </footer>
      </div>
    </div>
  )
}
