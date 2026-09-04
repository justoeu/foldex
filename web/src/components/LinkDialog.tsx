import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { FolderPicker } from './FolderPicker'
import { SlugField, useSlugFieldState } from './SlugField'
import { TagPicker, useTagPicker } from './TagPicker'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useDialogInitialFocus } from '../hooks/useDialogInitialFocus'
import { useLinkDialogForm } from '../hooks/useLinkDialogForm'
import { useLinkDialogImage } from '../hooks/useLinkDialogImage'
import { useLinkDialogSubmit } from '../hooks/useLinkDialogSubmit'
import { useExistingLinkByURL } from '../hooks/useExistingLinkByURL'
import { useFolders } from '../api/folders'
import { safeImageUrl, safeLinkHref, hostOf } from '../lib/url'
import { nextCheckPreview } from '../lib/time'
import type { Link } from '../api/types'

type Props = {
  open: boolean
  link: Link | null
  initialUrl?: string
  /** Where the dialog lands. 'image' is used by the card's add-image action. */
  focus?: 'url' | 'image'
  defaultFolderId?: number | null
  onClose: () => void
  onOpenExisting?: (link: Link) => void
}

type Form = ReturnType<typeof useLinkDialogForm> & ReturnType<typeof useSlugFieldState>
type Tags = ReturnType<typeof useTagPicker>
type Image = ReturnType<typeof useLinkDialogImage>

export function LinkDialog({ open, link, initialUrl, focus = 'url', defaultFolderId, onClose, onOpenExisting }: Props) {
  const { t } = useTranslation()
  const formState = useLinkDialogForm(open, link, initialUrl, defaultFolderId)
  const slugState = useSlugFieldState(open, formState.title, link?.slug, link?.id ?? null)
  const form = { ...formState, ...slugState }
  const tags = useTagPicker(open, link?.tags)
  const image = useLinkDialogImage(open, link)
  const { existing } = useExistingLinkByURL(
    form.url,
    open && (!link || form.url.trim() !== link.url),
    link?.id ?? null,
  )
  const duplicate = existing
  const save = useLinkDialogSubmit({
    link,
    values: form,
    selected: tags.selected,
    image,
    setSaveError: form.setSaveError,
    onClose,
  })
  const dialogRef = useRef<HTMLDivElement>(null)
  useEscape(onClose, open)
  useFocusTrap(dialogRef, open)
  // The image zone owns focus only when the caller asked for it; every other
  // entry point keeps landing on the URL, which is what a person opening the
  // dialog to EDIT expects. Passing the button ref means the browser scrolls it
  // into view on its own — load-bearing on mobile, where the image panel stacks
  // below the fold (INV-165) and would otherwise be focused but invisible.
  useDialogInitialFocus(open, dialogRef, focus === 'image' ? image.pickerRef : form.urlInputRef, focus === 'image')
  if (!open) return null

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={link ? t('link_dialog.edit_title') : t('link_dialog.kicker_create')}
    >
      <div className="fx-modal">
        <LinkDialogHeader
          isEdit={!!link}
          onClose={onClose}
        />
        <LinkDialogBody
          form={form}
          tags={tags}
          image={image}
          link={link}
          defaultFolderId={defaultFolderId}
          duplicate={duplicate}
          onOpenExisting={onOpenExisting}
        />
        <LinkDialogError message={duplicate ? null : form.saveError} />
        <LinkDialogFooter
          form={form}
          image={image}
          isEdit={!!link}
          busy={save.busy}
          blocked={!!duplicate}
          onClose={onClose}
          onSubmit={save.submit}
        />
      </div>
    </div>
  )
}

function LinkDialogError({ message }: { message: string | null }) {
  if (!message) return null
  return (
    <div className="fx-inline-error" role="alert" style={{ margin: '0 20px 10px' }}>
      <Icon d={I.alert} size={14} /> {message}
    </div>
  )
}

