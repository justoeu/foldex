import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { usePasswordGateMachine } from '../hooks/usePasswordGateMachine'
import { useUnlockFolder } from '../api/folders'
import type { Folder } from '../api/types'
import { PasswordGateForm } from './PasswordGateForm'

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

export function PasswordPromptProvider({ children }: Readonly<{ children: ReactNode }>) {
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
}: Readonly<{
  folder: Folder
  onCancel: () => void
  onUnlocked: (result: FolderUnlock) => void
}>) {
  const { t } = useTranslation()
  const unlock = useUnlockFolder()
  useEscape(onCancel)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)

  const doUnlock = useCallback(
    async (password: string): Promise<FolderUnlock> => {
      const data = await unlock.mutateAsync({ id: folder.id, password })
      return { token: data.unlock_token, expiresAt: new Date(data.expires_at).getTime() }
    },
    [unlock, folder.id],
  )

  const messages = useMemo(
    () => ({
      attemptsRemaining: (count: number) => t('folder_lock.attempts_remaining', { count }),
      incorrect: t('folder_lock.error_incorrect'),
      generic: t('folder_lock.error_generic'),
    }),
    [t],
  )

  const gate = usePasswordGateMachine({
    unlock: doUnlock,
    onUnlocked,
    hasHint: !!folder.password_hint,
    messages,
  })

  return (
    <PasswordGateForm
      dialogRef={dialogRef}
      folderName={folder.name}
      passwordHint={folder.password_hint}
      password={gate.password}
      error={gate.error}
      locked={gate.locked}
      remainingSecs={gate.remainingSecs}
      showHint={gate.showHint}
      submitting={unlock.isPending}
      onPasswordChange={gate.setPassword}
      onCancel={onCancel}
      onSubmit={() => void gate.submit()}
    />
  )
}
