import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'

type ConfirmOpts = {
  title: string
  message?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  destructive?: boolean
}

type Resolver = (ok: boolean) => void

type Ctx = (opts: ConfirmOpts) => Promise<boolean>

const ConfirmCtx = createContext<Ctx | null>(null)

// Use this hook anywhere inside <ConfirmProvider> to replace window.confirm:
//   const confirm = useConfirm()
//   if (await confirm({ title: 'Apagar X?', destructive: true })) { ... }
export function useConfirm(): Ctx {
  const fn = useContext(ConfirmCtx)
  if (!fn) throw new Error('useConfirm must be used inside <ConfirmProvider>')
  return fn
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{
    opts: ConfirmOpts
    resolve: Resolver
  } | null>(null)

  const ask = useCallback<Ctx>((opts) => {
    return new Promise<boolean>((resolve) => {
      setState({ opts, resolve })
    })
  }, [])

  const close = (ok: boolean) => {
    state?.resolve(ok)
    setState(null)
  }

  return (
    <ConfirmCtx.Provider value={ask}>
      {children}
      {state && (
        <ConfirmModal
          {...state.opts}
          onCancel={() => close(false)}
          onConfirm={() => close(true)}
        />
      )}
    </ConfirmCtx.Provider>
  )
}

function ConfirmModal({
  title,
  message,
  confirmLabel,
  cancelLabel,
  destructive,
  onCancel,
  onConfirm,
}: ConfirmOpts & { onCancel: () => void; onConfirm: () => void }) {
  const { t } = useTranslation()
  useEscape(onCancel)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, true)
  const resolvedConfirm = confirmLabel ?? t('common.confirm')
  const resolvedCancel = cancelLabel ?? t('common.cancel')
  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={title}
      onKeyDown={(e) => {
        if (e.key === 'Enter') onConfirm()
      }}
    >
      <div className="fx-modal fx-confirm">
        <header className="fx-modal-head">
          <div>
            <div
              className={'fx-modal-kicker' + (destructive ? '' : ' fx-modal-kicker-info')}
            >
              {destructive ? t('common.destructive_action_kicker') : t('common.confirmation_kicker')}
            </div>
            <h2 className="fx-modal-title">{title}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onCancel} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        {message && <div className="fx-confirm-body">{message}</div>}

        <footer className="fx-confirm-foot">
          <button className="fx-confirm-btn" onClick={onCancel} autoFocus>
            {resolvedCancel}
          </button>
          <button
            className={
              'fx-confirm-btn ' +
              (destructive ? 'fx-confirm-btn-danger' : 'fx-confirm-btn-primary')
            }
            onClick={onConfirm}
          >
            {destructive ? <Icon d={I.trash} size={13} /> : null}
            {resolvedConfirm}
          </button>
        </footer>
      </div>
    </div>
  )
}
