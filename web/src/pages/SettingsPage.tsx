import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../components/icons'
import { PasswordStrength } from '../components/PasswordStrength'
import { useFolders, useResetFolderPassword } from '../api/folders'
import {
  useMasterPasswordStatus,
  useSetMasterPassword,
  useRemoveMasterPassword,
} from '../api/settings'
import { apiErrorCode as errCode } from '../lib/apiError'

type Props = {
  // Opens the folder edit dialog (to set a fresh password after a reset).
  onEditFolder?: (folderId: number) => void
}

export function SettingsPage({ onEditFolder }: Props) {
  const { t } = useTranslation()
  return (
    <div style={{ padding: 6, maxWidth: 720 }}>
      <div className="fx-pagehead" style={{ marginBottom: 18 }}>
        <div>
          <div className="fx-pagehead-kicker">{t('settings.page_kicker')}</div>
          <h1 className="fx-pagehead-h">{t('settings.page_title')}</h1>
        </div>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <MasterPasswordSection />
        <LockedFoldersSection onEditFolder={onEditFolder} />
      </div>
    </div>
  )
}

function MasterPasswordSection() {
  const { t } = useTranslation()
  const status = useMasterPasswordStatus()
  const setMaster = useSetMasterPassword()
  const removeMaster = useRemoveMasterPassword()

  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [hint, setHint] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [ok, setOk] = useState<string | null>(null)
  const configured = status.data?.configured === true
  const currentHint = status.data?.hint ?? null

  const save = async () => {
    setError(null)
    setOk(null)
    if (next.length < 8) {
      setError(t('settings.master_too_short'))
      return
    }
    if (next !== confirm) {
      setError(t('settings.master_mismatch'))
      return
    }
    const trimmedHint = hint.trim()
    if (trimmedHint && trimmedHint.toLowerCase() === next.toLowerCase()) {
      setError(t('settings.master_hint_equals'))
      return
    }
    try {
      await setMaster.mutateAsync({
        password: next,
        currentPassword: configured ? current : undefined,
        // Omit when empty → the backend keeps the existing hint (a password
        // change doesn't silently wipe it). A non-empty value replaces it.
        hint: trimmedHint || undefined,
      })
      setCurrent('')
      setNext('')
      setConfirm('')
      setHint('')
      setOk(configured ? t('settings.master_changed') : t('settings.master_set'))
    } catch (e) {
      setError(errCode(e) === 'wrong_password' ? t('settings.master_wrong_current') : t('settings.save_error'))
    }
  }

  const remove = async () => {
    setError(null)
    setOk(null)
    try {
      await removeMaster.mutateAsync({ currentPassword: current })
      setCurrent('')
      setNext('')
      setConfirm('')
      setHint('')
      setOk(t('settings.master_removed'))
    } catch (e) {
      setError(errCode(e) === 'wrong_password' ? t('settings.master_wrong_current') : t('settings.save_error'))
    }
  }

  const busy = setMaster.isPending || removeMaster.isPending

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('settings.master_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('settings.master_desc')}</p>

        <div
          style={{ fontSize: 12, display: 'flex', alignItems: 'center', gap: 6, color: configured ? 'var(--fx-ink-2)' : 'var(--fx-ink-4)' }}
        >
          <Icon d={configured ? I.check : I.info} size={13} />{' '}
          {configured ? t('settings.master_status_on') : t('settings.master_status_off')}
        </div>

        {configured && currentHint && (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-3)', display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon d={I.info} size={12} /> {t('settings.master_current_hint', { hint: currentHint })}
          </div>
        )}

        {configured && (
          <label className="fx-field" style={{ margin: 0 }}>
            <span className="fx-field-label">{t('settings.master_current_label')}</span>
            <div className="fx-input">
              <input
                type="password"
                autoComplete="off"
                value={current}
                onChange={(e) => {
                  setCurrent(e.target.value)
                  setError(null)
                }}
                placeholder={t('settings.master_current_placeholder')}
                aria-label={t('settings.master_current_label')}
              />
            </div>
          </label>
        )}

        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">
            {configured ? t('settings.master_new_label') : t('settings.master_new_label_first')}
          </span>
          <div className="fx-input">
            <input
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => {
                setNext(e.target.value)
                setError(null)
              }}
              placeholder={t('settings.master_new_placeholder')}
              aria-label={configured ? t('settings.master_new_label') : t('settings.master_new_label_first')}
            />
          </div>
          <span className="fx-field-hint">{t('settings.master_min_hint')}</span>
          <PasswordStrength value={next} />
        </label>

        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">{t('settings.master_confirm_label')}</span>
          <div className="fx-input">
            <input
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => {
                setConfirm(e.target.value)
                setError(null)
              }}
              placeholder={t('settings.master_confirm_placeholder')}
              aria-label={t('settings.master_confirm_label')}
            />
          </div>
          {confirm.length > 0 && next !== confirm && (
            <span className="fx-field-hint" style={{ color: 'var(--fx-danger)' }}>
              {t('settings.master_mismatch')}
            </span>
          )}
        </label>

        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">{t('settings.master_hint_label')}</span>
          <div className="fx-input">
            <input
              type="text"
              maxLength={200}
              value={hint}
              onChange={(e) => {
                setHint(e.target.value)
                setError(null)
              }}
              placeholder={configured ? t('settings.master_hint_placeholder_keep') : t('settings.master_hint_placeholder')}
              aria-label={t('settings.master_hint_label')}
            />
          </div>
          <span className="fx-field-hint">{t('settings.master_hint_help')}</span>
        </label>

        {error && (
          <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.alert} size={12} /> {error}
          </div>
        )}
        {ok && (
          <div style={{ fontSize: 11, color: 'var(--fx-ok, #10B981)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.check} size={12} /> {ok}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={save} disabled={busy || !next || next !== confirm}>
            <Icon d={I.check} size={13} stroke={2.2} />{' '}
            {configured ? t('settings.master_change_action') : t('settings.master_set_action')}
          </button>
          {configured && (
            <button className="fx-confirm-btn fx-confirm-btn-warn" onClick={remove} disabled={busy || !current}>
              <Icon d={I.trash} size={13} stroke={2} /> {t('settings.master_remove_action')}
            </button>
          )}
        </div>
      </div>
    </section>
  )
}

function LockedFoldersSection({ onEditFolder }: Props) {
  const { t } = useTranslation()
  const { data: folders = [] } = useFolders({ scope: null })
  // Once reset, a folder drops out of the `locked` list on refetch — but we
  // want its success row (and "set new password" affordance) to persist. Track
  // reset folders separately and render them as done rows even after they leave
  // the locked set.
  const [resetDone, setResetDone] = useState<{ id: number; name: string; color: string }[]>([])
  const doneIds = new Set(resetDone.map((f) => f.id))
  const locked = folders.filter((f) => f.has_password && !doneIds.has(f.id))
  const isEmpty = locked.length === 0 && resetDone.length === 0

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('settings.locked_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('settings.locked_desc')}</p>

        {isEmpty ? (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-4)' }}>{t('settings.locked_empty')}</div>
        ) : (
          <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
            {locked.map((f) => (
              <LockedFolderRow
                key={f.id}
                id={f.id}
                name={f.name}
                color={f.color}
                onDone={() => setResetDone((prev) => [...prev, { id: f.id, name: f.name, color: f.color }])}
              />
            ))}
            {resetDone.map((f) => (
              <DoneFolderRow key={f.id} id={f.id} name={f.name} color={f.color} onEditFolder={onEditFolder} />
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}

function DoneFolderRow({
  id,
  name,
  color,
  onEditFolder,
}: {
  id: number
  name: string
  color: string
  onEditFolder?: (folderId: number) => void
}) {
  const { t } = useTranslation()
  return (
    <li
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        border: '1px solid var(--fx-border)',
        borderRadius: 10,
        padding: 12,
      }}
    >
      <span style={{ width: 12, height: 12, borderRadius: 4, background: color, flex: '0 0 auto' }} />
      <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>{name}</span>
      <span style={{ fontSize: 12, color: 'var(--fx-ok, #10B981)', display: 'flex', alignItems: 'center', gap: 4 }}>
        <Icon d={I.check} size={13} /> {t('settings.reset_done')}
      </span>
      {onEditFolder && (
        <button className="fx-pillbtn" onClick={() => onEditFolder(id)}>
          {t('settings.reset_set_new')}
        </button>
      )}
    </li>
  )
}

function LockedFolderRow({
  id,
  name,
  color,
  onDone,
}: {
  id: number
  name: string
  color: string
  onDone: () => void
}) {
  const { t } = useTranslation()
  const reset = useResetFolderPassword()
  const { data: masterStatus } = useMasterPasswordStatus()
  const [open, setOpen] = useState(false)
  const [master, setMaster] = useState('')
  const [error, setError] = useState<string | null>(null)

  const submit = async () => {
    setError(null)
    try {
      await reset.mutateAsync({ id, masterPassword: master })
      setMaster('')
      setOpen(false)
      onDone()
    } catch (e) {
      const code = errCode(e)
      if (code === 'master_not_configured') setError(t('settings.reset_no_master'))
      else if (code === 'wrong_master_password') setError(t('settings.reset_wrong_master'))
      else setError(t('settings.save_error'))
    }
  }

  return (
    <li
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        border: '1px solid var(--fx-border)',
        borderRadius: 10,
        padding: 12,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ width: 12, height: 12, borderRadius: 4, background: color, flex: '0 0 auto' }} />
        <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>{name}</span>
        <button className="fx-pillbtn" onClick={() => setOpen((v) => !v)}>
          {t('settings.reset_action')}
        </button>
      </div>

      {open && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {masterStatus?.hint && (
            <div className="fx-field-hint" style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <Icon d={I.info} size={12} /> {t('settings.reset_master_hint', { hint: masterStatus.hint })}
            </div>
          )}
          <div className="fx-input">
            <input
              autoFocus
              type="password"
              autoComplete="off"
              value={master}
              onChange={(e) => {
                setMaster(e.target.value)
                setError(null)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  void submit()
                }
              }}
              placeholder={t('settings.reset_master_placeholder')}
              aria-label={t('settings.reset_master_placeholder')}
            />
          </div>
          {error && (
            <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
              <Icon d={I.alert} size={12} /> {error}
            </div>
          )}
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="fx-confirm-btn" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              className="fx-confirm-btn fx-confirm-btn-danger"
              onClick={submit}
              disabled={!master || reset.isPending}
            >
              <Icon d={I.refresh} size={13} stroke={2} /> {t('settings.reset_confirm')}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}
