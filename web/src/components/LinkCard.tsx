import { memo, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import {
  goHref,
  useDeleteLink,
  useMarkChangeSeen,
  usePinLink,
  useRefreshPreview,
} from '../api/links'
import { safeImageUrl } from '../lib/url'
import { relativeTime } from '../lib/time'
import type { Link } from '../api/types'

type Props = {
  link: Link
  onEdit: (l: Link) => void
  density?: 'normal' | 'short' | 'medium' | 'tall'
  // Drag-and-drop: card is the source of `dnd:link/<id>` and accepts a drop
  // FROM another link card (which triggers the link↔link merge — see App.tsx).
  onMergeWith?: (sourceId: number, targetId: number) => void
}

// Decide card height purely from how much content we have. Tall when a real
// og:image was fetched AND known to load; medium when there's a description;
// short otherwise. The `imageOk` argument lets the card collapse to the
// no-image variant when the og:image URL fails at runtime instead of leaving
// a broken-image icon in the preview area.
function densityFor(link: Link, imageOk: boolean): 'tall' | 'medium' | 'short' {
  if (link.og_image_url && imageOk) return 'tall'
  if (link.description) return 'medium'
  return 'short'
}

// memo guards re-render storms in dense grids (200+ cards). LinkCard is a
// pure function of (link, onEdit, onMergeWith) — the callbacks are stable
// across App.tsx renders (lifted to module scope or wrapped in useCallback
// at the container) so the default shallow compare is correct.
export const LinkCard = memo(LinkCardImpl)
LinkCard.displayName = 'LinkCard'

function LinkCardImpl({ link, onEdit, onMergeWith }: Props) {
  const { t } = useTranslation()
  const del = useDeleteLink()
  const refresh = useRefreshPreview()
  const pin = usePinLink()
  const markSeen = useMarkChangeSeen()
  const confirm = useConfirm()
  const qc = useQueryClient()
  const [previewErrored, setPreviewErrored] = useState(false)
  const previewSrc = safeImageUrl(link.og_image_url)
  const showPreview = !!previewSrc && !previewErrored
  const density = densityFor(link, showPreview)
  // Optimistic — see usePinLink. Badge flips immediately; server-side reorder
  // catches up on onSettled invalidation.
  const togglePin = () => pin.mutate({ id: link.id, pinned: !link.pinned })

  // "Unseen update" badge shows when:
  //   - the worker has ever recorded a change for this link, AND
  //   - the user hasn't acknowledged it (either change_seen_at is unset, or
  //     it's older than the most recent change).
  const hasUnseenChange =
    !!link.last_change_detected_at &&
    (!link.change_seen_at || link.change_seen_at < link.last_change_detected_at)
  const [dragOver, setDragOver] = useState(false)
  const [dragging, setDragging] = useState(false)

  // Reset the preview-error flag when the URL changes (e.g. preview worker
  // re-runs and stamps a new og:image_url, or the user uploads an image).
  useEffect(() => {
    setPreviewErrored(false)
  }, [link.og_image_url])

  // Optimistic bump: patch click_count + last_clicked_at in every cached
  // links list immediately. The old code waited 800 ms and then invalidated
  // every ['links'] query (full refetch of every visible page) — wasteful
  // when the user clicks a card, and the badge would lag the click anyway.
  // The /go/:id redirect handler is the source of truth; the bump here is a
  // hint that's reconciled the next time the user navigates / refetches.
  const onGo = useCallback(() => {
    const nowISO = new Date().toISOString()
    qc.setQueriesData<Link[] | undefined>({ queryKey: ['links'] }, (old) => {
      if (!old) return old
      return old.map((l) =>
        l.id === link.id
          ? { ...l, click_count: (l.click_count ?? 0) + 1, last_clicked_at: nowISO }
          : l,
      )
    })
  }, [qc, link.id])

  const onDelete = async () => {
    const ok = await confirm({
      title: t('link_card.delete_confirm_title', { title: link.title }),
      message: t('link_card.delete_confirm_body', { url: link.url }),
      confirmLabel: t('link_card.delete_confirm_action'),
      destructive: true,
    })
    if (ok) del.mutate(link.id)
  }

  return (
    <article
      className={
        'fx-card fx-card-' + density +
        (link.pinned ? ' fx-card-pinned' : '') +
        (hasUnseenChange ? ' fx-card-update-alert' : '') +
        (dragging ? ' fx-card-dragging' : '') +
        (dragOver ? ' fx-card-drop-over' : '')
      }
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData('application/x-foldex-link', String(link.id))
        e.dataTransfer.effectAllowed = 'move'
        setDragging(true)
      }}
      onDragEnd={() => setDragging(false)}
      onDragOver={(e) => {
        // Accept only OTHER link cards. Folder targets handle their own drop.
        const raw = e.dataTransfer.types
        if (!raw.includes('application/x-foldex-link')) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDragEnter={(e) => {
        if (e.dataTransfer.types.includes('application/x-foldex-link')) setDragOver(true)
      }}
      onDragLeave={(e) => {
        // Only clear when the leave is to outside the card (not a child).
        const next = e.relatedTarget as Node | null
        if (!next || !(e.currentTarget as Node).contains(next)) setDragOver(false)
      }}
      onDrop={(e) => {
        setDragOver(false)
        const raw = e.dataTransfer.getData('application/x-foldex-link')
        const sourceId = Number(raw)
        if (!sourceId || sourceId === link.id) return
        e.preventDefault()
        onMergeWith?.(sourceId, link.id)
      }}
    >
      <button
        className={'fx-card-pin-badge' + (link.pinned ? '' : ' fx-card-pin-off')}
        onClick={(e) => {
          e.stopPropagation()
          togglePin()
        }}
        aria-label={link.pinned ? t('link_card.unpin') : t('link_card.pin')}
        data-tooltip={link.pinned ? t('link_card.unpin_tooltip') : t('link_card.pin_top_tooltip')}
        data-tooltip-side="left"
      >
        <Icon d={I.pin} size={13} stroke={2} />
      </button>

      {hasUnseenChange && (
        <button
          className="fx-card-update-badge"
          onClick={(e) => {
            e.stopPropagation()
            markSeen.mutate(link.id)
          }}
          aria-label={t('link_card.mark_seen_aria')}
          data-tooltip={t('link_card.update_detected_tooltip', {
            when: relativeTime(link.last_change_detected_at!, t),
          })}
          data-tooltip-side="left"
        >
          <Icon d={I.bell} size={13} stroke={2} />
        </button>
      )}

      {showPreview && (
        <a className="fx-preview fx-preview-img" href={goHref(link)} target="_blank" rel="noopener noreferrer" onClick={onGo}>
          <img
            src={previewSrc}
            alt=""
            referrerPolicy="no-referrer"
            loading="lazy"
            decoding="async"
            onError={() => setPreviewErrored(true)}
            style={{ width: '100%', height: '100%', objectFit: 'scale-down', display: 'block' }}
          />
        </a>
      )}
      <div className="fx-card-body">
        <header className="fx-card-head">
          <Favicon link={link} size={showPreview ? 28 : 36} />
          <div className="fx-card-head-text">
            <h3 className="fx-card-title">
              <a href={goHref(link)} target="_blank" rel="noopener noreferrer" className="fx-card-title-link" onClick={onGo}>
                {link.title}
              </a>
            </h3>
            <div className="fx-card-host">{hostOf(link.url)}</div>
          </div>
        </header>

        {link.description && (
          <p className="fx-card-desc">{truncateDesc(link.description)}</p>
        )}

        {link.tags.length > 0 && (
          <div className="fx-card-tags">
            {link.tags.map((tag) => (
              <TagChip key={tag.id} tag={tag} />
            ))}
          </div>
        )}

        <footer className="fx-card-foot">
          <div className="fx-card-meta">
            <span className="fx-meta-stat" data-tooltip={t('link_card.clicks_tooltip')} aria-label={t('link_card.clicks_tooltip')}>
              <Icon d={I.flame} size={13} /> {link.click_count}
            </span>
            <span className="fx-meta-sep" />
            <span className="fx-meta-stat" data-tooltip={t('link_card.last_click_tooltip')} aria-label={t('link_card.last_click_tooltip')}>
              <Icon d={I.clock} size={13} /> {lastClick(link, t)}
            </span>
            {link.preview_status === 'failed' && !link.og_image_url && (
              <>
                <span className="fx-meta-sep" />
                <span className="fx-meta-warn">
                  <Icon d={I.alert} size={13} /> {t('link_card.preview_failed')}
                </span>
              </>
            )}
            {link.preview_status === 'pending' && (
              <>
                <span className="fx-meta-sep" />
                <span className="fx-meta-stat" style={{ color: 'var(--fx-warn)' }}>
                  <Icon d={I.clock} size={13} /> {t('link_card.capturing')}
                </span>
              </>
            )}
            {/* Gray-toned because the amber halo `.fx-card-update-alert`
                owns the "you have an unseen update" signal. */}
            {link.check_interval && (
              <>
                <span className="fx-meta-sep" />
                <span
                  className="fx-meta-stat fx-meta-monitor"
                  data-tooltip={t('link_card.monitoring_tooltip', {
                    interval: t('link_card.interval_' + link.check_interval),
                  })}
                  aria-label={t('link_card.monitoring_tooltip', {
                    interval: t('link_card.interval_' + link.check_interval),
                  })}
                >
                  <Icon d={I.bell} size={13} /> {t('link_card.monitoring')}
                </span>
              </>
            )}
          </div>

          <div className="fx-card-actions">
            {link.preview_status !== 'ok' && (
              <button
                className="fx-iconbtn"
                data-tooltip={t('link_card.refresh_preview')}
                data-tooltip-side="top"
                aria-label={t('link_card.refresh_preview')}
                onClick={() => refresh.mutate(link.id)}
              >
                <Icon d={I.refresh} size={14} />
              </button>
            )}
            <button
              className="fx-iconbtn"
              data-tooltip={t('link_card.edit_link')}
              data-tooltip-side="top"
              aria-label={t('common.edit')}
              onClick={() => onEdit(link)}
            >
              <Icon d={I.pen} size={14} />
            </button>
            <button
              className="fx-iconbtn fx-iconbtn-danger"
              data-tooltip={t('link_card.delete_link')}
              data-tooltip-side="top"
              aria-label={t('common.delete')}
              onClick={onDelete}
            >
              <Icon d={I.trash} size={14} />
            </button>
            <a
              className="fx-openbtn"
              href={goHref(link)}
              target="_blank"
              rel="noopener noreferrer"
              data-tooltip={t('link_card.open_action')}
              data-tooltip-side="top"
              aria-label={t('common.open_link_aria', { title: link.title })}
              onClick={onGo}
            >
              <span className="fx-openbtn-go">{t('link_card.open_action')}</span>
              <Icon d={I.arrowR} size={14} />
            </a>
          </div>
        </footer>
      </div>
    </article>
  )
}

function hostOf(u: string) {
  try {
    return new URL(u).hostname.replace(/^www\./, '')
  } catch {
    return u
  }
}

// Cap the visible description at ~200 chars so a verbose Thingiverse-style
// blurb (X-Axis upgrade compatibility lists, mod instructions, BOMs…)
// doesn't blow the card to 30+ lines and crush the rest of the grid. We
// prefer breaking at the last whitespace inside the budget so the cut
// doesn't land mid-word; if there's no decent space in the last 30 chars
// we fall back to a hard slice.
function truncateDesc(s: string, max = 200): string {
  if (s.length <= max) return s
  const slice = s.slice(0, max)
  const lastSpace = slice.lastIndexOf(' ')
  if (lastSpace > max - 30) return slice.slice(0, lastSpace).trimEnd() + '…'
  return slice.trimEnd() + '…'
}

function lastClick(link: Link, t: (key: string, opts?: Record<string, unknown>) => string): string {
  if (!link.last_clicked_at) return t('link_card.never_clicked')
  const ms = Date.now() - new Date(link.last_clicked_at).getTime()
  const min = Math.round(ms / 60000)
  if (min < 1) return t('link_card.last_click_now')
  if (min < 60) return t('link_card.last_click_minutes', { count: min })
  const h = Math.round(min / 60)
  if (h < 24) return t('link_card.last_click_hours', { count: h })
  const d = Math.round(h / 24)
  if (d === 1) return t('link_card.last_click_yesterday')
  if (d < 30) return t('link_card.last_click_days', { count: d })
  return new Date(link.last_clicked_at).toLocaleDateString()
}
