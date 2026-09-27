import { useMemo, useState } from 'react'
import { formatLocalYMD } from '../../lib/time'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { activityQueryKey, fetchOwnActivity, type AuditEntry } from '../../api/admin'
import { actionLabel } from '../../lib/auditLabels'

/** Page sizes the account feed offers. 50 is the default the server already uses. */
const PAGE_SIZES = [25, 50, 100] as const
type PageSize = (typeof PAGE_SIZES)[number]
const PAGE_SIZE_KEY = 'foldex.activity.pageSize'

/** Days at the top of the feed start open. Older days stay collapsed. */
const OPEN_DAYS = 3

/** The chart reads a wider slice than one list page, still one request. */
const CHART_SAMPLE = 200

function readPageSize(): PageSize {
  try {
    const n = Number(localStorage.getItem(PAGE_SIZE_KEY))
    if (n === 25 || n === 50 || n === 100) return n
  } catch {
    // Private mode: the selector still works, it just does not stick.
  }
  return 50
}

/** The chart window: two weeks, ending today. Same span the design mock shows. */
const CHART_DAYS = 14

type Kind = 'all' | 'links' | 'folders' | 'backups'

const KIND_BY_ENTITY: Record<string, Kind> = {
  link: 'links',
  folder: 'folders',
  backup: 'backups',
}

/** The dot color classes by action prefix — create/edit/delete/other. */
function dotClass(action: string): string {
  if (action.startsWith('create')) return 'fx-acc2-act-dot-create'
  if (action.startsWith('update') || action.startsWith('edit')) return 'fx-acc2-act-dot-edit'
  if (action.startsWith('delete')) return 'fx-acc2-act-dot-delete'
  return 'fx-acc2-act-dot-other'
}

function entryKind(e: AuditEntry): Kind {
  return (e.entity_kind && KIND_BY_ENTITY[e.entity_kind]) || 'all'
}

/** Local civil date, matching the chart's day buckets: a UTC slice would
 *  file entries near midnight onto the wrong day for non-UTC users. */
function dayKey(iso: string): string {
  return formatLocalYMD(new Date(iso))
}

/** True when `key` (YYYY-MM-DD) is today or one of the two days before it. */
function dayIsRecent(key: string): boolean {
  const day = new Date(`${key}T12:00:00`)
  const today = new Date()
  today.setHours(12, 0, 0, 0)
  const diff = Math.round((today.getTime() - day.getTime()) / 86_400_000)
  return diff >= 0 && diff < OPEN_DAYS
}

/**
 * The account's own activity — the other half of ADR-46's read split.
 *
 * This is the ONLY projection that returns the content label: the caller is the
 * row's actor, so the title of the link they edited is their own. The
 * administrative trail withholds it from everyone, in SQL, which is what lets
 * one table serve both readers without INV-045 depending on which component
 * happens to render it.
 *
 * The redesign adds the overview the raw feed lacked: a two-week bar chart,
 * a subject search and kind filters. The chart reads one wider sample. The
 * list is one page at a time — the keyset cursor is the previous page's last
 * id, so next and previous do not accumulate rows on screen.
 */
