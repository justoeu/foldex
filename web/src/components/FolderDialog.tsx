import { useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { FolderPicker } from './FolderPicker'
import { ColorModeFields } from './ColorModeFields'
import { useFolders } from '../api/folders'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { type FolderUnlock } from './PasswordPromptDialog'
import { folderDescendants } from '../lib/folderDialogPayload'
import { useFolderDialogForm } from '../hooks/useFolderDialogForm'
import { useFolderDeleteController, useFolderSaveController } from '../hooks/useFolderDialogActions'
import type { Folder } from '../api/types'
import { PasswordInput } from './PasswordInput'

type Props = {
  open: boolean
  onClose: () => void
  folder?: Folder | null
  justCreated?: boolean
  parentId?: number | null
  unlockToken?: string
  onUnlocked?: (result: FolderUnlock) => void
}

type Form = ReturnType<typeof useFolderDialogForm>

export function FolderDialog(props: Props) {
  const { t } = useTranslation()
  const isEdit = !!props.folder
  const isNaming = isEdit && !!props.justCreated
  // The colours already on screen, so a new folder does not open on one of
  // them. Read here rather than inside the hook to keep the hook pure of data
  // fetching — the list is already in the cache for the parent picker.
  const { data: allFolders = [] } = useFolders()
  const takenColors = useMemo(
    () => allFolders.map((f) => f.color).filter((c): c is string => !!c),
    [allFolders],
  )
  const form = useFolderDialogForm(props.open, props.folder, takenColors)
  const save = useFolderSaveController({
    folder: props.folder,
    parentId: props.parentId,
    values: form,
    setPasswordError: form.setPasswordError,
    setSaveError: form.setSaveError,
    onClose: props.onClose,
  })
  const deletion = useFolderDeleteController({
    folder: props.folder,
    unlockToken: props.unlockToken,
    onUnlocked: props.onUnlocked,
    setPasswordError: form.setPasswordError,
    setSaveError: form.setSaveError,
    onClose: props.onClose,
  })
  const dialogRef = useRef<HTMLDivElement>(null)
  useEscape(props.onClose, props.open)
  useFocusTrap(dialogRef, props.open)
  if (!props.open) return null

  const busy = save.busy || deletion.busy
  const ariaLabel = isNaming
    ? t('folder_dialog.kicker_naming')
    : isEdit ? t('folder_dialog.kicker_edit') : t('folder_dialog.kicker_create')
  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={ariaLabel}
    >
      <div className="fx-modal" style={{ maxWidth: 480 }}>
        <FolderDialogHeader
          folder={props.folder}
          isEdit={isEdit}
          isNaming={isNaming}
          onClose={props.onClose}
        />
        <FolderDialogBody
          form={form}
          folder={props.folder}
          isEdit={isEdit}
          isNaming={isNaming}
        />
        <FolderDialogError message={form.saveError} />
        <FolderDialogFooter
          form={form}
          isEdit={isEdit}
          isNaming={isNaming}
          busy={busy}
          onClose={props.onClose}
          onSubmit={save.submit}
          onDeleteKeepingLinks={deletion.deleteKeepingLinks}
          onDeleteCascade={deletion.deleteCascade}
        />
      </div>
    </div>
  )
}

function FolderDialogError({ message }: { message: string | null }) {
  if (!message) return null
  return (
    <div role="alert" style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, padding: '0 20px 8px' }}>
      <Icon d={I.alert} size={12} /> {message}
    </div>
  )
}

function FolderDialogHeader({
  folder,
  isEdit,
  isNaming,
  onClose,
}: {
  folder?: Folder | null
  isEdit: boolean
  isNaming: boolean
  onClose: () => void
}) {
  const { t } = useTranslation()
  const kicker = isNaming
    ? t('folder_dialog.kicker_naming')
    : isEdit ? t('folder_dialog.kicker_edit') : t('folder_dialog.kicker_create')
  const title = isNaming
    ? t('folder_dialog.naming_title')
    : isEdit ? t('folder_dialog.edit_title', { name: folder?.name ?? '' }) : t('folder_dialog.create_title')
  return (
    <header className="fx-modal-head">
      <div>
        <div className="fx-modal-kicker"><Icon d={I.folder} size={12} />{' '}{kicker}</div>
        <h2 className="fx-modal-title">{title}</h2>
      </div>
      <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
        <Icon d={I.x} size={14} />
      </button>
    </header>
  )
}

