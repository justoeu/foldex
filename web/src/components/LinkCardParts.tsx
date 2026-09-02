import type { TFunction } from 'i18next'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { hostOf } from '../lib/url'
import { goHref } from '../api/links'
import { relativeTime } from '../lib/time'
import type { Link } from '../api/types'

type CardActions = {
  onEdit: (link: Link) => void
  onDelete: (link: Link) => void
  onPin: (link: Link, pinned: boolean) => void
  onRefreshPreview: (id: number) => void
  onAddImage: (link: Link) => void
  onMarkSeen: (id: number) => void
}

export function LinkCardBadges({
  link,
  unseenChange,
  actions,
  t,
}: {
  link: Link
  unseenChange: boolean
  actions: CardActions
  t: TFunction
}) {
  const togglePin = (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    actions.onPin(link, !link.pinned)
  }
  const markSeen = (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation()
    actions.onMarkSeen(link.id)
  }

  return (
    <>
      <button
        type="button"
        className={'fx-card-pin-badge' + (link.pinned ? '' : ' fx-card-pin-off')}
        onClick={togglePin}
        aria-label={link.pinned ? t('link_card.unpin') : t('link_card.pin')}
        data-tooltip={link.pinned ? t('link_card.unpin_tooltip') : t('link_card.pin_top_tooltip')}
        data-tooltip-side="left"
      >
        <Icon d={I.pin} size={13} stroke={2} />
      </button>
      {unseenChange && (
        <button
          type="button"
          className="fx-card-update-badge"
          onClick={markSeen}
          aria-label={t('link_card.mark_seen_aria')}
          data-tooltip={t('link_card.update_detected_tooltip', {
            when: relativeTime(link.last_change_detected_at!, t),
          })}
          data-tooltip-side="left"
        >
          <Icon d={I.bell} size={13} stroke={2} />
        </button>
      )}
    </>
  )
}

export function LinkCardPreview({
  link,
  previewSrc,
  onGo,
  onError,
}: {
  link: Link
  previewSrc: string | undefined
  onGo: () => void
  onError: () => void
}) {
  if (!previewSrc) return null
  return (
    <a
      className="fx-preview fx-preview-img"
      href={goHref(link)}
      target="_blank"
      rel="noopener noreferrer"
      onClick={onGo}
    >
      <img
        src={previewSrc}
        alt=""
        referrerPolicy="no-referrer"
        loading="lazy"
        decoding="async"
        onError={onError}
        style={{ width: '100%', height: '100%', objectFit: 'scale-down', display: 'block' }}
      />
    </a>
  )
}

export function LinkCardBody({
  link,
  showPreview,
  actions,
  onGo,
  t,
}: {
  link: Link
  showPreview: boolean
  actions: CardActions
  onGo: () => void
  t: TFunction
}) {
  return (
    <div className="fx-card-body">
      <header className="fx-card-head">
        <Favicon link={link} size={showPreview ? 28 : 36} />
        <div className="fx-card-head-text">
          <h3 className="fx-card-title">
            <a
              href={goHref(link)}
              target="_blank"
              rel="noopener noreferrer"
              className="fx-card-title-link"
              onClick={onGo}
            >
              {link.title}
            </a>
          </h3>
          <div className="fx-card-host">{hostOf(link.url)}</div>
        </div>
      </header>
      {link.description && <p className="fx-card-desc">{truncateDesc(link.description)}</p>}
      {link.tags.length > 0 && (
        <div className="fx-card-tags">
          {link.tags.map((tag) => <TagChip key={tag.id} tag={tag} />)}
        </div>
      )}
      <footer className="fx-card-foot">
        <LinkCardMeta link={link} t={t} />
        <LinkCardActions link={link} showPreview={showPreview} actions={actions} onGo={onGo} t={t} />
      </footer>
    </div>
  )
}

