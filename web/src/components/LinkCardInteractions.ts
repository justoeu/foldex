import { useCallback, useEffect, useState, type DragEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { mapCachedLinks } from '../api/links'
import { mapCachedLinkEntries } from '../api/entries'
import type { Link, MergeSource } from '../api/types'

const LINK_MIME = 'application/x-foldex-link'
const NOTE_MIME = 'application/x-foldex-note'

export function hasUnseenChange(link: Link): boolean {
  return !!link.last_change_detected_at &&
    (!link.change_seen_at || link.change_seen_at < link.last_change_detected_at)
}

export function acceptsCardDrag(types: readonly string[]): boolean {
  return types.includes(LINK_MIME) || types.includes(NOTE_MIME)
}

export function dropSourceForLink(
  dataTransfer: Pick<DataTransfer, 'getData'>,
  targetId: number,
): MergeSource | null {
  const linkRaw = dataTransfer.getData(LINK_MIME)
  const noteRaw = dataTransfer.getData(NOTE_MIME)
  const kind = linkRaw ? 'link' : 'note'
  const rawId = linkRaw || noteRaw
  const id = Number(rawId)
  if (!rawId || !id) return null
  if (kind === 'link' && id === targetId) return null
  return { kind, id }
}

export function useLinkCardInteractions({
  linkId,
  previewUrl,
  onMergeWith,
}: {
  linkId: number
  previewUrl: string | null | undefined
  onMergeWith?: (source: MergeSource, targetId: number) => void
}) {
  const queryClient = useQueryClient()
  const [previewErrored, setPreviewErrored] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const [dragging, setDragging] = useState(false)

  useEffect(() => {
    setPreviewErrored(false)
  }, [previewUrl])

  const onGo = useCallback(() => {
    const last_clicked_at = new Date().toISOString()
    const bump = (link: Link): Link => link.id === linkId
      ? { ...link, click_count: (link.click_count ?? 0) + 1, last_clicked_at }
      : link
    mapCachedLinks(queryClient, bump)
    mapCachedLinkEntries(queryClient, bump)
  }, [linkId, queryClient])

  const onDragStart = useCallback((event: DragEvent<HTMLElement>) => {
    event.dataTransfer.setData(LINK_MIME, String(linkId))
    event.dataTransfer.effectAllowed = 'move'
    setDragging(true)
  }, [linkId])
  const onDragEnd = useCallback(() => setDragging(false), [])
  const onDragOver = useCallback((event: DragEvent<HTMLElement>) => {
    if (!acceptsCardDrag(event.dataTransfer.types)) return
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])
  const onDragEnter = useCallback((event: DragEvent<HTMLElement>) => {
    if (acceptsCardDrag(event.dataTransfer.types)) setDragOver(true)
  }, [])
  const onDragLeave = useCallback((event: DragEvent<HTMLElement>) => {
    const next = event.relatedTarget as Node | null
    if (!next || !event.currentTarget.contains(next)) setDragOver(false)
  }, [])
  const onDrop = useCallback((event: DragEvent<HTMLElement>) => {
    setDragOver(false)
    const source = dropSourceForLink(event.dataTransfer, linkId)
    if (!source) return
    event.preventDefault()
    onMergeWith?.(source, linkId)
  }, [linkId, onMergeWith])

  return {
    previewErrored,
    dragging,
    dragOver,
    onPreviewError: () => setPreviewErrored(true),
    onGo,
    onDragStart,
    onDragEnd,
    onDragOver,
    onDragEnter,
    onDragLeave,
    onDrop,
  }
}