function FolderDialogBody({
  form,
  folder,
  isEdit,
  isNaming,
}: {
  form: Form
  folder?: Folder | null
  isEdit: boolean
  isNaming: boolean
}) {
  return (
    <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
      <div className="fx-modal-col">
        <FolderNameField form={form} isNaming={isNaming} />
        {isEdit && !isNaming && folder && <FolderParentField form={form} folder={folder} />}
        <FolderPasswordSection form={form} folder={folder} isEdit={isEdit} isNaming={isNaming} />
        <ColorModeFields
          mode={form.mode}
          onModeChange={form.setMode}
          solid={form.solid}
          onSolidChange={form.setSolid}
          gradFrom={form.gradFrom}
          gradTo={form.gradTo}
          onGradientChange={(from, to) => {
            form.setGradFrom(from)
            form.setGradTo(to)
          }}
          i18nPrefix="folder_dialog"
        />
      </div>
    </div>
  )
}

function FolderNameField({ form, isNaming }: { form: Form; isNaming: boolean }) {
  const { t } = useTranslation()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('folder_dialog.name_label')}</span>
      <div className="fx-input">
        <input
          autoFocus
          value={form.name}
          onChange={(event) => form.setName(event.target.value)}
          onFocus={(event) => {
            if (isNaming) event.target.select()
          }}
          placeholder={t('folder_dialog.name_placeholder')}
          aria-label={t('folder_dialog.name_aria')}
        />
      </div>
    </label>
  )
}

function FolderParentField({ form, folder }: { form: Form; folder: Folder }) {
  const { t } = useTranslation()
  const { data: folders = [] } = useFolders()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('folder_dialog.parent_label')}</span>
      <FolderPicker
        selected={form.parentDirty ? form.parentChoice : (folder.parent_id ?? null)}
        onChange={(id) => {
          form.setParentChoice(id)
          form.setParentDirty(true)
        }}
        excludeIds={folderDescendants(folder.id, folders)}
      />
      <span className="fx-field-hint">{t('folder_dialog.parent_help')}</span>
    </label>
  )
}

function FolderPasswordSection({
  form,
  folder,
  isEdit,
  isNaming,
}: {
  form: Form
  folder?: Folder | null
  isEdit: boolean
  isNaming: boolean
}) {
  if (isNaming) return null
  if (!isEdit || !folder?.has_password) return <NewFolderPasswordFields form={form} />
  return <ProtectedFolderPasswordFields form={form} />
}

function NewFolderPasswordFields({ form }: { form: Form }) {
  const { t } = useTranslation()
  return (
    <>
      <label className="fx-field">
        <span className="fx-field-label">{t('folder_dialog.password_label')}</span>
        <div className="fx-input">
          <PasswordInput
            autoComplete="new-password"
            value={form.password}
            onChange={(event) => form.setPassword(event.target.value)}
            placeholder={t('folder_dialog.password_placeholder')}
            aria-label={t('folder_dialog.password_label')}
          />
        </div>
        <span className="fx-field-hint">{t('folder_dialog.password_hint')}</span>
      </label>
      {form.password && <FolderHintField form={form} />}
      {form.passwordError && <InlinePasswordError message={form.passwordError} />}
    </>
  )
}

function ProtectedFolderPasswordFields({ form }: { form: Form }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="fx-field">
        <span className="fx-field-label">{t('folder_dialog.password_label')}</span>
        {form.passwordEditing
          ? <PasswordEditor form={form} />
          : (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
              <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, color: 'var(--fx-ink-3)' }}>
                <Icon d={I.lock} size={13} /> {t('folder_dialog.password_protected_label')}
              </span>
              <button type="button" className="fx-pillbtn" onClick={() => form.setPasswordEditing(true)}>
                {t('folder_dialog.change_password_action')}
              </button>
            </div>
          )}
        {form.passwordError && <InlinePasswordError message={form.passwordError} marginTop />}
      </div>
      {!(form.passwordEditing && form.removePassword) && <FolderHintField form={form} />}
    </>
  )
}

