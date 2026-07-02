import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { FolderPicker } from './FolderPicker'
import { GradientPicker } from './GradientPicker'
import { useCreateFolder, useFolders, useUpdateFolder, useDeleteFolder } from '../api/folders'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useConfirm } from './ConfirmDialog'
import { isGradient, makeGradient, parseGradient } from '../lib/tagColor'
import { apiErrorCode } from '../lib/apiError'
import type { Folder } from '../api/types'

type Props = {
  open: boolean
  onClose: () => void
  folder?: Folder | null
  // When the dialog opens for a folder that was just auto-created (e.g. via
  // link-onto-link drag-and-drop merge), surfacing delete buttons is jarring
  // — the user is naming, not editing. `justCreated` swaps the kicker/title
  // to a naming context and hides the destructive actions.
  justCreated?: boolean
  // Parent for new folders. When set, the dialog creates the folder as a
  // child of this id. Ignored when editing (folder's own parent_id wins).
  parentId?: number | null
}

const DEFAULT_COLORS = [
  '#6366F1',
  '#0EA5E9',
  '#8B5CF6',
  '#EC4899',
  '#F59E0B',
  '#10B981',
  '#64748B',
  '#FFD400',
]

type Mode = 'solid' | 'gradient'

// FolderDialog mirrors TagDialog (same tagColor helper for solid/gradient) but
// targets the folder CRUD endpoints. Delete lives inside the dialog when in
// edit mode — links survive (ON DELETE SET NULL on the FK).
export function FolderDialog({ open, onClose, folder, justCreated, parentId }: Props) {
  const { t } = useTranslation()
  const isEdit = !!folder
  const isNaming = !!folder && !!justCreated
  const [name, setName] = useState('')
  const [mode, setMode] = useState<Mode>('solid')
  const [solid, setSolid] = useState('#6366F1')
  const [gradFrom, setGradFrom] = useState('#6366F1')
  const [gradTo, setGradTo] = useState('#EC4899')
  // The folder's parent in edit mode. Read once into local state on open
  // and tracked as a tri-state (`undefined` → "untouched", null → root,
  // number → another folder) so we can decide whether to include
  // parent_id in the PATCH body.
  const [parentChoice, setParentChoice] = useState<number | null>(null)
  const [parentDirty, setParentDirty] = useState(false)
  // Password (ADR-28). `password` covers both create and "set for the first
  // time" edit — no current-password proof needed either way. Editing an
  // ALREADY-protected folder's password is a distinct flow (passwordEditing
  // reveals current/new fields) since changing/removing it requires proving
  // the current one.
  const [password, setPassword] = useState('')
  const [passwordEditing, setPasswordEditing] = useState(false)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [removePassword, setRemovePassword] = useState(false)
  // Reminder hint (ADR-29). Prefilled from the folder's existing hint in edit
  // mode; must never equal the password (validated client-side before save and
  // authoritatively by the backend).
  const [hint, setHint] = useState('')
  const [passwordError, setPasswordError] = useState<string | null>(null)
  // Separate from passwordError (which is scoped to the password section and
  // only ever means "wrong current password") — this covers any OTHER save
  // failure (network, unexpected 5xx, etc.) so it isn't silently dropped as
  // an unhandled rejection. Shown near the footer, visible in every mode.
  const [saveError, setSaveError] = useState<string | null>(null)
  const create = useCreateFolder()
  const update = useUpdateFolder()
  const del = useDeleteFolder()
  const confirm = useConfirm()
  // Full folder tree, for descendant-cycle prevention in the picker.
  const { data: allFolders = [] } = useFolders()

  useEffect(() => {
    if (!open) return
    if (folder) {
      setName(folder.name)
      setParentChoice(folder.parent_id ?? null)
      setParentDirty(false)
      if (isGradient(folder.color)) {
        const { from, to } = parseGradient(folder.color)
        setMode('gradient')
        setGradFrom(from)
        setGradTo(to)
        setSolid(from)
      } else {
        setMode('solid')
        setSolid(folder.color)
        setGradFrom(folder.color)
        setGradTo('#EC4899')
      }
    } else {
      setName('')
      setParentChoice(null)
      setParentDirty(false)
      setMode('solid')
      setSolid('#6366F1')
      setGradFrom('#6366F1')
      setGradTo('#EC4899')
    }
    setPassword('')
    setPasswordEditing(false)
    setCurrentPassword('')
    setNewPassword('')
    setRemovePassword(false)
    setHint(folder?.password_hint ?? '')
    setPasswordError(null)
    setSaveError(null)
  }, [open, folder])

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  const finalColor = mode === 'gradient' ? makeGradient(gradFrom, gradTo) : solid

  const submit = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    setPasswordError(null)
    setSaveError(null)
    // Client-side hint≠password guard (backend also enforces). The password
    // being set is `newPassword` in the change flow, else `password`.
    const settingPw = folder?.has_password && passwordEditing && !removePassword ? newPassword : password
    const trimmedHint = hint.trim()
    if (trimmedHint && settingPw && trimmedHint.toLowerCase() === settingPw.toLowerCase()) {
      setPasswordError(t('folder_dialog.hint_equals_password_error'))
      return
    }
    try {
      if (isEdit && folder) {
        // Only send parent_id when the user actually touched the picker —
        // sending an unchanged value adds zero info and would surprise on
        // a future cycle-check change. Same idea for password: only
        // include it when the user actually touched a password field this
        // session (first-time set, or the change/remove flow).
        const body: {
          name: string
          color: string
          parent_id?: number | null
          password?: string | null
          current_password?: string
          password_hint?: string | null
        } = { name: trimmed, color: finalColor }
        if (parentDirty) body.parent_id = parentChoice
        if (!folder.has_password) {
          if (password) {
            body.password = password
            if (trimmedHint) body.password_hint = trimmedHint
          }
        } else if (passwordEditing) {
          if (removePassword) {
            body.password = null
            body.current_password = currentPassword
            // Password removal auto-clears the hint server-side; nothing to send.
          } else if (newPassword) {
            body.password = newPassword
            body.current_password = currentPassword
          }
        }
        // Standalone hint change on an already-protected folder (not removing
        // the password): send the tri-state only when it actually changed.
        if (folder.has_password && !(passwordEditing && removePassword)) {
          const current = folder.password_hint ?? ''
          if (trimmedHint !== current) body.password_hint = trimmedHint || null
        }
        await update.mutateAsync({ id: folder.id, body })
      } else {
        const body: {
          name: string
          color: string
          parent_id: number | null
          password?: string
          password_hint?: string
        } = {
          name: trimmed,
          color: finalColor,
          parent_id: parentId ?? null,
        }
        if (password) {
          body.password = password
          if (trimmedHint) body.password_hint = trimmedHint
        }
        await create.mutateAsync(body)
      }
      onClose()
    } catch (e: unknown) {
      const code = apiErrorCode(e)
      if (code === 'wrong_password') {
        setPasswordError(t('folder_dialog.wrong_password_error'))
        return
      }
      // Anything else (network failure, unexpected 5xx, a future error code
      // this dialog doesn't special-case) must still surface SOMETHING —
      // silently rethrowing from an onClick handler becomes an unhandled
      // promise rejection with no user-visible feedback at all.
      setSaveError(t('folder_dialog.save_error_generic'))
    }
  }

  const onDeleteKeepLinks = async () => {
    if (!folder) return
    const ok = await confirm({
      title: t('folder_dialog.delete_confirm_title'),
      message: t('folder_dialog.delete_confirm_body', { count: folder.link_count, name: folder.name }),
      confirmLabel: t('folder_dialog.delete_confirm_action'),
      destructive: true,
    })
    if (!ok) return
    await del.mutateAsync({ id: folder.id, cascade: false })
    onClose()
  }

  const onDeleteCascade = async () => {
    if (!folder) return
    const ok = await confirm({
      title: t('folder_dialog.delete_cascade_confirm_title'),
      message:
        folder.link_count > 0
          ? t('folder_dialog.delete_cascade_confirm_body', { count: folder.link_count, name: folder.name })
          : t('folder_dialog.delete_cascade_confirm_body_empty', { name: folder.name }),
      confirmLabel: t('folder_dialog.delete_cascade_confirm_action'),
      destructive: true,
    })
    if (!ok) return
    await del.mutateAsync({ id: folder.id, cascade: true })
    onClose()
  }

  const busy = create.isPending || update.isPending || del.isPending

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={isNaming ? t('folder_dialog.kicker_naming') : isEdit ? t('folder_dialog.kicker_edit') : t('folder_dialog.kicker_create')}
    >
      <div className="fx-modal" style={{ maxWidth: 480 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">
              <Icon d={I.folder} size={12} />{' '}
              {isNaming ? t('folder_dialog.kicker_naming') : isEdit ? t('folder_dialog.kicker_edit') : t('folder_dialog.kicker_create')}
            </div>
            <h2 className="fx-modal-title">
              {isNaming ? t('folder_dialog.naming_title') : isEdit ? t('folder_dialog.edit_title', { name: folder?.name ?? '' }) : t('folder_dialog.create_title')}
            </h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">{t('folder_dialog.name_label')}</span>
              <div className="fx-input">
                <input
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  onFocus={(e) => {
                    // After a merge the field comes pre-filled with "Nova pasta"
                    // — select it so the user can just start typing to overwrite.
                    if (isNaming) e.target.select()
                  }}
                  placeholder={t('folder_dialog.name_placeholder')}
                  aria-label={t('folder_dialog.name_aria')}
                />
              </div>
            </label>

            {isEdit && !isNaming && folder && (
              <label className="fx-field">
                <span className="fx-field-label">{t('folder_dialog.parent_label')}</span>
                <FolderPicker
                  selected={parentDirty ? parentChoice : (folder.parent_id ?? null)}
                  onChange={(id) => {
                    setParentChoice(id)
                    setParentDirty(true)
                  }}
                  excludeIds={descendantSet(folder.id, allFolders)}
                />
                <span className="fx-field-hint">{t('folder_dialog.parent_help')}</span>
              </label>
            )}

            {!isNaming && (!isEdit || (folder && !folder.has_password)) && (
              <label className="fx-field">
                <span className="fx-field-label">{t('folder_dialog.password_label')}</span>
                <div className="fx-input">
                  <input
                    type="password"
                    autoComplete="new-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder={t('folder_dialog.password_placeholder')}
                    aria-label={t('folder_dialog.password_label')}
                  />
                </div>
                <span className="fx-field-hint">{t('folder_dialog.password_hint')}</span>
              </label>
            )}

            {!isNaming && (!isEdit || (folder && !folder.has_password)) && password && (
              <label className="fx-field">
                <span className="fx-field-label">{t('folder_dialog.hint_label')}</span>
                <div className="fx-input">
                  <input
                    type="text"
                    value={hint}
                    maxLength={200}
                    onChange={(e) => {
                      setHint(e.target.value)
                      setPasswordError(null)
                    }}
                    placeholder={t('folder_dialog.hint_placeholder')}
                    aria-label={t('folder_dialog.hint_label')}
                  />
                </div>
                <span className="fx-field-hint">{t('folder_dialog.hint_help')}</span>
              </label>
            )}

            {/* Create/first-set mode: passwordError only ever carries the
                hint≠password validation message (wrong_password is edit-only). */}
            {!isNaming && (!isEdit || (folder && !folder.has_password)) && passwordError && (
              <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
                <Icon d={I.alert} size={12} /> {passwordError}
              </div>
            )}

            {!isNaming && isEdit && folder?.has_password && (
              <div className="fx-field">
                <span className="fx-field-label">{t('folder_dialog.password_label')}</span>
                {!passwordEditing ? (
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--fx-ink-3)' }}>
                      <Icon d={I.lock} size={13} /> {t('folder_dialog.password_protected_label')}
                    </span>
                    <button
                      type="button"
                      className="fx-pillbtn"
                      onClick={() => setPasswordEditing(true)}
                    >
                      {t('folder_dialog.change_password_action')}
                    </button>
                  </div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    <div className="fx-input">
                      <input
                        type="password"
                        autoFocus
                        autoComplete="off"
                        value={currentPassword}
                        onChange={(e) => {
                          setCurrentPassword(e.target.value)
                          setPasswordError(null)
                        }}
                        placeholder={t('folder_dialog.current_password_label')}
                        aria-label={t('folder_dialog.current_password_label')}
                      />
                    </div>
                    {!removePassword && (
                      <div className="fx-input">
                        <input
                          type="password"
                          autoComplete="new-password"
                          value={newPassword}
                          onChange={(e) => setNewPassword(e.target.value)}
                          placeholder={t('folder_dialog.password_placeholder')}
                          aria-label={t('folder_dialog.new_password_label')}
                        />
                      </div>
                    )}
                    <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fx-ink-3)' }}>
                      <input
                        type="checkbox"
                        checked={removePassword}
                        onChange={(e) => setRemovePassword(e.target.checked)}
                      />
                      {t('folder_dialog.remove_password_action')}
                    </label>
                    <button
                      type="button"
                      className="fx-pillbtn"
                      onClick={() => {
                        setPasswordEditing(false)
                        setCurrentPassword('')
                        setNewPassword('')
                        setRemovePassword(false)
                        setPasswordError(null)
                      }}
                      style={{ alignSelf: 'flex-start' }}
                    >
                      {t('common.cancel')}
                    </button>
                  </div>
                )}
                {passwordError && (
                  <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, marginTop: 6 }}>
                    <Icon d={I.alert} size={12} /> {passwordError}
                  </div>
                )}
              </div>
            )}

            {!isNaming && isEdit && folder?.has_password && !(passwordEditing && removePassword) && (
              <label className="fx-field">
                <span className="fx-field-label">{t('folder_dialog.hint_label')}</span>
                <div className="fx-input">
                  <input
                    type="text"
                    value={hint}
                    maxLength={200}
                    onChange={(e) => {
                      setHint(e.target.value)
                      setPasswordError(null)
                    }}
                    placeholder={t('folder_dialog.hint_placeholder')}
                    aria-label={t('folder_dialog.hint_label')}
                  />
                </div>
                <span className="fx-field-hint">{t('folder_dialog.hint_help')}</span>
              </label>
            )}

            <div className="fx-field">
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                <span className="fx-field-label" style={{ margin: 0 }}>{t('folder_dialog.color_label')}</span>
                <div className="fx-mode-toggle" role="tablist" aria-label={t('folder_dialog.color_mode_aria')}>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={mode === 'solid'}
                    className={'fx-mode-tab' + (mode === 'solid' ? ' fx-mode-tab-active' : '')}
                    onClick={() => setMode('solid')}
                  >
                    <Icon d={I.solid} size={11} /> {t('folder_dialog.color_solid')}
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={mode === 'gradient'}
                    className={'fx-mode-tab' + (mode === 'gradient' ? ' fx-mode-tab-active' : '')}
                    onClick={() => setMode('gradient')}
                  >
                    <Icon d={I.gradient} size={11} /> {t('folder_dialog.color_gradient')}
                  </button>
                </div>
              </div>

              {mode === 'solid' ? (
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                  {DEFAULT_COLORS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => setSolid(c)}
                      aria-label={t('common.color_swatch_aria', { c })}
                      style={{
                        width: 26,
                        height: 26,
                        borderRadius: 8,
                        background: c,
                        border:
                          c === solid ? '2px solid var(--fx-ink)' : '1px solid var(--fx-border)',
                        cursor: 'pointer',
                      }}
                    />
                  ))}
                  <input
                    type="color"
                    value={solid}
                    onChange={(e) => setSolid(e.target.value)}
                    style={{
                      width: 36,
                      height: 28,
                      border: 0,
                      background: 'transparent',
                      cursor: 'pointer',
                    }}
                    aria-label={t('folder_dialog.custom_color_aria')}
                  />
                </div>
              ) : (
                <GradientPicker
                  from={gradFrom}
                  to={gradTo}
                  onChange={(f, to) => {
                    setGradFrom(f)
                    setGradTo(to)
                  }}
                />
              )}
            </div>
          </div>
        </div>

        {saveError && (
          <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, padding: '0 20px 8px' }}>
            <Icon d={I.alert} size={12} /> {saveError}
          </div>
        )}

        <footer className="fx-modal-foot">
          {isEdit && !isNaming && (
            <div style={{ display: 'flex', gap: 8, marginRight: 'auto' }}>
              <button
                className="fx-confirm-btn fx-confirm-btn-warn"
                onClick={onDeleteKeepLinks}
                disabled={busy}
                aria-label={t('common.delete_folder_keep_links_aria')}
                data-tooltip={t('folder_dialog.delete_button_tooltip')}
                data-tooltip-side="top"
              >
                <Icon d={I.folder} size={13} stroke={2} /> {t('folder_dialog.delete_button')}
              </button>
              <button
                className="fx-confirm-btn fx-confirm-btn-danger"
                onClick={onDeleteCascade}
                disabled={busy}
                aria-label={t('common.delete_folder_and_links_aria')}
                data-tooltip={t('folder_dialog.delete_with_links_button_tooltip')}
                data-tooltip-side="top"
              >
                <Icon d={I.trash} size={13} stroke={2} /> {t('folder_dialog.delete_with_links_button')}
              </button>
            </div>
          )}
          <button className="fx-confirm-btn" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!name.trim() || busy}
          >
            <Icon d={isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}
            {isNaming ? t('folder_dialog.submit_done') : isEdit ? t('folder_dialog.submit_save') : t('folder_dialog.submit_create')}
          </button>
        </footer>
      </div>
    </div>
  )
}

// Collect rootId + every descendant id (transitive closure of parent_id).
// Used by the parent picker so the user can't move a folder under one of
// its own children — the backend already rejects cycles, but blocking it
// in the UI avoids a confusing error after a click.
function descendantSet(rootId: number, folders: Folder[]): Set<number> {
  const out = new Set<number>([rootId])
  let added = true
  while (added) {
    added = false
    for (const f of folders) {
      if (f.parent_id != null && out.has(f.parent_id) && !out.has(f.id)) {
        out.add(f.id)
        added = true
      }
    }
  }
  return out
}