export function ActivitySection() {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState<Kind>('all')
  const [pageSize, setPageSize] = useState<PageSize>(readPageSize)
  // Index 0 is the first page (no cursor). Later entries are the `before` id
  // that opens that page. Going back reuses the cursor instead of refetching
  // a guess.
  const [cursors, setCursors] = useState<Array<number | undefined>>([undefined])
  const [page, setPage] = useState(0)
  const cursor = cursors[page]

  const chartFeed = useQuery({
    queryKey: [...activityQueryKey, 'chart'],
    queryFn: () => fetchOwnActivity(undefined, CHART_SAMPLE),
  })
  const feed = useQuery({
    queryKey: [...activityQueryKey, 'page', pageSize, cursor ?? 0],
    queryFn: () => fetchOwnActivity(cursor, pageSize),
    placeholderData: keepPreviousData,
  })

  const entries = feed.data ?? []
  const chartEntries = chartFeed.data ?? []

  // Chart first: counts per day over the window, computed where the bars are
  // drawn rather than kept in state — a memo over the loaded pages, so a
  // "load more" that adds older rows never re-slices the window.
  const chart = useMemo(() => {
    const days: { key: string; label: string; count: number }[] = []
    const fmtDay = new Intl.DateTimeFormat(i18nLocale(), { day: 'numeric' })
    for (let i = CHART_DAYS - 1; i >= 0; i--) {
      const d = new Date()
      d.setHours(12, 0, 0, 0)
      d.setDate(d.getDate() - i)
      days.push({ key: formatLocalYMD(d), label: fmtDay.format(d), count: 0 })
    }
    const byKey = new Map(days.map((d) => [d.key, d]))
    for (const e of chartEntries) {
      const bucket = byKey.get(dayKey(e.created_at))
      if (bucket) bucket.count++
    }
    return days
  }, [chartEntries])

  const q = query.trim().toLowerCase()
  const filtered = useMemo(
    () =>
      entries.filter(
        (e) => (kind === 'all' || entryKind(e) === kind) && (!q || (e.subject ?? '').toLowerCase().includes(q)),
      ),
    [entries, kind, q],
  )

  // Grouped by calendar day, newest first. Intl carries the locale's weekday
  // and month names — no hand-rolled calendar vocabulary to translate.
  const groups = useMemo(() => {
    const map = new Map<string, AuditEntry[]>()
    for (const e of filtered) {
      const k = dayKey(e.created_at)
      const list = map.get(k)
      if (list) list.push(e)
      else map.set(k, [e])
    }
    const fmtDay = new Intl.DateTimeFormat(i18nLocale(), {
      weekday: 'long',
      day: 'numeric',
      month: 'long',
    })
    const fmtTime = new Intl.DateTimeFormat(i18nLocale(), { hour: '2-digit', minute: '2-digit' })
    return [...map.entries()]
      .sort((a, b) => (a[0] < b[0] ? 1 : -1))
      .map(([key, items]) => ({
        key,
        recent: dayIsRecent(key),
        label: fmtDay.format(new Date(key + 'T12:00:00')),
        items: items.map((e) => ({
          id: e.id,
          time: fmtTime.format(new Date(e.created_at)),
          label: actionLabel(t, e.action),
          dot: dotClass(e.action),
          title: e.subject || t('admin.audit_detail_absent'),
          ip: e.ip ?? '',
        })),
      }))
  }, [filtered, t])

  const maxCount = Math.max(...chart.map((c) => c.count), 1)
  const hasNext = entries.length === pageSize
  const hasPrev = page > 0

  function goNext() {
    const last = entries.at(-1)?.id
    if (!last || !hasNext) return
    setCursors((prev) => {
      const next = prev.slice(0, page + 1)
      next.push(last)
      return next
    })
    setPage((n) => n + 1)
  }

  function goPrev() {
    if (!hasPrev) return
    setPage((n) => n - 1)
  }

  function changePageSize(next: PageSize) {
    setPageSize(next)
    setCursors([undefined])
    setPage(0)
    try {
      localStorage.setItem(PAGE_SIZE_KEY, String(next))
    } catch {
      // The new size still applies to this visit.
    }
  }

  // The two empty states mean different things and must not read alike: an
  // account with no history vs. filters that match nothing on this page.
  let emptyState = ''
  if (entries.length === 0) emptyState = t('admin.activity_empty')
  else if (groups.length === 0) emptyState = t('account.activity_no_results')

  return (
    <div>
      {feed.isPending && <div className="fx-acc2-empty fx-acc2-empty-soft">{t('common.loading')}</div>}
      {feed.isError && <div className="fx-acc2-empty">{t('admin.activity_unavailable')}</div>}

      {!feed.isPending && !feed.isError && (
        <>
          <div className="fx-acc2-chart-card">
            <div className="fx-acc2-chart-head">
              <span>{t('account.activity_window', { count: CHART_DAYS })}</span>
              <span className="fx-acc2-chart-total">
                {t('account.activity_total_loaded', { count: chartEntries.length })}
              </span>
            </div>
            <div className="fx-acc2-chart-bars" role="img" aria-label={t('account.activity_chart_aria')}>
              {chart.map((c) => (
                <div
                  className="fx-acc2-chart-col"
                  key={c.key}
                  title={t('account.activity_day_tip', { day: c.label, count: c.count })}
                >
                  <div
                    className={'fx-acc2-chart-bar' + (c.count ? '' : ' fx-acc2-chart-bar-empty')}
                    style={{ height: `${Math.round((c.count / maxCount) * 100)}%` }}
                  />
                </div>
              ))}
            </div>
            <div className="fx-acc2-chart-labels">
              {chart.map((c) => (
                <span className="fx-acc2-chart-label" key={c.key}>{c.label}</span>
              ))}
            </div>
          </div>

          <div className="fx-acc2-filters">
            <input
              className="fx-acc2-input fx-acc2-search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('account.activity_search_ph')}
              aria-label={t('account.activity_search_aria')}
            />
            <div className="fx-acc2-seg" role="group" aria-label={t('account.activity_filter_aria')}>
              {(['all', 'links', 'folders', 'backups'] as const).map((k) => (
                <button
                  key={k}
                  type="button"
                  aria-pressed={kind === k}
                  className="fx-acc2-seg-btn"
                  onClick={() => setKind(k)}
                >
                  {t(`account.activity_filter_${k}`)}
                </button>
              ))}
            </div>
          </div>

          {emptyState && <div className="fx-acc2-empty">{emptyState}</div>}

          <div className="fx-acc2-days">
            {groups.map((day) => (
              <DayGroup key={day.key} day={day} />
            ))}
          </div>

          <div className="fx-acc2-pager">
            <div className="fx-acc2-pager-nav">
              <button
                type="button"
                className="fx-acc2-btn-outline"
                disabled={!hasPrev || feed.isFetching}
                onClick={goPrev}
              >
                {t('account.activity_prev')}
              </button>
              <span className="fx-acc2-pager-page">{t('account.activity_page', { page: page + 1 })}</span>
              <button
                type="button"
                className="fx-acc2-btn-outline"
                disabled={!hasNext || feed.isFetching}
                onClick={goNext}
              >
                {t('account.activity_next')}
              </button>
            </div>
            <label className="fx-acc2-pager-size">
              <span>{t('account.activity_page_size')}</span>
              <select
                className="fx-acc2-select"
                aria-label={t('account.activity_page_size_aria')}
                value={pageSize}
                onChange={(e) => changePageSize(Number(e.target.value) as PageSize)}
              >
                {PAGE_SIZES.map((n) => (
                  <option key={n} value={n}>{n}</option>
                ))}
              </select>
            </label>
          </div>
        </>
      )}
    </div>
  )
}