function PasswordEditor({ form }: { form: Form }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div className="fx-input">
        <PasswordInput
          autoFocus
          autoComplete="off"
          value={form.currentPassword}
          onChange={(event) => {
            form.setCurrentPassword(event.target.value)
            form.setPasswordError(null)
          }}
          placeholder={t('folder_dialog.current_password_label')}
          aria-label={t('folder_dialog.current_password_label')}
        />
      </div>
      {!form.removePassword && (
        <div className="fx-input">
          <PasswordInput
            autoComplete="new-password"
            value={form.newPassword}
            onChange={(event) => form.setNewPassword(event.target.value)}
            placeholder={t('folder_dialog.password_placeholder')}
            aria-label={t('folder_dialog.new_password_label')}
          />
        </div>
      )}
      <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fx-ink-3)' }}>
        <input type="checkbox" checked={form.removePassword} onChange={(event) => form.setRemovePassword(event.target.checked)} />
        {t('folder_dialog.remove_password_action')}
      </label>
      <button type="button" className="fx-pillbtn" onClick={form.resetPasswordEdit} style={{ alignSelf: 'flex-start' }}>
        {t('common.cancel')}
      </button>
    </div>
  )
}

function FolderHintField({ form }: { form: Form }) {
  const { t } = useTranslation()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('folder_dialog.hint_label')}</span>
      <div className="fx-input">
        <input
          type="text"
          value={form.hint}
          maxLength={200}
          onChange={(event) => {
            form.setHint(event.target.value)
            form.setPasswordError(null)
          }}
          placeholder={t('folder_dialog.hint_placeholder')}
          aria-label={t('folder_dialog.hint_label')}
        />
      </div>
      <span className="fx-field-hint">{t('folder_dialog.hint_help')}</span>
    </label>
  )
}

function InlinePasswordError({ message, marginTop }: { message: string; marginTop?: boolean }) {
  return (
    <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, marginTop: marginTop ? 6 : undefined }}>
      <Icon d={I.alert} size={12} /> {message}
    </div>
  )
}

function FolderDialogFooter({
  form,
  isEdit,
  isNaming,
  busy,
  onClose,
  onSubmit,
  onDeleteKeepingLinks,
  onDeleteCascade,
}: {
  form: Form
  isEdit: boolean
  isNaming: boolean
  busy: boolean
  onClose: () => void
  onSubmit: () => Promise<void>
  onDeleteKeepingLinks: () => Promise<void>
  onDeleteCascade: () => Promise<void>
}) {
  const { t } = useTranslation()
  const submitLabel = isNaming
    ? t('folder_dialog.submit_done')
    : isEdit ? t('folder_dialog.submit_save') : t('folder_dialog.submit_create')
  return (
    <footer className="fx-modal-foot">
      {isEdit && !isNaming && (
        <div style={{ display: 'flex', gap: 8, marginRight: 'auto' }}>
          <button
            className="fx-confirm-btn fx-confirm-btn-warn"
            onClick={() => void onDeleteKeepingLinks()}
            disabled={busy}
            aria-label={t('common.delete_folder_keep_links_aria')}
            data-tooltip={t('folder_dialog.delete_button_tooltip')}
            data-tooltip-side="top"
          >
            <Icon d={I.folder} size={13} stroke={2} /> {t('folder_dialog.delete_button')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-danger"
            onClick={() => void onDeleteCascade()}
            disabled={busy}
            aria-label={t('common.delete_folder_and_links_aria')}
            data-tooltip={t('folder_dialog.delete_with_links_button_tooltip')}
            data-tooltip-side="top"
          >
            <Icon d={I.trash} size={13} stroke={2} /> {t('folder_dialog.delete_with_links_button')}
          </button>
        </div>
      )}
      <button className="fx-confirm-btn" onClick={onClose}>{t('common.cancel')}</button>
      <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={() => void onSubmit()} disabled={!form.name.trim() || busy}>
        <Icon d={isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}{submitLabel}
      </button>
    </footer>
  )
}
