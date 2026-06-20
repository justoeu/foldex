import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { TagDialog } from './TagDialog'
import { TagManagerDialog } from './TagManagerDialog'
import { useTags } from '../api/tags'
import { useRecentChanges } from '../api/links'
import { relativeTime } from '../lib/time'
import { VERSION, BUILD_DATE } from '../version'

type Props = {
  selected: number[]
  onToggle: (id: number) => void
  onClear: () => void
  totalLinks: number
  collapsed: boolean
  onToggleCollapsed: () => void
  // Mobile drawer: when true and viewport is ≤768px, the sidebar slides
  // in from the left as an overlay. `onMobileClose` closes the drawer
  // without otherwise affecting the desktop collapsed state.
  mobileOpen?: boolean
  onMobileClose?: () => void
}

// Bucket sizes: top 5 by usage live in the always-shown "Frequentes" group.
// Everything else lands in "Outras" with a 15-item soft cap and a "load more"
// affordance so the sidebar never grows past a screenful by default.
const FREQ_COUNT = 5
const OTHER_INITIAL = 15

const FREQ_OPEN_KEY = 'foldex.sidebar.freqOpen'
const OTHER_OPEN_KEY = 'foldex.sidebar.otherOpen'
const RECENT_OPEN_KEY = 'foldex.sidebar.recentOpen'

function readBool(key: string, def: boolean): boolean {
  if (typeof localStorage === 'undefined') return def
  const v = localStorage.getItem(key)
  if (v === null) return def
  return v === '1'
}