function LinkCardMeta({ link, t }: { link: Link; t: TFunction }) {
  const monitoringTooltip = link.check_interval
    ? t('link_card.monitoring_tooltip', { interval: t('link_card.interval_' + link.check_interval) })
    : undefined
  return (
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
          <span className="fx-meta-warn"><Icon d={I.alert} size={13} /> {t('link_card.preview_failed')}</span>
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
      {link.check_interval && (
        <>
          <span className="fx-meta-sep" />
          <span
            className="fx-meta-stat fx-meta-monitor"
            data-tooltip={monitoringTooltip}
            aria-label={monitoringTooltip}
          >
            <Icon d={I.bell} size={13} /> {t('link_card.monitoring')}
          </span>
        </>
      )}
    </div>
  )
}

function LinkCardActions({
  link,
  showPreview,
  actions,
  onGo,
  t,
}: {
  link: Link
  showPreview: boolean
  actions: CardActions
  onGo: () => void
  t: TFunction
}) {
  return (
    <div className="fx-card-actions">
      {/* Gated on what the reader SEES, not on what the backend concluded.
          `preview_status === 'ok'` with an empty og_image_url is the ordinary
          outcome for a page that simply has no og:image — the fetch succeeded,
          so nothing failed, so no warning and no recapture button rendered, and
          the card offered no way out of being blank. `showPreview` is false for
          all three blank shapes: ok-and-empty, failed, and an image that 404s
          at render time (INV-082). */}
      {!showPreview && (
        <button
          type="button"
          className="fx-iconbtn"
          data-tooltip={t('link_card.add_image')}
          data-tooltip-side="top"
          aria-label={t('link_card.add_image')}
          onClick={() => actions.onAddImage(link)}
        >
          <Icon d={I.image} size={14} />
        </button>
      )}
      {/* Same trigger as the button above, and for the same reason: a blank
          card needs BOTH ways out, and `preview_status` hid this one exactly
          where it works. A page with no og:image reports 'ok' — nothing
          failed — yet that is precisely the condition the worker answers with
          a SCREENSHOT (INV-083: fallback, never default). Gating on status
          meant the one action that could fill this card was the one action not
          offered. */}
      {(!showPreview || link.preview_status !== 'ok') && (
        <button
          type="button"
          className="fx-iconbtn"
          data-tooltip={link.preview_status === 'pending' ? t('link_card.capturing') : t('link_card.refresh_preview')}
          data-tooltip-side="top"
          aria-label={link.preview_status === 'pending' ? t('link_card.capturing') : t('link_card.refresh_preview')}
          aria-busy={link.preview_status === 'pending' || undefined}
          disabled={link.preview_status === 'pending'}
          onClick={() => actions.onRefreshPreview(link.id)}
        >
          {link.preview_status === 'pending'
            ? <span className="fx-spinner" aria-hidden="true" />
            : <Icon d={I.refresh} size={14} />}
        </button>
      )}
      <button
        type="button"
        className="fx-iconbtn"
        data-tooltip={t('link_card.edit_link')}
        data-tooltip-side="top"
        aria-label={t('common.edit')}
        onClick={() => actions.onEdit(link)}
      >
        <Icon d={I.pen} size={14} />
      </button>
      <button
        type="button"
        className="fx-iconbtn fx-iconbtn-danger"
        data-tooltip={t('link_card.delete_link')}
        data-tooltip-side="top"
        aria-label={t('common.delete')}
        onClick={() => actions.onDelete(link)}
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
  )
}

function truncateDesc(description: string, max = 200): string {
  if (description.length <= max) return description
  const slice = description.slice(0, max)
  const lastSpace = slice.lastIndexOf(' ')
  if (lastSpace > max - 30) return slice.slice(0, lastSpace).trimEnd() + '…'
  return slice.trimEnd() + '…'
}

function lastClick(link: Link, t: TFunction): string {
  if (!link.last_clicked_at) return t('link_card.never_clicked')
  const minutes = Math.floor((Date.now() - new Date(link.last_clicked_at).getTime()) / 60_000)
  if (minutes < 1) return t('link_card.last_click_now')
  if (minutes < 60) return t('link_card.last_click_minutes', { count: minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t('link_card.last_click_hours', { count: hours })
  const days = Math.floor(hours / 24)
  if (days === 1) return t('link_card.last_click_yesterday')
  if (days < 30) return t('link_card.last_click_days', { count: days })
  return new Date(link.last_clicked_at).toLocaleDateString()
}
