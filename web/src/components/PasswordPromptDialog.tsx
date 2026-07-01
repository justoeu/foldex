import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useUnlockFolder } from '../api/folders'
import type { Folder } from '../api/types'

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
  const unlock = useUnlockFolder()
  useEscape(onCancel)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)

  const submit = async () => {
    if (!password || unlock.isPending) return
    setError(null)
    try {
      const data = await unlock.mutateAsync({ id: folder.id, password })
      onUnlocked({ token: data.unlock_token, expiresAt: new Date(data.expires_at).getTime() })
    } catch (e: unknown) {
      const code = (e as { response?: { data?: { error?: { code?: string } } } })?.response?.data?.error?.code
      setError(code === 'wrong_password' ? t('folder_lock.error_incorrect') : t('folder_lock.error_generic'))
      setPassword('')
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
      <div className="fx-modal fx-confirm">
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">
              <Icon d={I.lock} size={12} /> {t('folder_lock.kicker')}
            </div>
            <h2 className="fx-modal-title">{t('folder_lock.title', { name: folder.name })}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onCancel} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-confirm-body">
          <p>{t('folder_lock.body')}</p>
          <div className="fx-input" style={{ marginTop: 10 }}>
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
            <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, marginTop: 6 }}>
              <Icon d={I.alert} size={12} /> {error}
            </div>
          )}
        </div>

        <footer className="fx-confirm-foot">
          <button className="fx-confirm-btn" onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!password || unlock.isPending}
          >
            <Icon d={I.lock} size={13} stroke={2.2} /> {t('folder_lock.submit')}
          </button>
        </footer>
      </div>
    </div>
  )
}