function DuplicateURLNotice({
  link,
  onOpenExisting,
}: {
  link: Link
  onOpenExisting?: (link: Link) => void
}) {
  const { t } = useTranslation()
  const { data: folders = [] } = useFolders({ fields: 'minimal' })
  const folderName = link.folder_id != null
    ? folders.find((folder) => folder.id === link.folder_id)?.name
    : undefined
  const where = folderName
    ? t('link_dialog.error_url_taken_in_folder', { name: folderName })
    : t('link_dialog.error_url_taken_on_home')
  return (
    <div className="fx-inline-error" role="alert">
      <Icon d={I.alert} size={14} />
      <div className="fx-inline-error-body">
        <div className="fx-inline-error-title">{t('link_dialog.error_url_taken')}</div>
        <div>{where}</div>
      </div>
      {onOpenExisting && (
        <button
          type="button"
          className="fx-confirm-btn"
          onClick={() => onOpenExisting(link)}
        >
          {t('link_dialog.open_existing')}
        </button>
      )}
    </div>
  )
}

function LinkDialogHeader({ isEdit, onClose }: { isEdit: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  return (
    <header className="fx-modal-head">
      <div>
        <div className="fx-modal-kicker">{isEdit ? t('link_dialog.kicker_edit') : t('link_dialog.kicker_create')}</div>
        <h2 className="fx-modal-title">{isEdit ? t('link_dialog.edit_title') : t('link_dialog.create_title')}</h2>
      </div>
      <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
        <Icon d={I.x} size={14} />
      </button>
    </header>
  )
}

function LinkDialogBody({
  form,
  tags,
  image,
  link,
  defaultFolderId,
  duplicate,
  onOpenExisting,
}: {
  form: Form
  tags: Tags
  image: Image
  link: Link | null
  defaultFolderId?: number | null
  duplicate: Link | null
  onOpenExisting?: (link: Link) => void
}) {
  return (
    <div className="fx-modal-body">
      <div className="fx-modal-col">
        <LinkBasicsFields form={form} duplicate={duplicate} onOpenExisting={onOpenExisting} />
        <LinkTagsField tags={tags} />
        <LinkOrganizationFields form={form} link={link} defaultFolderId={defaultFolderId} />
      </div>
      <aside className="fx-modal-side">
        <LinkStatus form={form} link={link} />
        <LinkImagePanel form={form} image={image} link={link} />
      </aside>
    </div>
  )
}

function LinkBasicsFields({
  form,
  duplicate,
  onOpenExisting,
}: {
  form: Form
  duplicate: Link | null
  onOpenExisting?: (link: Link) => void
}) {
  const { t } = useTranslation()
  return (
    <>
      <label className="fx-field">
        <span className="fx-field-label">{t('link_dialog.url_label')}</span>
        <div className="fx-input fx-input-url">
          <Icon d={I.link} size={15} />
          <input
            ref={form.urlInputRef}
            value={form.url}
            onChange={(event) => form.setUrl(event.target.value)}
            placeholder={t('link_dialog.url_placeholder')}
            aria-label={t('common.url_aria')}
            aria-invalid={duplicate ? true : undefined}
          />
        </div>
      </label>
      {duplicate && (
        <DuplicateURLNotice link={duplicate} onOpenExisting={onOpenExisting} />
      )}
      <label className="fx-field">
        <span className="fx-field-label">
          {t('link_dialog.title_label')}
          {form.autofillPending && (
            <span className="fx-spinner" aria-hidden="true" style={{ marginLeft: 8 }} />
          )}
        </span>
        <div className="fx-input">
          <input
            value={form.title}
            onChange={(event) => form.setTitle(event.target.value)}
            placeholder={t('link_dialog.title_placeholder')}
            aria-label={t('common.title_aria')}
            aria-busy={form.autofillPending || undefined}
          />
        </div>
        {form.autofillFailed && !form.title.trim() && (
          <span className="fx-field-hint fx-field-hint-warn">{t('link_dialog.autofill_failed')}</span>
        )}
      </label>
      <SlugField
        title={form.title}
        slug={form.slug}
        slugDirty={form.slugDirty}
        setSlug={form.setSlug}
        setSlugDirty={form.setSlugDirty}
        routePrefix="/go/"
        i18nPrefix="link_dialog"
        fallback="jira-board"
      />
      <label className="fx-field">
        <span className="fx-field-label">{t('link_dialog.description_label')}</span>
        <div className="fx-textarea-wrap">
          <textarea
            className="fx-textarea"
            value={form.description}
            onChange={(event) => form.setDescription(event.target.value.slice(0, 1000))}
            rows={3}
            maxLength={1000}
            aria-label={t('common.description_aria')}
          />
          <span className={descriptionCountClass(form.description.length)}>{form.description.length}/1000</span>
        </div>
      </label>
    </>
  )
}

function LinkTagsField({ tags }: { tags: Tags }) {
  return <TagPicker picker={tags} i18nPrefix="link_dialog" />
}

function LinkOrganizationFields({ form, link, defaultFolderId }: { form: Form; link: Link | null; defaultFolderId?: number | null }) {
  const { t } = useTranslation()
  return (
    <>
      <label className="fx-field">
        <span className="fx-field-label">{t('link_dialog.folder_label')}</span>
        <FolderPicker selected={form.folderId} onChange={form.setFolderId} parentId={defaultFolderId ?? null} />
      </label>
      <label className="fx-toggle-row">
        <input type="checkbox" checked={form.pinned} onChange={(event) => form.setPinned(event.target.checked)} aria-label={t('link_dialog.pinned_aria')} />
        <span className="fx-toggle-track"><span className="fx-toggle-knob" /></span>
        <span className="fx-toggle-label">
          <Icon d={I.pin} size={12} /> {t('link_dialog.pinned_label')}
          <span className="fx-toggle-hint">{t('link_dialog.pinned_hint')}</span>
        </span>
      </label>
      <label className="fx-field">
        <span className="fx-field-label"><Icon d={I.bell} size={12} /> {t('link_dialog.check_updates_label')}</span>
        <select
          className="fx-input"
          value={form.checkInterval ?? ''}
          onChange={(event) => {
            const value = event.target.value
            form.setCheckInterval(value === 'hourly' || value === 'daily' || value === 'weekly' ? value : null)
          }}
          aria-label={t('link_dialog.check_updates_label')}
        >
          <option value="">{t('link_dialog.check_updates_off')}</option>
          <option value="hourly">{t('link_dialog.check_updates_hourly')}</option>
          <option value="daily">{t('link_dialog.check_updates_daily')}</option>
          <option value="weekly">{t('link_dialog.check_updates_weekly')}</option>
        </select>
        <span className="fx-field-hint">{t('link_dialog.check_updates_hint')}</span>
        {form.checkInterval && (
          <span className="fx-field-hint" data-testid="check-next-preview">
            {t('link_dialog.check_updates_next', { when: nextCheckPreview(form.checkInterval, link?.last_checked_at, t) })}
          </span>
        )}
      </label>
    </>
  )
}

function LinkStatus({ form, link }: { form: Form; link: Link | null }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="fx-modal-side-label">{t('link_dialog.status_label')}</div>
      <div className="fx-modal-side-meta">
        <div className="fx-modal-side-meta-row"><Icon d={I.globe} size={13} /> {hostOf(form.url) || '—'}</div>
        <div className="fx-modal-side-meta-row"><Icon d={I.flame} size={13} /> {t('link_dialog.clicks_count', { count: link?.click_count ?? 0 })}</div>
        {form.pinned && (
          <div className="fx-modal-side-meta-row" style={{ color: 'var(--fx-accent)' }}>
            <Icon d={I.pin} size={13} /> {t('link_dialog.pinned_status')}
          </div>
        )}
      </div>
    </>
  )
}