export function TagSidebar({
  selected,
  onToggle,
  onClear,
  totalLinks,
  collapsed,
  onToggleCollapsed,
  mobileOpen = false,
  onMobileClose,
}: Props) {
  const { t } = useTranslation()
  const { data: tags = [], isLoading } = useTags()
  const [creating, setCreating] = useState(false)
  const [managing, setManaging] = useState(false)

  const [freqOpen, setFreqOpen] = useState(() => readBool(FREQ_OPEN_KEY, true))
  const [otherOpen, setOtherOpen] = useState(() => readBool(OTHER_OPEN_KEY, true))
  const [otherShowAll, setOtherShowAll] = useState(false)

  useEffect(() => {
    if (typeof localStorage === 'undefined') return
    localStorage.setItem(FREQ_OPEN_KEY, freqOpen ? '1' : '0')
  }, [freqOpen])
  useEffect(() => {
    if (typeof localStorage === 'undefined') return
    localStorage.setItem(OTHER_OPEN_KEY, otherOpen ? '1' : '0')
  }, [otherOpen])

  // Esc closes the mobile drawer when it's open. Hooked unconditionally
  // (handler no-ops when mobileOpen is false) so React doesn't see a
  // changing hook count.
  useEffect(() => {
    if (!mobileOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onMobileClose?.()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [mobileOpen, onMobileClose])

  const railClass = mobileOpen ? ' fx-sidebar-mobile-open' : ''

  // Collapsed = thin rail. Keeps the two tag-management actions accessible so
  // the user doesn't have to expand → click → collapse just to add a tag.
  // Note: the desktop-collapsed branch is skipped on mobile because mobileOpen
  // always wants the full view as a drawer.
  if (collapsed && !mobileOpen) {
    return (
      <aside className="fx-sidebar fx-sidebar-rail">
        <button
          className="fx-iconbtn"
          aria-label={t('sidebar.expand')}
          data-tooltip={t('sidebar.expand')}
          data-tooltip-side="right"
          onClick={onToggleCollapsed}
        >
          <Icon d={I.chevronRight} size={14} />
        </button>
        <div className="fx-sidebar-rail-icon">
          <Icon d={I.tag} size={15} />
        </div>
        <div className="fx-sidebar-rail-actions">
          <button
            className="fx-iconbtn"
            aria-label={t('sidebar.new_tag_tooltip')}
            data-tooltip={t('sidebar.new_tag_tooltip')}
            data-tooltip-side="right"
            onClick={() => setCreating(true)}
          >
            <Icon d={I.plus} size={14} />
          </button>
          <button
            className="fx-iconbtn"
            aria-label={t('sidebar.manage_tags_tooltip')}
            data-tooltip={t('sidebar.manage_tags_tooltip')}
            data-tooltip-side="right"
            disabled={tags.length === 0}
            onClick={() => setManaging(true)}
          >
            <Icon d={I.pen} size={13} />
          </button>
        </div>
        <TagDialog open={creating} onClose={() => setCreating(false)} />
        <TagManagerDialog open={managing} onClose={() => setManaging(false)} />
      </aside>
    )
  }

  const sorted = [...tags].sort((a, b) => (b.link_count ?? 0) - (a.link_count ?? 0))
  const freq = sorted.slice(0, FREQ_COUNT)
  const other = sorted.slice(FREQ_COUNT)
  const otherVisible = otherShowAll ? other : other.slice(0, OTHER_INITIAL)
  const otherHidden = Math.max(0, other.length - otherVisible.length)

  return (
    <aside className={'fx-sidebar' + railClass}>
      <div className="fx-side-head">
        <div className="fx-side-title">
          <Icon d={I.tag} size={15} /> <span>{t('sidebar.tags')}</span>
        </div>
        <div className="fx-side-head-actions">
          {mobileOpen && (
            <button
              className="fx-iconbtn fx-sidebar-mobile-close"
              aria-label={t('common.close')}
              data-tooltip={t('common.close')}
              onClick={onMobileClose}
            >
              <Icon d={I.x} size={14} />
            </button>
          )}
          <button
            className="fx-iconbtn fx-sidebar-collapse-btn"
            aria-label={t('sidebar.collapse')}
            data-tooltip={t('sidebar.collapse')}
            onClick={onToggleCollapsed}
          >
            <Icon d={I.chevronLeft} size={14} />
          </button>
        </div>
      </div>

      <div className="fx-sidebar-scroll">
        <button
          className={
            'fx-side-row fx-side-all' + (selected.length === 0 ? ' fx-side-row-active' : '')
          }
          onClick={onClear}
        >
          <span className="fx-all-dot">
            <Icon d={I.layers} size={13} />
          </span>
          <span className="fx-side-label">{t('sidebar.all_links')}</span>
          <span className="fx-side-count">{totalLinks}</span>
        </button>

        <RecentChangesSection enabled={!collapsed} />

        {isLoading && <div style={{ padding: 12, color: 'var(--fx-ink-4)' }}>{t('sidebar.loading')}</div>}

        {freq.length > 0 && (
          <>
            <SectionHeader
              label={t('sidebar.frequent')}
              open={freqOpen}
              onToggle={() => setFreqOpen((v) => !v)}
            />
            {freqOpen && freq.map((tag) => (
              <TagRow
                key={tag.id}
                name={tag.name}
                color={tag.color}
                count={tag.link_count ?? 0}
                active={selected.includes(tag.id)}
                onClick={() => onToggle(tag.id)}
              />
            ))}
          </>
        )}

        {other.length > 0 && (
          <>
            <SectionHeader
              label={t('sidebar.others')}
              open={otherOpen}
              onToggle={() => setOtherOpen((v) => !v)}
            />
            {otherOpen && (
              <>
                {otherVisible.map((tag) => (
                  <TagRow
                    key={tag.id}
                    name={tag.name}
                    color={tag.color}
                    count={tag.link_count ?? 0}
                    active={selected.includes(tag.id)}
                    onClick={() => onToggle(tag.id)}
                  />
                ))}
                {otherHidden > 0 && (
                  <button
                    type="button"
                    className="fx-side-more"
                    onClick={() => setOtherShowAll(true)}
                    data-tooltip={t('sidebar.load_more', { count: otherHidden })}
                  >
                    {t('sidebar.load_more', { count: otherHidden })}
                  </button>
                )}
              </>
            )}
          </>
        )}
      </div>

      <div className="fx-side-actions">
        <button
          className="fx-side-action"
          onClick={() => setCreating(true)}
          data-tooltip={t('sidebar.new_tag_tooltip')}
        >
          <Icon d={I.plus} size={13} />
          <span>{t('sidebar.new')}</span>
        </button>
        <button
          className="fx-side-action"
          onClick={() => setManaging(true)}
          disabled={tags.length === 0}
          data-tooltip={t('sidebar.manage_tags_tooltip')}
        >
          <Icon d={I.pen} size={13} />
          <span>{t('sidebar.manage')}</span>
        </button>
      </div>

      <div className="fx-side-version" aria-label={`foldex ${VERSION} build ${BUILD_DATE}`}>
        foldex v{VERSION} · {formatBuildDate(BUILD_DATE)}
      </div>

      <TagDialog open={creating} onClose={() => setCreating(false)} />
      <TagManagerDialog open={managing} onClose={() => setManaging(false)} />
    </aside>
  )
}

function RecentChangesSection({ enabled }: { enabled: boolean }) {
  const { t } = useTranslation()
  const { data: links = [], isLoading } = useRecentChanges(7, 10, enabled)
  const [open, setOpen] = useState(() => readBool(RECENT_OPEN_KEY, true))

  useEffect(() => {
    if (typeof localStorage === 'undefined') return
    localStorage.setItem(RECENT_OPEN_KEY, open ? '1' : '0')
  }, [open])

  if (!isLoading && links.length === 0) return null

  return (
    <>
      <SectionHeader
        label={t('sidebar.recent_updates')}
        open={open}
        onToggle={() => setOpen((v) => !v)}
      />
      {open && links.map((link) => (
        <a
          key={link.id}
          className="fx-side-row fx-side-recent"
          href={`/go/${link.slug || link.id}`}
          target="_blank"
          rel="noopener noreferrer"
          data-tooltip={link.title}
        >
          <span className="fx-side-dot fx-side-dot-recent" />
          <span className="fx-side-label">{link.title}</span>
          <span className="fx-side-count">
            {link.last_change_detected_at ? relativeTime(link.last_change_detected_at, t) : ''}
          </span>
        </a>
      ))}
    </>
  )
}

function SectionHeader({
  label,
  open,
  onToggle,
}: {
  label: string
  open: boolean
  onToggle: () => void
}) {
  return (
    <button
      type="button"
      className={'fx-side-sep fx-side-sep-toggle' + (open ? '' : ' fx-side-sep-collapsed')}
      onClick={onToggle}
      aria-expanded={open}
    >
      <Icon d={open ? I.chevronDown : I.chevronRight} size={11} stroke={2} />
      <span>{label}</span>
    </button>
  )
}

function formatBuildDate(iso: string): string {
  // ISO `YYYY-MM-DD` → pt-BR `DD/MM/YYYY`. Parsed with explicit Y/M/D rather
  // than `new Date(iso)` to avoid the UTC-midnight-shifts-a-day-back trap.
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso)
  if (!m) return iso
  return `${m[3]}/${m[2]}/${m[1]}`
}

function TagRow({
  name,
  color,
  count,
  active,
  onClick,
}: {
  name: string
  color: string
  count: number
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      className={'fx-side-row' + (active ? ' fx-side-row-active' : '')}
      onClick={onClick}
    >
      <span className="fx-side-dot" style={{ background: color }} />
      <span className="fx-side-label">{name}</span>
      <span className="fx-side-count">{count}</span>
    </button>
  )
}
