import { useEffect, useMemo, useState, type Dispatch, type SetStateAction } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import type { Tag } from '../api/types'
import { INLINE_PALETTE } from '../lib/inlinePalette'
import { Icon, I } from './icons'
import { TagChip } from './TagChip'
import type { SelectedNoteTag } from './NoteDialogPayload'

const PAGE_SIZE = 7

type Props = {
  tags: Tag[]
  selected: SelectedNoteTag[]
  setSelected: Dispatch<SetStateAction<SelectedNoteTag[]>>
}

type RegisteredTagsProps = {
  tags: Tag[]
  page: number
  setPage: Dispatch<SetStateAction<number>>
  canCreate: boolean
  filter: string
  addTag: (tag: Tag) => void
  queueTag: () => void
}

function RegisteredTags({ tags, page, setPage, canCreate, filter, addTag, queueTag }: RegisteredTagsProps) {
  const { t } = useTranslation()
  const totalPages = Math.ceil(tags.length / PAGE_SIZE)
  const pageTags = tags.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)

  return (
    <div style={{ marginTop: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
        <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
          {t('link_dialog.tags_registered_label')}
        </span>
        {totalPages > 1 && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
            <button type="button" className="fx-iconbtn" disabled={page === 0} onClick={() => setPage((value) => value - 1)} aria-label={t('link_dialog.tags_page_prev_aria')} style={{ width: 22, height: 22 }}>
              <Icon d={I.chevronLeft} size={12} />
            </button>
            <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', minWidth: 32, textAlign: 'center' }}>
              {page + 1}/{totalPages}
            </span>
            <button type="button" className="fx-iconbtn" disabled={page >= totalPages - 1} onClick={() => setPage((value) => value + 1)} aria-label={t('link_dialog.tags_page_next_aria')} style={{ width: 22, height: 22 }}>
              <Icon d={I.chevronRight} size={12} />
            </button>
          </div>
        )}
      </div>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
        {pageTags.map((tag) => <TagChip key={tag.id} tag={tag} onClick={() => addTag(tag)} />)}
        {canCreate && (
          <button type="button" className="fx-pillbtn" onClick={queueTag} style={{ fontSize: 11 }}>
            <Icon d={I.plus} size={11} /> {t('link_dialog.tags_create_inline', { name: filter.trim() })}
          </button>
        )}
      </div>
    </div>
  )
}

export function NoteDialogTagPicker({ tags, selected, setSelected }: Props) {
  const { t } = useTranslation()
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(0)
  const available = useMemo(
    () => tags.filter((tag) => !selected.some((item) => item.id === tag.id)),
    [tags, selected],
  )
  const filtered = useMemo(() => {
    const search = filter.trim().toLowerCase()
    return search ? available.filter((tag) => tag.name.toLowerCase().includes(search)) : available
  }, [available, filter])
  const normalizedFilter = filter.trim().toLowerCase()
  const canCreate = Boolean(normalizedFilter)
    && !tags.some((tag) => tag.name.toLowerCase() === normalizedFilter)
    && !selected.some((tag) => tag.name.toLowerCase() === normalizedFilter)

  useEffect(() => {
    const lastPage = Math.max(0, Math.ceil(filtered.length / PAGE_SIZE) - 1)
    if (page > lastPage) setPage(lastPage)
  }, [filtered.length, page])

  const queueTag = () => {
    const name = filter.trim()
    if (!name) return
    setSelected((current) => [...current, { id: 0, name, color: INLINE_PALETTE[0], icon: null, _pending: true }])
    setFilter('')
  }
  const cycleColor = (index: number) => {
    setSelected((current) => current.map((tag, position) => {
      if (position !== index || !tag._pending) return tag
      const paletteIndex = INLINE_PALETTE.indexOf(tag.color)
      return { ...tag, color: INLINE_PALETTE[(paletteIndex + 1) % INLINE_PALETTE.length] }
    }))
  }
  const addTag = (tag: Tag) => {
    setSelected((current) => [...current, tag])
    setFilter('')
  }

  return (
    <label className="fx-field">
      <span className="fx-field-label">{t('note_dialog.tags_label')}</span>
      <div className="fx-tagpicker">
        {selected.map((tag, index) => (
          <TagChip
            key={tag.id || `pending-${index}`}
            tag={tag}
            active
            closable
            onClick={tag._pending ? () => cycleColor(index) : undefined}
            onClose={() => setSelected((current) => current.filter((_, position) => position !== index))}
          />
        ))}
        <input
          className="fx-tagpicker-input"
          value={filter}
          onChange={(event) => { setFilter(event.target.value); setPage(0) }}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || !canCreate) return
            event.preventDefault()
            queueTag()
          }}
          placeholder={t('note_dialog.tags_search_placeholder')}
          aria-label={t('common.tag_filter_aria')}
        />
      </div>
      {selected.some((tag) => tag._pending) && (
        <div className="fx-tag-hint">
          <Trans i18nKey="note_dialog.pending_tag_color_hint_html" components={{ strong: <strong /> }} />
        </div>
      )}
      {(filtered.length > 0 || canCreate) && (
        <RegisteredTags tags={filtered} page={page} setPage={setPage} canCreate={canCreate} filter={filter} addTag={addTag} queueTag={queueTag} />
      )}
    </label>
  )
}
