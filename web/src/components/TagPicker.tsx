import { useEffect, useMemo, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { useTags } from '../api/tags'
import type { Tag } from '../api/types'
import type { SelectedTag } from '../lib/dialogTags'
import { INLINE_PALETTE } from '../lib/inlinePalette'
import { suggestColor } from '../lib/suggestColor'
import { Icon, I } from './icons'
import { TagChip } from './TagChip'

const PAGE_SIZE = 7

export function useTagPicker(open: boolean, initialSelected?: SelectedTag[]) {
  const { data: tags = [] } = useTags()
  const [selected, setSelected] = useState<SelectedTag[]>([])
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(0)

  useEffect(() => {
    if (!open) return
    setSelected(initialSelected ?? [])
    setFilter('')
    setPage(0)
  }, [open, initialSelected])

  const available = useMemo(
    () => tags.filter((tag) => !selected.some((item) => item.id === tag.id)),
    [tags, selected],
  )
  const filtered = useMemo(() => {
    const search = filter.trim().toLowerCase()
    return search ? available.filter((tag) => tag.name.toLowerCase().includes(search)) : available
  }, [available, filter])
  const totalPages = Math.ceil(filtered.length / PAGE_SIZE)
  const pageTags = filtered.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE)
  const normalizedFilter = filter.trim().toLowerCase()
  const canCreate = Boolean(normalizedFilter)
    && !tags.some((tag) => tag.name.toLowerCase() === normalizedFilter)
    && !selected.some((tag) => tag.name.toLowerCase() === normalizedFilter)

  useEffect(() => {
    const lastPage = Math.max(0, totalPages - 1)
    if (page > lastPage) setPage(lastPage)
  }, [page, totalPages])

  const setSearch = (value: string) => {
    setFilter(value)
    setPage(0)
  }
  const queue = () => {
    const name = filter.trim()
    if (!name) return
    setSelected((current) => [...current, {
      id: 0,
      name,
      // Every pending chip used to open on the same indigo, so adding three
      // tags in a row produced three identical chips and the colour stopped
      // carrying information. The colours already spoken for include the ones
      // chosen in THIS dialog, not just the saved tags — otherwise two chips
      // added back to back could still collide.
      color: suggestColor([
        ...tags.map((x) => x.color),
        ...current.map((x) => x.color),
      ].filter(Boolean)),
      icon: null,
      _pending: true,
    }])
    setFilter('')
    setPage(0)
  }
  const cycleColor = (index: number) => {
    setSelected((current) => current.map((tag, position) => {
      if (position !== index || !tag._pending) return tag
      const paletteIndex = INLINE_PALETTE.indexOf(tag.color)
      return { ...tag, color: INLINE_PALETTE[(paletteIndex + 1) % INLINE_PALETTE.length] }
    }))
  }
  const add = (tag: Tag) => {
    setSelected((current) => [...current, tag])
    setFilter('')
    setPage(0)
  }
  const remove = (index: number) => {
    setSelected((current) => current.filter((_, position) => position !== index))
  }

  return {
    selected,
    setSelected,
    filter,
    page,
    pageTags,
    totalPages,
    canCreate,
    setPage,
    setSearch,
    queue,
    cycleColor,
    add,
    remove,
  }
}

export type TagPickerController = ReturnType<typeof useTagPicker>

export function TagPicker({
  picker,
  i18nPrefix,
}: {
  picker: TagPickerController
  i18nPrefix: 'link_dialog' | 'note_dialog'
}) {
  const { t } = useTranslation()
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t(`${i18nPrefix}.tags_label`)}</span>
      <div className="fx-tagpicker">
        {picker.selected.map((tag, index) => (
          <TagChip
            key={tag.id || `pending-${index}`}
            tag={tag}
            active
            closable
            onClick={tag._pending ? () => picker.cycleColor(index) : undefined}
            onClose={() => picker.remove(index)}
          />
        ))}
        <input
          className="fx-tagpicker-input"
          value={picker.filter}
          onChange={(event) => picker.setSearch(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || !picker.canCreate) return
            event.preventDefault()
            picker.queue()
          }}
          placeholder={t(`${i18nPrefix}.tags_search_placeholder`)}
          aria-label={t('common.tag_filter_aria')}
        />
      </div>
      {picker.selected.some((tag) => tag._pending) && (
        <div className="fx-tag-hint">
          <Trans i18nKey={`${i18nPrefix}.pending_tag_color_hint_html`} components={{ strong: <strong /> }} />
        </div>
      )}
      {(picker.pageTags.length > 0 || picker.canCreate) && (
        <div style={{ marginTop: 10 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
            <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10.5, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--fx-ink-4)' }}>
              {t('tag_picker.registered_label')}
            </span>
            {picker.totalPages > 1 && (
              <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                <button type="button" className="fx-iconbtn" disabled={picker.page === 0} onClick={() => picker.setPage((page) => page - 1)} aria-label={t('tag_picker.page_prev_aria')} style={{ width: 22, height: 22 }}>
                  <Icon d={I.chevronLeft} size={12} />
                </button>
                <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)', minWidth: 32, textAlign: 'center' }}>
                  {picker.page + 1}/{picker.totalPages}
                </span>
                <button type="button" className="fx-iconbtn" disabled={picker.page >= picker.totalPages - 1} onClick={() => picker.setPage((page) => page + 1)} aria-label={t('tag_picker.page_next_aria')} style={{ width: 22, height: 22 }}>
                  <Icon d={I.chevronRight} size={12} />
                </button>
              </div>
            )}
          </div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {picker.pageTags.map((tag) => <TagChip key={tag.id} tag={tag} onClick={() => picker.add(tag)} />)}
            {picker.canCreate && (
              <button type="button" className="fx-pillbtn" onClick={picker.queue} style={{ fontSize: 11 }}>
                <Icon d={I.plus} size={11} /> {t('tag_picker.create_inline', { name: picker.filter.trim() })}
              </button>
            )}
          </div>
        </div>
      )}
    </label>
  )
}
