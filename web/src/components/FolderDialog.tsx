import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { GradientPicker } from './GradientPicker'
import { useCreateFolder, useUpdateFolder, useDeleteFolder } from '../api/folders'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useConfirm } from './ConfirmDialog'
import { isGradient, makeGradient, parseGradient } from '../lib/tagColor'
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
  const create = useCreateFolder()
  const update = useUpdateFolder()
  const del = useDeleteFolder()
  const confirm = useConfirm()

  useEffect(() => {
    if (!open) return
    if (folder) {
      setName(folder.name)
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
      setMode('solid')
      setSolid('#6366F1')
      setGradFrom('#6366F1')
      setGradTo('#EC4899')
    }
  }, [open, folder])

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  const finalColor = mode === 'gradient' ? makeGradient(gradFrom, gradTo) : solid

  const submit = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    if (isEdit && folder) {
      await update.mutateAsync({ id: folder.id, body: { name: trimmed, color: finalColor } })
    } else {
      await create.mutateAsync({ name: trimmed, color: finalColor, parent_id: parentId ?? null })
    }
    onClose()
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
                      aria-label={`color ${c}`}
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

        <footer className="fx-modal-foot">
          {isEdit && !isNaming && (
            <div style={{ display: 'flex', gap: 8, marginRight: 'auto' }}>
              <button
                className="fx-confirm-btn fx-confirm-btn-warn"
                onClick={onDeleteKeepLinks}
                disabled={busy}
                aria-label="delete folder keep links"
                data-tooltip={t('folder_dialog.delete_button_tooltip')}
                data-tooltip-side="top"
              >
                <Icon d={I.folder} size={13} stroke={2} /> {t('folder_dialog.delete_button')}
              </button>
              <button
                className="fx-confirm-btn fx-confirm-btn-danger"
                onClick={onDeleteCascade}
                disabled={busy}
                aria-label="delete folder and links"
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

