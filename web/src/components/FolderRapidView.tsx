import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { primaryColor } from '../lib/tagColor'
import { safeImageUrl } from '../lib/url'
import type { Folder } from '../api/types'

const MAX_ITEMS = 10
const SHOW_DELAY_MS = 220

type Props = {
  folder: Folder
  children: ReactNode
  // When false, behaves as a passthrough wrapper (no popover). Lets the
  // FolderCard mount the same JSX subtree regardless of compact mode.
  enabled: boolean
}

export function FolderRapidView({ folder, children, enabled }: Props) {
  const { t } = useTranslation()
  const wrapRef = useRef<HTMLSpanElement>(null)
  const [open, setOpen] = useState(false)
  const [rect, setRect] = useState<DOMRect | null>(null)
  const showTimer = useRef<number | null>(null)

  // Without the cleanup, a pending setTimeout can fire after the wrapper is
  // gone (or after `enabled` flipped) and call setState on an unmounted node:
  // React 19 warns, and in production the stale popover briefly appears on
  // the next render of any FolderCard.
  useEffect(() => {
    if (!enabled) {
      setOpen(false)
      setRect(null)
    }
    return () => {
      if (showTimer.current !== null) {
        window.clearTimeout(showTimer.current)
        showTimer.current = null
      }
    }
  }, [enabled])

  useEffect(() => {
    if (!open) return
    const onScroll = () => {
      if (wrapRef.current) setRect(wrapRef.current.getBoundingClientRect())
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onScroll)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onScroll)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const total = folder.preview_folders.length + folder.preview_links.length
  const hasContent = total > 0

  const scheduleOpen = () => {
    if (!enabled || !hasContent) return
    if (showTimer.current !== null) window.clearTimeout(showTimer.current)
    showTimer.current = window.setTimeout(() => {
      if (wrapRef.current) {
        setRect(wrapRef.current.getBoundingClientRect())
        setOpen(true)
      }
    }, SHOW_DELAY_MS)
  }
  const cancelOpen = () => {
    if (showTimer.current !== null) {
      window.clearTimeout(showTimer.current)
      showTimer.current = null
    }
    setOpen(false)
  }

  return (
    <span
      ref={wrapRef}
      className="fx-rapidview-trigger"
      onMouseEnter={scheduleOpen}
      onMouseLeave={cancelOpen}
      onFocus={scheduleOpen}
      onBlur={cancelOpen}
    >
      {children}
      {enabled && open && rect && hasContent && (
        <RapidViewPopover folder={folder} rect={rect} t={t} />
      )}
    </span>
  )
}

function RapidViewPopover({
  folder,
  rect,
  t,
}: {
  folder: Folder
  rect: DOMRect
  t: ReturnType<typeof useTranslation>['t']
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number; ready: boolean }>({
    left: 0,
    top: 0,
    ready: false,
  })

  const rows: Array<
    | { kind: 'folder'; id: number; name: string; color: string }
    | { kind: 'link'; id: number; title: string; favSrc: string | undefined }
  > = []
  for (const f of folder.preview_folders) {
    if (rows.length >= MAX_ITEMS) break
    rows.push({ kind: 'folder', id: f.id, name: f.name, color: f.color })
  }
  for (const l of folder.preview_links) {
    if (rows.length >= MAX_ITEMS) break
    rows.push({ kind: 'link', id: l.id, title: l.title, favSrc: safeImageUrl(l.favicon_url) })
  }
  const total = folder.link_count + folder.folder_count
  const moreCount = Math.max(0, total - rows.length)

  useLayoutEffect(() => {
    if (!ref.current) return
    const tip = ref.current.getBoundingClientRect()
    const m = 8
    const vpW = window.innerWidth
    const vpH = window.innerHeight

    // Prefer below the anchor, like the tooltip default. Flip above when
    // there's no room below.
    let left = rect.left + rect.width / 2 - tip.width / 2
    let top = rect.bottom + m
    if (top + tip.height > vpH - 6) top = rect.top - tip.height - m

    const edge = 6
    left = Math.max(edge, Math.min(left, vpW - tip.width - edge))
    top = Math.max(edge, Math.min(top, vpH - tip.height - edge))
    setPos({ left, top, ready: true })
  }, [rect])

  return createPortal(
    <div
      ref={ref}
      className="fx-rapidview"
      role="tooltip"
      style={{
        position: 'fixed',
        left: pos.left,
        top: pos.top,
        opacity: pos.ready ? 1 : 0,
      }}
    >
      <div className="fx-rapidview-head">
        <Icon d={I.folder} size={13} />
        <span className="fx-rapidview-head-name">{folder.name}</span>
      </div>
      <ul className="fx-rapidview-list">
        {rows.map((r) =>
          r.kind === 'folder' ? (
            <li key={`f-${r.id}`} className="fx-rapidview-item">
              <span className="fx-rapidview-icon" style={{ color: primaryColor(r.color) }}>
                <Icon d={I.folder} size={12} />
              </span>
              <span className="fx-rapidview-label">{r.name}</span>
            </li>
          ) : (
            <li key={`l-${r.id}`} className="fx-rapidview-item">
              <span className="fx-rapidview-icon">
                {r.favSrc ? (
                  <img
                    src={r.favSrc}
                    alt=""
                    referrerPolicy="no-referrer"
                    className="fx-rapidview-favicon"
                  />
                ) : (
                  <Icon d={I.link} size={11} />
                )}
              </span>
              <span className="fx-rapidview-label">{r.title}</span>
            </li>
          ),
        )}
      </ul>
      {moreCount > 0 && (
        <div className="fx-rapidview-more">
          {t('folder_card.rapidview_more', { count: moreCount })}
        </div>
      )}
    </div>,
    document.body,
  )
}