function LinkImagePanel({ form, image, link }: { form: Form; image: Image; link: Link | null }) {
  const { t } = useTranslation()
  const storedImage = image.removed ? undefined : safeImageUrl(link?.og_image_url)
  const stagedPreview = safeImageUrl(image.preview)
  const remotePreview = image.removed || image.captureOnSave ? undefined : safeLinkHref(form.ogPreview)
  const currentImage = image.previewBroken ? undefined : (stagedPreview ?? storedImage ?? remotePreview)
  useEffect(() => {
    image.setPreviewBroken(false)
  }, [form.ogPreview, form.url])
  const canRemove = !image.removed && !!(image.preview || link?.og_image_url)
  const href = safeLinkHref(form.url)
  const previewFailed = link?.preview_status === 'failed' && !currentImage && !image.captureOnSave
  const captureLabel = image.busy ? t('link_dialog.image_capturing') : t('link_dialog.image_capture')
  return (
    <div className="fx-modal-side-preview" style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div className="fx-modal-side-label">{t('link_dialog.image_label')}</div>
        <div className="fx-modal-side-image-actions">
          <button
            type="button"
            className="fx-modal-side-icon"
            disabled={image.busy || !form.url.trim()}
            onClick={() => { void image.captureScreenshot() }}
            aria-label={captureLabel}
            data-tooltip={t('link_dialog.image_capture_hint')}
            data-tooltip-side="bottom"
          >
            {image.busy
              ? <span className="fx-spinner" aria-hidden="true" />
              : <Icon d={I.camera} size={14} />}
          </button>
          {href && (
            <a href={href} target="_blank" rel="noopener noreferrer" className="fx-modal-side-open-link">
              <Icon d={I.arrowR} size={11} /> {t('link_dialog.image_open_browser')}
            </a>
          )}
        </div>
      </div>
      {currentImage && (
        <LinkImagePreview
          url={currentImage}
          busy={image.busy}
          onBroken={() => image.setPreviewBroken(true)}
        />
      )}
      {(previewFailed || image.previewBroken) && (
        <div style={{ fontSize: 11, color: 'var(--fx-warn)', display: 'flex', alignItems: 'center', gap: 4 }}>
          <Icon d={I.alert} size={12} /> {t('link_dialog.image_preview_missing')}
        </div>
      )}
      <LinkImageUploadZone image={image} />
      {image.captureOnSave && (
        <div style={{ fontSize: 11, color: 'var(--fx-accent)', display: 'flex', alignItems: 'center', gap: 4 }}>
          <Icon d={I.check} size={12} /> {t('link_dialog.image_capture_on_save')}
        </div>
      )}
      {image.preview && !image.captureOnSave && (
        <div style={{ fontSize: 11, color: 'var(--fx-accent)', display: 'flex', alignItems: 'center', gap: 4 }}>
          <Icon d={I.check} size={12} /> {t('link_dialog.image_saved_with_link')}
        </div>
      )}
      {image.captureWarning && (
        <div className="fx-inline-warn" role="status">
          <Icon d={I.alert} size={12} /> {image.captureWarning}
        </div>
      )}
      {image.uploadError && (
        <div className="fx-inline-error" role="alert">
          <Icon d={I.alert} size={12} /> {image.uploadError}
        </div>
      )}
      {canRemove && (
        <button type="button" className="fx-confirm-btn" style={{ justifyContent: 'center', color: 'var(--fx-danger)' }} onClick={image.remove}>
          <Icon d={I.trash} size={13} /> {t('link_dialog.image_remove')}
        </button>
      )}
    </div>
  )
}

