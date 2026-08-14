import { useRef } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { FolderPicker } from './FolderPicker'
import { TagChip } from './TagChip'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useDialogInitialFocus } from '../hooks/useDialogInitialFocus'
import { useLinkDialogForm } from '../hooks/useLinkDialogForm'
import { useLinkTagSelection } from '../hooks/useLinkTagSelection'
import { useLinkDialogImage } from '../hooks/useLinkDialogImage'
import { useLinkDialogSubmit } from '../hooks/useLinkDialogSubmit'
import { safeImageUrl, safeLinkHref, hostOf } from '../lib/url'
import { nextCheckPreview } from '../lib/time'
import { slugifyClient } from '../lib/slugify'
import type { Link } from '../api/types'

type Props = {
  open: boolean
  link: Link | null
  initialUrl?: string
  defaultFolderId?: number | null
  onClose: () => void
}

type Form = ReturnType<typeof useLinkDialogForm>
type Tags = ReturnType<typeof useLinkTagSelection>
type Image = ReturnType<typeof useLinkDialogImage>

export function LinkDialog({ open, link, initialUrl, defaultFolderId, onClose }: Props) {
  const { t } = useTranslation()
  const form = useLinkDialogForm(open, link, initialUrl, defaultFolderId)
  const tags = useLinkTagSelection(open, link)
  const image = useLinkDialogImage(open, link)
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
  useDialogInitialFocus(open, dialogRef, form.urlInputRef)
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
        />
        <LinkDialogError message={form.saveError} />
        <LinkDialogFooter
          form={form}
          image={image}
          isEdit={!!link}
          busy={save.busy}
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
    <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, padding: '0 20px 8px' }}>
      <Icon d={I.alert} size={12} /> {message}
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
}: {
  form: Form
  tags: Tags
  image: Image
  link: Link | null
  defaultFolderId?: number | null
}) {
  return (
    <div className="fx-modal-body">
      <div className="fx-modal-col">
        <LinkBasicsFields form={form} />
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

function LinkBasicsFields({ form }: { form: Form }) {
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
          />
        </div>
      </label>
      <label className="fx-field">
        <span className="fx-field-label">{t('link_dialog.title_label')}</span>
        <div className="fx-input">
          <input
            value={form.title}
            onChange={(event) => form.setTitle(event.target.value)}
            placeholder={t('link_dialog.title_placeholder')}
            aria-label={t('common.title_aria')}
          />
        </div>
        {form.autofillFailed && !form.title.trim() && (
          <span className="fx-field-hint fx-field-hint-warn">{t('link_dialog.autofill_failed')}</span>
        )}
      </label>
      <LinkSlugField form={form} />
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

function LinkSlugField({ form }: { form: Form }) {
  const { t } = useTranslation()
  const reset = () => {
    form.setSlug(slugifyClient(form.title))
    form.setSlugDirty(false)
  }
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('link_dialog.slug_label')}</span>
      <div className="fx-input">
        <span style={{ color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)', fontSize: 12, paddingRight: 4 }}>/go/</span>
        <input
          value={form.slug}
          onChange={(event) => {
            form.setSlug(event.target.value)
            form.setSlugDirty(true)
          }}
          placeholder={slugifyClient(form.title) || 'jira-board'}
          aria-label={t('link_dialog.slug_aria')}
          pattern="[a-z0-9]+(-[a-z0-9]+)*"
          style={{ fontFamily: 'var(--fx-mono)' }}
        />
        {form.slugDirty && (
          <button type="button" className="fx-iconbtn" onClick={reset} data-tooltip={t('link_dialog.slug_reset_tooltip')} aria-label={t('link_dialog.slug_reset_tooltip')}>
            <Icon d={I.refresh} size={13} />
          </button>
        )}
      </div>
      <span className="fx-field-hint">{t('link_dialog.slug_hint')}</span>
    </label>
  )
}

function LinkTagsField({ tags }: { tags: Tags }) {
  const { t } = useTranslation()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('link_dialog.tags_label')}</span>
      <div className="fx-tagpicker">
        {tags.selected.map((tag, index) => (
          <TagChip
            key={tag.id || `pending-${index}`}
            tag={tag}
            active
            closable
            onClick={tag._pending ? () => tags.cycleColor(index) : undefined}
            onClose={() => tags.remove(index)}
          />
        ))}
        <input
          className="fx-tagpicker-input"
          value={tags.filter}
          onChange={(event) => tags.setSearch(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && tags.canCreate) {
              event.preventDefault()
              tags.queue()
            }
          }}
          placeholder={t('link_dialog.tags_search_placeholder')}
          aria-label={t('common.tag_filter_aria')}
        />
      </div>
      {tags.selected.some((tag) => tag._pending) && (
        <div className="fx-tag-hint">
          <Trans i18nKey="link_dialog.pending_tag_color_hint_html" components={{ strong: <strong /> }} />
        </div>
      )}
      {(tags.filtered.length > 0 || tags.canCreate) && <LinkTagSuggestions tags={tags} />}
    </label>
  )
}

