import { useEffect, useMemo, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { TagChip } from './TagChip'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useCreateLink, useUpdateLink, uploadLinkImage, removeLinkImage } from '../api/links'
import { useCreateTag, useTags } from '../api/tags'
import { useFolders } from '../api/folders'
import { useQueryClient } from '@tanstack/react-query'
import type { Link, Tag } from '../api/types'

type Props = {
  open: boolean
  link: Link | null
  initialUrl?: string
  // Default folder for a new link — preselects the picker when the user is
  // creating a link while inside a folder view. Ignored in edit mode (the
  // link's own folder_id wins).
  defaultFolderId?: number | null
  onClose: () => void
}

// Tags being composed inside the link dialog. Real tags from the backend have
// `id > 0`; tags the user typed inline live with `id === 0` until the link is
// saved — at submit time we create them, get real ids, then attach to the
// link. This keeps the cancel button truly destructive (nothing was written
// to the DB if you bail out).
type SelectedTag = Tag & { _pending?: boolean }

// Click-to-cycle palette for pending tag chips. Spread evenly around the hue
// wheel at Tailwind's 500-weight so adjacent picks are visually distinct AND
// the chance of colliding with an existing tag is low — 20 swatches > the
// typical tag-count of personal-scale users.
const INLINE_PALETTE = [
  '#6366F1', // indigo
  '#3B82F6', // blue
  '#0EA5E9', // sky
  '#06B6D4', // cyan
  '#14B8A6', // teal
  '#10B981', // emerald
  '#22C55E', // green
  '#84CC16', // lime
  '#EAB308', // yellow
  '#F59E0B', // amber
  '#F97316', // orange
  '#EF4444', // red
  '#F43F5E', // rose
  '#EC4899', // pink
  '#D946EF', // fuchsia
  '#A855F7', // purple
  '#8B5CF6', // violet
  '#64748B', // slate
  '#78716C', // stone
  '#6B7280', // gray
]

