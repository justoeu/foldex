import { useRef } from 'react'
import { EditorContent } from '@tiptap/react'
import { useTranslation } from 'react-i18next'
import type { Note } from '../api/types'
import { useTags } from '../api/tags'
import { slugifyClient } from '../lib/slugify'
import { FolderPicker } from './FolderPicker'
import { Icon, I } from './icons'
import { NoteToolbar } from './NoteToolbar'
import { NoteDialogTagPicker } from './NoteDialogTagPicker'
import { useNoteDialogController } from './useNoteDialogController'

type Props = {
  note: Note | null
  defaultFolderId?: number | null
  onClose: () => void
}

export function NoteDialogEditor({ note, defaultFolderId, onClose }: Props) {
  const { t } = useTranslation()
  const { data: tags = [] } = useTags()
  const controller = useNoteDialogController({ note, defaultFolderId, onClose })
  const imageInputRef = useRef<HTMLInputElement>(null)

  return (
    <>
      <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
        <div className="fx-modal-col">
          <label className="fx-field">
            <span className="fx-field-label">{t('note_dialog.title_label')}</span>
            <div className="fx-input">
              <input autoFocus value={controller.title} onChange={(event) => controller.setTitle(event.target.value)} placeholder={t('note_dialog.title_placeholder')} aria-label={t('common.title_aria')} />
            </div>
          </label>

          <label className="fx-field">
            <span className="fx-field-label">{t('note_dialog.slug_label')}</span>
            <div className="fx-input">
              <span style={{ color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)', fontSize: 12, paddingRight: 4 }}>/n/</span>
              <input
                value={controller.slug}
                onChange={(event) => { controller.setSlug(event.target.value); controller.setSlugDirty(true) }}
                placeholder={slugifyClient(controller.title) || 'my-note'}
                aria-label={t('note_dialog.slug_aria')}
                pattern="[a-z0-9]+(-[a-z0-9]+)*"
                style={{ fontFamily: 'var(--fx-mono)' }}
              />
              {controller.slugDirty && (
                <button
                  type="button"
                  className="fx-iconbtn"
                  onClick={() => { controller.setSlug(slugifyClient(controller.title)); controller.setSlugDirty(false) }}
                  data-tooltip={t('note_dialog.slug_reset_tooltip')}
                  aria-label={t('note_dialog.slug_reset_tooltip')}
                >
                  <Icon d={I.refresh} size={13} />
                </button>
              )}
            </div>
            <span className="fx-field-hint">{t('note_dialog.slug_hint')}</span>
          </label>

          <div className="fx-field">
            <span className="fx-field-label">{t('note_dialog.body_label')}</span>
            <div className="fx-tiptap-wrap">
              <NoteToolbar editor={controller.editor} onInsertImage={() => imageInputRef.current?.click()} />
              <EditorContent editor={controller.editor} className="fx-tiptap" />
              <input
                ref={imageInputRef}
                type="file"
                accept="image/*"
                hidden
                onChange={(event) => {
                  const file = event.target.files?.[0]
                  if (file && controller.editor) controller.handleUpload(controller.editor.view, file)
                  event.target.value = ''
                }}
              />
            </div>
            {controller.imgUploadError && (
              <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, marginTop: 4 }}>
                <Icon d={I.alert} size={12} /> {controller.imgUploadError}
              </div>
            )}
          </div>

          <NoteDialogTagPicker tags={tags} selected={controller.selectedTags} setSelected={controller.setSelectedTags} />

          <label className="fx-field">
            <span className="fx-field-label">{t('note_dialog.folder_label')}</span>
            <FolderPicker selected={controller.folderId} onChange={controller.setFolderId} parentId={defaultFolderId ?? null} />
          </label>

          <label className="fx-toggle-row">
            <input type="checkbox" checked={controller.pinned} onChange={(event) => controller.setPinned(event.target.checked)} aria-label={t('note_dialog.pinned_aria')} />
            <span className="fx-toggle-track"><span className="fx-toggle-knob" /></span>
            <span className="fx-toggle-label">
              <Icon d={I.pin} size={12} /> {t('note_dialog.pinned_label')}
              <span className="fx-toggle-hint">{t('note_dialog.pinned_hint')}</span>
            </span>
          </label>
        </div>
      </div>

      {controller.saveError && (
        <div role="alert" style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, padding: '0 20px 8px' }}>
          <Icon d={I.alert} size={12} /> {controller.saveError}
        </div>
      )}

      <footer className="fx-modal-foot">
        <button type="button" className="fx-confirm-btn" onClick={onClose}>{t('common.cancel')}</button>
        <button type="button" className="fx-confirm-btn fx-confirm-btn-primary" onClick={() => void controller.submit()} disabled={!controller.title.trim() || controller.busy}>
          <Icon d={controller.isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}
          {controller.isEdit ? t('note_dialog.submit_save') : t('note_dialog.submit_create')}
        </button>
      </footer>
    </>
  )
}
