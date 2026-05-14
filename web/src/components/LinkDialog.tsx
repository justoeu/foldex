import { useEffect, useMemo, useRef, useState } from 'react'
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
  const { data: tags = [] } = useTags()
  const { data: folders = [] } = useFolders()
  const createTag = useCreateTag()
  const createLink = useCreateLink()
  const updateLink = useUpdateLink()

  const [url, setUrl] = useState('')
  const [title, setTitle] = useState('')
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

  useEscape(onClose, open)

  const available = useMemo(
    () => tags.filter((t) => !selected.some((s) => s.id === t.id)),
    [tags, selected],
  )
  const filteredAvailable = useMemo(
    () =>
      tagFilter
        ? available.filter((t) => t.name.toLowerCase().includes(tagFilter.toLowerCase()))
        : available,
    [available, tagFilter],
  )
  useEffect(() => {
    const lastPage = Math.max(0, Math.ceil(filteredAvailable.length / 7) - 1)
    if (tagPage > lastPage) setTagPage(lastPage)
  }, [filteredAvailable.length, tagPage])
  const canCreateFromFilter =
    tagFilter.trim().length > 0 &&
    !tags.some((t) => t.name.toLowerCase() === tagFilter.trim().toLowerCase()) &&
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
      selected.map((t, i) => {
        if (i !== idx || !t._pending) return t
        const cur = INLINE_PALETTE.indexOf(t.color)
        const next = INLINE_PALETTE[(cur + 1) % INLINE_PALETTE.length]
        return { ...t, color: next }
      }),
    )
  }

  const handleImageFile = (file: File) => {
    if (!file.type.startsWith('image/')) {
      setImgUploadError('Arquivo deve ser uma imagem.')
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
    for (const t of selected) {
      if (t.id) {
        tagIds.push(t.id)
      } else {
        const created = await createTag.mutateAsync({ name: t.name, color: t.color })
        tagIds.push(created.id)
      }
    }

    if (link) {
      await updateLink.mutateAsync({
        id: link.id,
        body: {
          url: trimmed,
          title: title.trim() || trimmed,
          description: description.trim() || null,
          tag_ids: tagIds,
          pinned,
          folder_id: folderId,
        },
      })
      if (pendingImage) {
        setImageBusy(true)
        try {
          await uploadLinkImage(link.id, pendingImage)
          qc.invalidateQueries({ queryKey: ['links'] })
          qc.invalidateQueries({ queryKey: ['folders'] })
        } catch (e: unknown) {
          setImgUploadError(extractUploadErr(e))
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
      const newLink = await createLink.mutateAsync({
        url: trimmed,
        title: title.trim() || trimmed,
        description: description.trim() || null,
        tag_ids: tagIds,
        pinned,
        folder_id: folderId,
      })
      if (pendingImage && newLink?.id) {
        setImageBusy(true)
        try {
          await uploadLinkImage(newLink.id, pendingImage)
          qc.invalidateQueries({ queryKey: ['links'] })
          qc.invalidateQueries({ queryKey: ['folders'] })
        } catch (e: unknown) {
          setImgUploadError(extractUploadErr(e))
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
      aria-label={isEdit ? 'Edit link' : 'New link'}
    >
      <div className="fx-modal">
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{isEdit ? '✎ Editar' : '+ Novo link'}</div>
            <h2 className="fx-modal-title">{isEdit ? 'Editar link' : 'Captura visual'}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label="close">
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body">
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">URL</span>
              <div className="fx-input fx-input-url">
                <Icon d={I.link} size={15} />
                <input
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://"
                  autoFocus
                  aria-label="URL"
                />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">Título</span>
              <div className="fx-input">
                <input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder="Derivado da URL se vazio"
                  aria-label="Title"
                />
              </div>
            </label>

            <label className="fx-field">
              <span className="fx-field-label">Descrição</span>
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
              <span className="fx-field-label">Tags</span>
              <div className="fx-tagpicker">
                {selected.map((t, i) => (
                  <TagChip
                    key={t.id || `pending-${i}`}
                    tag={t}
                    active
                    closable
                    onClick={t._pending ? () => cycleColor(i) : undefined}
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
                  placeholder="+ adicionar tag…"
                  aria-label="tag filter"
                />
              </div>
              {selected.some((t) => t._pending) && (
                <div className="fx-tag-hint">
                  Clique no <strong>círculo colorido</strong> de uma tag nova pra trocar a cor.
                  Tags só são criadas quando você salva o link.
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
                        Tags cadastradas
                      </span>
                      {totalPages > 1 && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                          <button
                            type="button"
                            className="fx-iconbtn"
                            disabled={tagPage === 0}
                            onClick={() => setTagPage((p) => p - 1)}
                            aria-label="página anterior"
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
                            aria-label="próxima página"
                            style={{ width: 22, height: 22 }}
                          >
                            <Icon d={I.chevronRight} size={12} />
                          </button>
                        </div>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {pageTags.map((t) => (
                        <TagChip
                          key={t.id}
                          tag={t}
                          onClick={() => {
                            setSelected([...selected, t])
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
                          <Icon d={I.plus} size={11} /> criar "{tagFilter.trim()}"
                        </button>
                      )}
                    </div>
                  </div>
                )
              })()}
            </label>

            <label className="fx-field">
              <span className="fx-field-label">Pasta</span>
              <div className="fx-input">
                <select
                  className="fx-folder-select"
                  value={folderId == null ? '' : String(folderId)}
                  onChange={(e) =>
                    setFolderId(e.target.value === '' ? null : Number(e.target.value))
                  }
                  aria-label="folder"
                >
                  <option value="">Nenhuma pasta</option>
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
                aria-label="pin to top"
              />
              <span className="fx-toggle-track">
                <span className="fx-toggle-knob" />
              </span>
              <span className="fx-toggle-label">
                <Icon d={I.pin} size={12} /> Fixar no topo
                <span className="fx-toggle-hint">Pinados aparecem antes dos demais</span>
              </span>
            </label>
          </div>

          <aside className="fx-modal-side">
            {/* Status */}
            <div className="fx-modal-side-label">Status</div>
            <div className="fx-modal-side-meta">
              <div className="fx-modal-side-meta-row">
                <Icon d={I.globe} size={13} /> {hostOf(url) || '—'}
              </div>
              <div className="fx-modal-side-meta-row">
                <Icon d={I.flame} size={13} /> {link?.click_count ?? 0} cliques
              </div>
              {pinned && (
                <div className="fx-modal-side-meta-row" style={{ color: 'var(--fx-accent)' }}>
                  <Icon d={I.pin} size={13} /> Fixado
                </div>
              )}
            </div>

            {/* Preview / Upload */}
            <div className="fx-modal-side-preview" style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div className="fx-modal-side-label">Imagem de prévia</div>
                {url && (
                  <a href={url} target="_blank" rel="noopener noreferrer" className="fx-modal-side-open-link">
                    <Icon d={I.arrowR} size={11} /> Abrir no browser
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
                      <span>Enviando imagem…</span>
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
                    <span className="fx-spinner" aria-hidden="true" /> Enviando imagem…
                  </span>
                ) : pendingImagePreview
                  ? '✅ Imagem selecionada — será salva ao clicar em Salvar'
                  : '📎 Arraste ou clique para adicionar imagem'}
              </div>

              {/* Status messages */}
              {pendingImagePreview && (
                <div style={{ fontSize: 11, color: 'var(--fx-accent)', display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Icon d={I.check} size={12} /> Será salva junto com o link
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
                  <Icon d={I.trash} size={13} /> Remover imagem
                </button>
              )}
            </div>
          </aside>
        </div>

        <footer className="fx-modal-foot">
          <button className="fx-confirm-btn" onClick={onClose}>
            Cancelar
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!url.trim() || busy}
          >
            {imageBusy ? (
              <>
                <span className="fx-spinner" aria-hidden="true" /> Enviando imagem…
              </>
            ) : (
              <>
                {isEdit ? 'Salvar alterações' : 'Salvar link'}
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

function extractUploadErr(e: unknown): string {
  const obj = e as { response?: { data?: { error?: { message?: string } } }; message?: string }
  return obj?.response?.data?.error?.message ?? obj?.message ?? 'falha ao enviar imagem'
}