function LinkTagSuggestions({ tags }: { tags: Tags }) {
  const { t } = useTranslation()
  return (
    <div style={{ marginTop: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
        <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
          {t('link_dialog.tags_registered_label')}
        </span>
        {tags.totalPages > 1 && <TagPagination tags={tags} />}
      </div>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {tags.pageTags.map((tag) => <TagChip key={tag.id} tag={tag} onClick={() => tags.add(tag)} />)}
        {tags.canCreate && (
          <button type="button" className="fx-pillbtn" onClick={tags.queue} style={{ fontSize: 11 }}>
            <Icon d={I.plus} size={11} /> {t('link_dialog.tags_create_inline', { name: tags.filter.trim() })}
          </button>
        )}
      </div>
    </div>
  )
}

function TagPagination({ tags }: { tags: Tags }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
      <button type="button" className="fx-iconbtn" disabled={tags.page === 0} onClick={() => tags.setPage((page) => page - 1)} aria-label={t('link_dialog.tags_page_prev_aria')} style={{ width: 22, height: 22 }}>
        <Icon d={I.chevronLeft} size={12} />
      </button>
      <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', minWidth: 32, textAlign: 'center' }}>
        {tags.page + 1}/{tags.totalPages}
      </span>
      <button type="button" className="fx-iconbtn" disabled={tags.page >= tags.totalPages - 1} onClick={() => tags.setPage((page) => page + 1)} aria-label={t('link_dialog.tags_page_next_aria')} style={{ width: 22, height: 22 }}>
        <Icon d={I.chevronRight} size={12} />
      </button>
    </div>
  )
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
  const currentImage = image.preview ?? (image.removed ? undefined : safeImageUrl(link?.og_image_url))
  const hasImage = !image.removed && !!(image.preview || link?.og_image_url)
  const href = safeLinkHref(form.url)
  return (
    <div className="fx-modal-side-preview" style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div className="fx-modal-side-label">{t('link_dialog.image_label')}</div>
        {href && (
          <a href={href} target="_blank" rel="noopener noreferrer" className="fx-modal-side-open-link">
            <Icon d={I.arrowR} size={11} /> {t('link_dialog.image_open_browser')}
          </a>
        )}
      </div>
      {currentImage && <LinkImagePreview url={currentImage} busy={image.busy} />}
      <LinkImageUploadZone image={image} />
      {image.preview && (
        <div style={{ fontSize: 11, color: 'var(--fx-accent)', display: 'flex', alignItems: 'center', gap: 4 }}>
          <Icon d={I.check} size={12} /> {t('link_dialog.image_saved_with_link')}
        </div>
      )}
      {image.uploadError && (
        <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
          <Icon d={I.alert} size={12} /> {image.uploadError}
        </div>
      )}
      {hasImage && (
        <button type="button" className="fx-confirm-btn" style={{ justifyContent: 'center', color: 'var(--fx-danger)' }} onClick={image.remove}>
          <Icon d={I.trash} size={13} /> {t('link_dialog.image_remove')}
        </button>
      )}
    </div>
  )
}

function LinkImagePreview({ url, busy }: { url: string; busy: boolean }) {
  const { t } = useTranslation()
  return (
    <div className="fx-modal-side-ogwrap">
      <img src={url} alt="preview" referrerPolicy="no-referrer" className="fx-modal-side-ogimg" />
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
      <div
        className={'fx-img-upload-zone' + (image.dragging ? ' fx-img-upload-zone-drag' : '') + (image.busy ? ' fx-img-upload-zone-busy' : '')}
        onClick={() => !image.busy && image.fileInputRef.current?.click()}
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
  onClose,
  onSubmit,
}: {
  form: Form
  image: Image
  isEdit: boolean
  busy: boolean
  onClose: () => void
  onSubmit: () => Promise<void>
}) {
  const { t } = useTranslation()
  return (
    <footer className="fx-modal-foot">
      <button className="fx-confirm-btn" onClick={onClose}>{t('common.cancel')}</button>
      <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={() => void onSubmit()} disabled={!form.url.trim() || busy}>
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
