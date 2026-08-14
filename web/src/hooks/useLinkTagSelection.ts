import { useEffect, useMemo, useState } from 'react'
import { useTags } from '../api/tags'
import { INLINE_PALETTE } from '../lib/inlinePalette'
import type { Link, Tag } from '../api/types'
import type { SelectedTag } from '../lib/linkDialogPayload'

const TAGS_PER_PAGE = 7

export function useLinkTagSelection(open: boolean, link: Link | null) {
  const { data: tags = [] } = useTags()
  const [selected, setSelected] = useState<SelectedTag[]>([])
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(0)

  useEffect(() => {
    if (!open) return
    setSelected(link?.tags ?? [])
    setFilter('')
    setPage(0)
  }, [open, link])

  const available = useMemo(
    () => tags.filter((tag) => !selected.some((chosen) => chosen.id === tag.id)),
    [selected, tags],
  )
  const filtered = useMemo(() => {
    const normalized = filter.toLowerCase()
    return filter
      ? available.filter((tag) => tag.name.toLowerCase().includes(normalized))
      : available
  }, [available, filter])
  const totalPages = Math.ceil(filtered.length / TAGS_PER_PAGE)
  const pageTags = filtered.slice(page * TAGS_PER_PAGE, (page + 1) * TAGS_PER_PAGE)
  const name = filter.trim()
  const normalizedName = name.toLowerCase()
  const canCreate = !!name
    && !tags.some((tag) => tag.name.toLowerCase() === normalizedName)
    && !selected.some((tag) => tag.name.toLowerCase() === normalizedName)

  useEffect(() => {
    const lastPage = Math.max(0, totalPages - 1)
    if (page > lastPage) setPage(lastPage)
  }, [page, totalPages])

  const setSearch = (value: string) => {
    setFilter(value)
    setPage(0)
  }

  const queue = () => {
    if (!name) return
    setSelected((current) => [
      ...current,
      { id: 0, name, color: INLINE_PALETTE[0], icon: null, _pending: true },
    ])
    setFilter('')
  }

  const cycleColor = (index: number) => {
    setSelected((current) => current.map((tag, tagIndex) => {
      if (tagIndex !== index || !tag._pending) return tag
      const paletteIndex = INLINE_PALETTE.indexOf(tag.color)
      return { ...tag, color: INLINE_PALETTE[(paletteIndex + 1) % INLINE_PALETTE.length] }
    }))
  }

  const add = (tag: Tag) => {
    setSelected((current) => [...current, tag])
    setFilter('')
  }

  const remove = (index: number) => {
    setSelected((current) => current.filter((_, tagIndex) => tagIndex !== index))
  }

  return {
    selected,
    filter,
    page,
    filtered,
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