function LinkImagePreview({ url, busy, onBroken }: { url: string; busy: boolean; onBroken: () => void }) {
  const { t } = useTranslation()
  // Taint analysis tracks <input type=file>.files → createObjectURL → <img src>
  // as DOM-to-HTML. encodeURI is the sanitizer it recognizes; it is a no-op on
  // well-formed http(s)/blob/site-relative URLs that already passed the helper.
  const src = safeImageUrl(url)
  if (!src) return null
  return (
    <div className="fx-modal-side-ogwrap">
      <img
        src={encodeURI(src)}
        alt=""
        referrerPolicy="no-referrer"
        className="fx-modal-side-ogimg"
        decoding="async"
        onError={onBroken}
      />
      {busy && (
        <div className="fx-modal-side-uploading" aria-live="polite">
          <span className="fx-spinner" aria-hidden="true" />
          <span>{t('link_dialog.image_uploading')}</span>
        </div>
      )}
    </div>
  )
}

function LinkImageUploadZone({ image }: { image: Image }) {
  const { t } = useTranslation()
  return (
    <>
      <input
        ref={image.fileInputRef}
        type="file"
        accept="image/*"
        style={{ display: 'none' }}
        onChange={(event) => {
          const file = event.target.files?.[0]
          if (file) image.selectFile(file)
          event.target.value = ''
        }}
      />
      {/* A div rather than a <button>: it is also a drop target, and turning it
          into a button would put `.fx-img-upload-zone` on an element the UA
          resets — the cascade failure INV-154 exists to refuse. It gets the
          button ROLE and keyboard handling instead, which is what it was
          missing: until now the zone was mouse-only and unreachable by Tab. */}
      <div
        ref={image.pickerRef}
        role="button"
        tabIndex={0}
        aria-label={t('link_dialog.image_drop_hint')}
        // Both handlers already no-op while busy; without this the refusal is
        // invisible to a screen reader, which announces a button that does
        // nothing when pressed. A native <button disabled> would say it for
        // free, but this cannot be a <button> — see INV-154.
        aria-disabled={image.busy || undefined}
        className={'fx-img-upload-zone' + (image.dragging ? ' fx-img-upload-zone-drag' : '') + (image.busy ? ' fx-img-upload-zone-busy' : '')}
        onClick={() => !image.busy && image.fileInputRef.current?.click()}
        onKeyDown={(event) => {
          if (event.key !== 'Enter' && event.key !== ' ') return
          // Space scrolls the dialog body otherwise, which moves the very
          // panel the reader was sent here to use.
          event.preventDefault()
          if (!image.busy) image.fileInputRef.current?.click()
        }}
        onDragOver={(event) => {
          event.preventDefault()
          if (!image.busy) image.setDragging(true)
        }}
        onDragLeave={() => image.setDragging(false)}
        onDrop={(event) => {
          event.preventDefault()
          image.setDragging(false)
          if (image.busy) return
          const file = event.dataTransfer.files?.[0]
          if (file) image.selectFile(file)
        }}
      >
        {image.busy
          ? <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}><span className="fx-spinner" aria-hidden="true" /> {t('link_dialog.image_uploading')}</span>
          : image.preview ? t('link_dialog.image_selected_hint') : t('link_dialog.image_drop_hint')}
      </div>
    </>
  )
}

function LinkDialogFooter({
  form,
  image,
  isEdit,
  busy,
  blocked,
  onClose,
  onSubmit,
}: {
  form: Form
  image: Image
  isEdit: boolean
  busy: boolean
  blocked: boolean
  onClose: () => void
  onSubmit: () => Promise<void>
}) {
  const { t } = useTranslation()
  return (
    <footer className="fx-modal-foot">
      <button className="fx-confirm-btn" onClick={onClose}>{t('common.cancel')}</button>
      <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={() => void onSubmit()} disabled={!form.url.trim() || busy || blocked}>
        {image.busy
          ? <><span className="fx-spinner" aria-hidden="true" /> {t('link_dialog.image_uploading')}</>
          : <>{isEdit ? t('link_dialog.submit_save') : t('link_dialog.submit_create')}<Icon d={I.arrowR} size={14} stroke={2} /></>}
      </button>
    </footer>
  )
}

function descriptionCountClass(length: number): string {
  if (length >= 1000) return 'fx-textarea-count fx-textarea-count-limit'
  if (length >= 900) return 'fx-textarea-count fx-textarea-count-warn'
  return 'fx-textarea-count'
}