export function LinkDialog({ open, link, initialUrl, defaultFolderId, onClose }: Props) {
  const { t } = useTranslation()
  const { data: tags = [] } = useTags()
  const { data: folders = [] } = useFolders()
  const createTag = useCreateTag()
  const createLink = useCreateLink()
  const updateLink = useUpdateLink()

  const [url, setUrl] = useState('')
  const [title, setTitle] = useState('')
  // Slug is auto-derived from title until the user touches the field. After
  // that it stays "dirty" — the user is in control. On submit, an empty slug
  // is sent as `null` (edit mode = regenerate from title) or omitted from
  // the create payload (= backend auto-generates).
  const [slug, setSlug] = useState('')
  const [slugDirty, setSlugDirty] = useState(false)
  const [description, setDescription] = useState('')
  const [pinned, setPinned] = useState(false)
  const [folderId, setFolderId] = useState<number | null>(null)
  const [selected, setSelected] = useState<SelectedTag[]>([])
  const [tagFilter, setTagFilter] = useState('')
  const [tagPage, setTagPage] = useState(0)
  const [imgUploadError, setImgUploadError] = useState<string | null>(null)
  const [isDragging, setIsDragging] = useState(false)
  const [pendingImage, setPendingImage] = useState<File | null>(null)
  const [pendingImagePreview, setPendingImagePreview] = useState<string | null>(null)
  const [imageRemoved, setImageRemoved] = useState(false)
  const [imageBusy, setImageBusy] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const qc = useQueryClient()

  useEffect(() => {
    if (!open) return
    setUrl(link?.url ?? initialUrl ?? '')
    setTitle(link?.title ?? '')
    setSlug(link?.slug ?? '')
    // Treat a pre-existing slug (edit mode) as dirty so we don't overwrite
    // the user's saved value while they edit other fields.
    setSlugDirty(!!link?.slug)
    setDescription(link?.description ?? '')
    setPinned(link?.pinned ?? false)
    setFolderId(link?.folder_id ?? defaultFolderId ?? null)
    setSelected(link?.tags ?? [])
    setTagFilter('')
    setTagPage(0)
    setImgUploadError(null)
    setIsDragging(false)
    setPendingImage(null)
    setPendingImagePreview(null)
    setImageRemoved(false)
  }, [open, link, initialUrl])

  // Live auto-derive the slug from the title until the user takes over.
  useEffect(() => {
    if (slugDirty) return
    setSlug(slugifyClient(title))
  }, [title, slugDirty])

  useEscape(onClose, open)

  const available = useMemo(
    () => tags.filter((tag) => !selected.some((s) => s.id === tag.id)),
    [tags, selected],
  )
  const filteredAvailable = useMemo(
    () =>
      tagFilter
        ? available.filter((tag) => tag.name.toLowerCase().includes(tagFilter.toLowerCase()))
        : available,
    [available, tagFilter],
  )
  useEffect(() => {
    const lastPage = Math.max(0, Math.ceil(filteredAvailable.length / 7) - 1)
    if (tagPage > lastPage) setTagPage(lastPage)
  }, [filteredAvailable.length, tagPage])
  const canCreateFromFilter =
    tagFilter.trim().length > 0 &&
    !tags.some((tag) => tag.name.toLowerCase() === tagFilter.trim().toLowerCase()) &&
    !selected.some((s) => s.name.toLowerCase() === tagFilter.trim().toLowerCase())

  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  // Queue a pending tag instead of creating it now. Cycling the color is done
  // by clicking the dot on the chip (see TagChip in render).
  const queueInlineTag = () => {
    const name = tagFilter.trim()
    if (!name) return
    setSelected([
      ...selected,
      { id: 0, name, color: INLINE_PALETTE[0], icon: null, _pending: true },
    ])
    setTagFilter('')
  }

  // Cycle through INLINE_PALETTE for a pending tag (real tags ignore clicks).
  const cycleColor = (idx: number) => {
    setSelected(
      selected.map((tag, i) => {
        if (i !== idx || !tag._pending) return tag
        const cur = INLINE_PALETTE.indexOf(tag.color)
        const next = INLINE_PALETTE[(cur + 1) % INLINE_PALETTE.length]
        return { ...tag, color: next }
      }),
    )
  }

  const handleImageFile = (file: File) => {
    if (!file.type.startsWith('image/')) {
      setImgUploadError(t('link_dialog.image_error_type'))
      return
    }
    setImgUploadError(null)
    setImageRemoved(false)
    // Always store locally — upload happens on Save for both new and edit
    setPendingImage(file)
    setPendingImagePreview(URL.createObjectURL(file))
  }

  const submit = async () => {
    const trimmed = url.trim()
    if (!trimmed) return

    // Resolve pending tags now — only when the user is committing the link.
    // If any of these fail (e.g. duplicate name vs another tag we don't know
    // about), the link save also fails so the user sees the error and can
    // recover without ending up with orphan tags.
    const tagIds: number[] = []
    for (const tag of selected) {
      if (tag.id) {
        tagIds.push(tag.id)
      } else {
        const created = await createTag.mutateAsync({ name: tag.name, color: tag.color })
        tagIds.push(created.id)
      }
    }

    if (link) {
      // Slug semantics on PATCH:
      //   user-typed slug      → send as string (backend validates + sets)
      //   field empty + dirty  → send as null  (backend regenerates from title)
      //   not dirty            → don't include the field (keep current slug)
      const slugTrimmed = slug.trim()
      const slugPayload: { slug?: string | null } = {}
      if (slugDirty) {
        slugPayload.slug = slugTrimmed === '' ? null : slugTrimmed
      }
      await updateLink.mutateAsync({
        id: link.id,
        body: {
          url: trimmed,
          title: title.trim() || trimmed,
          description: description.trim() || null,
          tag_ids: tagIds,
          pinned,
          folder_id: folderId,
          ...slugPayload,
        },
      })
      if (pendingImage) {
        setImageBusy(true)
        try {
          await uploadLinkImage(link.id, pendingImage)
          qc.invalidateQueries({ queryKey: ['links'] })
          qc.invalidateQueries({ queryKey: ['folders'] })
        } catch (e: unknown) {
          setImgUploadError(extractUploadErr(e, t('link_dialog.image_error_generic')))
          setImageBusy(false)
          return
        }
        setImageBusy(false)
      } else if (imageRemoved) {
        setImageBusy(true)
        try {
          await removeLinkImage(link.id)
          qc.invalidateQueries({ queryKey: ['links'] })
          qc.invalidateQueries({ queryKey: ['folders'] })
        } catch { /* non-fatal */ }
        setImageBusy(false)
      }
    } else {
      // Slug on CREATE:
      //   dirty + non-empty → ship verbatim
      //   else              → omit (backend auto-derives via Slugify)
      const slugTrimmed = slug.trim()
      const createSlug: { slug?: string } = {}
      if (slugDirty && slugTrimmed !== '') {
        createSlug.slug = slugTrimmed
      }
      const newLink = await createLink.mutateAsync({
        url: trimmed,
        title: title.trim() || trimmed,
        description: description.trim() || null,
        tag_ids: tagIds,
        pinned,
        folder_id: folderId,
        ...createSlug,
      })
      if (pendingImage && newLink?.id) {
        setImageBusy(true)
        try {
          await uploadLinkImage(newLink.id, pendingImage)
          qc.invalidateQueries({ queryKey: ['links'] })
          qc.invalidateQueries({ queryKey: ['folders'] })
        } catch (e: unknown) {
          setImgUploadError(extractUploadErr(e, t('link_dialog.image_error_generic')))
          setImageBusy(false)
          return
        }
        setImageBusy(false)
      }
    }
    onClose()
  }

  const busy = createLink.isPending || updateLink.isPending || createTag.isPending || imageBusy
  const isEdit = !!link
  const hasImage = !imageRemoved && !!(pendingImagePreview || link?.og_image_url)
  const currentImageUrl = pendingImagePreview ?? (imageRemoved ? null : link?.og_image_url ?? null)

  const handleRemoveImage = () => {
    // Stage the deletion only — the actual DELETE fires in submit() so that
    // Cancel can still abort and Save is the single commit point.
    setPendingImage(null)
    setPendingImagePreview(null)
    setImageRemoved(true)
  }

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={isEdit ? t('link_dialog.edit_title') : t('link_dialog.kicker_create')}
    >
      <div className="fx-modal">
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{isEdit ? t('link_dialog.kicker_edit') : t('link_dialog.kicker_create')}</div>
            <h2 className="fx-modal-title">{isEdit ? t('link_dialog.edit_title') : t('link_dialog.create_title')}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body">
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.url_label')}</span>
              <div className="fx-input fx-input-url">
                <Icon d={I.link} size={15} />
                <input
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder={t('link_dialog.url_placeholder')}
                  autoFocus
                  aria-label="URL"
                />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.title_label')}</span>
              <div className="fx-input">
                <input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder={t('link_dialog.title_placeholder')}
                  aria-label="Title"
                />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.slug_label')}</span>
              <div className="fx-input">
                <span style={{ color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)', fontSize: 12, paddingRight: 4 }}>
                  /go/
                </span>
                <input
                  value={slug}
                  onChange={(e) => {
                    setSlug(e.target.value)
                    setSlugDirty(true)
                  }}
                  placeholder={slugifyClient(title) || 'jira-board'}
                  aria-label={t('link_dialog.slug_aria')}
                  pattern="[a-z0-9]+(-[a-z0-9]+)*"
                  style={{ fontFamily: 'var(--fx-mono)' }}
                />
                {slugDirty && (
                  <button
                    type="button"
                    className="fx-iconbtn"
                    onClick={() => {
                      setSlug(slugifyClient(title))
                      setSlugDirty(false)
                    }}
                    data-tooltip={t('link_dialog.slug_reset_tooltip')}
                    aria-label={t('link_dialog.slug_reset_tooltip')}
                  >
                    <Icon d={I.refresh} size={13} />
                  </button>
                )}
              </div>
              <span className="fx-field-hint">{t('link_dialog.slug_hint')}</span>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.description_label')}</span>
              <div className="fx-textarea-wrap">
                <textarea
                  className="fx-textarea"
                  value={description}
                  onChange={(e) => setDescription(e.target.value.slice(0, 1000))}
                  rows={3}
                  maxLength={1000}
                  aria-label="Description"
                />
                <span className={
                  'fx-textarea-count' +
                  (description.length >= 1000 ? ' fx-textarea-count-limit' :
                   description.length >= 900  ? ' fx-textarea-count-warn'  : '')
                }>
                  {description.length}/1000
                </span>
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.tags_label')}</span>
              <div className="fx-tagpicker">
                {selected.map((tag, i) => (
                  <TagChip
                    key={tag.id || `pending-${i}`}
                    tag={tag}
                    active
                    closable
                    onClick={tag._pending ? () => cycleColor(i) : undefined}
                    onClose={() => setSelected(selected.filter((_, j) => j !== i))}
                  />
                ))}
                <input
                  className="fx-tagpicker-input"
                  value={tagFilter}
                  onChange={(e) => { setTagFilter(e.target.value); setTagPage(0) }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && canCreateFromFilter) {
                      e.preventDefault()
                      queueInlineTag()
                    }
                  }}
                  placeholder={t('link_dialog.tags_search_placeholder')}
                  aria-label="tag filter"
                />
              </div>
              {selected.some((tag) => tag._pending) && (
                <div className="fx-tag-hint">
                  <Trans i18nKey="link_dialog.pending_tag_color_hint_html" components={{ strong: <strong /> }} />
                </div>
              )}
              {(filteredAvailable.length > 0 || canCreateFromFilter) && (() => {
                const PAGE = 7
                const totalPages = Math.ceil(filteredAvailable.length / PAGE)
                const pageTags = filteredAvailable.slice(tagPage * PAGE, (tagPage + 1) * PAGE)
                return (
                  <div style={{ marginTop: 10 }}>
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                      <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
                        {t('link_dialog.tags_registered_label')}
                      </span>
                      {totalPages > 1 && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                          <button
                            type="button"
                            className="fx-iconbtn"
                            disabled={tagPage === 0}
                            onClick={() => setTagPage((p) => p - 1)}
                            aria-label={t('link_dialog.tags_page_prev_aria')}
                            style={{ width: 22, height: 22 }}
                          >
                            <Icon d={I.chevronLeft} size={12} />
                          </button>
                          <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', minWidth: 32, textAlign: 'center' }}>
                            {tagPage + 1}/{totalPages}
                          </span>
                          <button
                            type="button"
                            className="fx-iconbtn"
                            disabled={tagPage >= totalPages - 1}
                            onClick={() => setTagPage((p) => p + 1)}
                            aria-label={t('link_dialog.tags_page_next_aria')}
                            style={{ width: 22, height: 22 }}
                          >
                            <Icon d={I.chevronRight} size={12} />
                          </button>
                        </div>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {pageTags.map((tag) => (
                        <TagChip
                          key={tag.id}
                          tag={tag}
                          onClick={() => {
                            setSelected([...selected, tag])
                            setTagFilter('')
                          }}
                        />
                      ))}
                      {canCreateFromFilter && (
                        <button
                          type="button"
                          className="fx-pillbtn"
                          onClick={queueInlineTag}
                          style={{ fontSize: 11 }}
                        >
                          <Icon d={I.plus} size={11} /> {t('link_dialog.tags_create_inline', { name: tagFilter.trim() })}
                        </button>
                      )}
                    </div>
                  </div>
                )
              })()}
            </label>

            <label className="fx-field">
              <span className="fx-field-label">{t('link_dialog.folder_label')}</span>
              <div className="fx-input">
                <select
                  className="fx-folder-select"
                  value={folderId == null ? '' : String(folderId)}
                  onChange={(e) =>
                    setFolderId(e.target.value === '' ? null : Number(e.target.value))
                  }
                  aria-label="folder"
                >
                  <option value="">{t('link_dialog.folder_none')}</option>
                  {folders.map((f) => (
                    <option key={f.id} value={String(f.id)}>
                      {f.name}
                    </option>
                  ))}
                </select>
              </div>
            </label>

            <label className="fx-toggle-row">
              <input
                type="checkbox"
                checked={pinned}
                onChange={(e) => setPinned(e.target.checked)}
                aria-label={t('link_dialog.pinned_aria')}
              />
              <span className="fx-toggle-track">
                <span className="fx-toggle-knob" />
              </span>
              <span className="fx-toggle-label">
                <Icon d={I.pin} size={12} /> {t('link_dialog.pinned_label')}
                <span className="fx-toggle-hint">{t('link_dialog.pinned_hint')}</span>
              </span>
            </label>
          </div>

          <aside className="fx-modal-side">
            {/* Status */}
            <div className="fx-modal-side-label">{t('link_dialog.status_label')}</div>
            <div className="fx-modal-side-meta">
              <div className="fx-modal-side-meta-row">
                <Icon d={I.globe} size={13} /> {hostOf(url) || '—'}
              </div>
              <div className="fx-modal-side-meta-row">
                <Icon d={I.flame} size={13} /> {t('link_dialog.clicks_count', { count: link?.click_count ?? 0 })}
              </div>
              {pinned && (
                <div className="fx-modal-side-meta-row" style={{ color: 'var(--fx-accent)' }}>
                  <Icon d={I.pin} size={13} /> {t('link_dialog.pinned_status')}
                </div>
              )}
            </div>

            {/* Preview / Upload */}
            <div className="fx-modal-side-preview" style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div className="fx-modal-side-label">{t('link_dialog.image_label')}</div>
                {url && (
                  <a href={url} target="_blank" rel="noopener noreferrer" className="fx-modal-side-open-link">
                    <Icon d={I.arrowR} size={11} /> {t('link_dialog.image_open_browser')}
                  </a>
                )}
              </div>

              {/* Current image */}
              {currentImageUrl && (
                <div className="fx-modal-side-ogwrap">
                  <img src={currentImageUrl} alt="preview" referrerPolicy="no-referrer" className="fx-modal-side-ogimg" />
                  {imageBusy && (
                    <div className="fx-modal-side-uploading" aria-live="polite">
                      <span className="fx-spinner" aria-hidden="true" />
                      <span>{t('link_dialog.image_uploading')}</span>
                    </div>
                  )}
                </div>
              )}

              {/* Upload zone */}
              <input
                ref={fileInputRef}
                type="file"
                accept="image/*"
                style={{ display: 'none' }}
                onChange={(e) => {
                  const file = e.target.files?.[0]
                  if (file) handleImageFile(file)
                  e.target.value = ''
                }}
              />
              <div
                className={'fx-img-upload-zone' + (isDragging ? ' fx-img-upload-zone-drag' : '') + (imageBusy ? ' fx-img-upload-zone-busy' : '')}
                onClick={() => !imageBusy && fileInputRef.current?.click()}
                onDragOver={(e) => { e.preventDefault(); if (!imageBusy) setIsDragging(true) }}
                onDragLeave={() => setIsDragging(false)}
                onDrop={(e) => {
                  e.preventDefault()
                  setIsDragging(false)
                  if (imageBusy) return
                  const file = e.dataTransfer.files?.[0]
                  if (file) handleImageFile(file)
                }}
              >
                {imageBusy ? (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                    <span className="fx-spinner" aria-hidden="true" /> {t('link_dialog.image_uploading')}
                  </span>
                ) : pendingImagePreview
                  ? t('link_dialog.image_selected_hint')
                  : t('link_dialog.image_drop_hint')}
              </div>

              {/* Status messages */}
              {pendingImagePreview && (
                <div style={{ fontSize: 11, color: 'var(--fx-accent)', display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Icon d={I.check} size={12} /> {t('link_dialog.image_saved_with_link')}
                </div>
              )}
              {imgUploadError && (
                <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Icon d={I.alert} size={12} /> {imgUploadError}
                </div>
              )}

              {/* Remove button */}
              {hasImage && (
                <button
                  type="button"
                  className="fx-confirm-btn"
                  style={{ justifyContent: 'center', color: 'var(--fx-danger)' }}
                  onClick={handleRemoveImage}
                >
                  <Icon d={I.trash} size={13} /> {t('link_dialog.image_remove')}
                </button>
              )}
            </div>
          </aside>
        </div>

        <footer className="fx-modal-foot">
          <button className="fx-confirm-btn" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!url.trim() || busy}
          >
            {imageBusy ? (
              <>
                <span className="fx-spinner" aria-hidden="true" /> {t('link_dialog.image_uploading')}
              </>
            ) : (
              <>
                {isEdit ? t('link_dialog.submit_save') : t('link_dialog.submit_create')}
                <Icon d={I.arrowR} size={14} stroke={2} />
              </>
            )}
          </button>
        </footer>
      </div>
    </div>
  )
}

function hostOf(u: string) {
  try {
    return new URL(u).hostname.replace(/^www\./, '')
  } catch {
    return ''
  }
}

// Mirror of the backend Slugify (internal/links/slug.go) — used to render
// the live "Auto: jira-board" placeholder under the slug field as the user
// types a title. Keep both in sync; the source of truth lives on the
// backend (the value posted is what gets persisted, not whatever this
// returns).
function slugifyClient(title: string): string {
  return title
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '') // strip combining marks
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80)
    .replace(/-+$/g, '')
}

function extractUploadErr(e: unknown, fallback: string): string {
  const obj = e as { response?: { data?: { error?: { message?: string } } }; message?: string }
  return obj?.response?.data?.error?.message ?? obj?.message ?? fallback
}