type DayGroupModel = {
  key: string
  recent: boolean
  label: string
  items: Array<{ id: number; time: string; label: string; dot: string; title: string; ip: string }>
}

function DayGroup({ day }: Readonly<{ day: DayGroupModel }>) {
  const { t } = useTranslation()
  // Recent days start open. The button owns the toggle after that, so a
  // re-render from the search box does not snap an older day back shut.
  const [open, setOpen] = useState(day.recent)
  return (
    <div className="fx-acc2-day">
      <button
        type="button"
        className="fx-acc2-day-head"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <span>{day.label}</span>
        <span className="fx-acc2-day-count">
          {t('account.activity_day_count', { count: day.items.length })}
        </span>
      </button>
      {open && day.items.map((a) => (
        <div className="fx-acc2-act-row" key={a.id} data-testid="fx-activity-row">
          <span className="fx-acc2-act-time">{a.time}</span>
          <span className="fx-acc2-act-kind">
            <span className={'fx-acc2-act-dot ' + a.dot} aria-hidden="true" />
            {a.label}
          </span>
          <span className="fx-acc2-act-title" title={a.title}>{a.title}</span>
          <span className="fx-acc2-act-ip">{a.ip}</span>
        </div>
      ))}
    </div>
  )
}

function i18nLocale(): string | undefined {
  const loc = typeof document !== 'undefined' ? document.documentElement.lang : ''
  return loc || undefined
}

