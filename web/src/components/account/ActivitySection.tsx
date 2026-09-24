import { useMemo, useState } from 'react'
import { formatLocalYMD } from '../../lib/time'
import { useInfiniteQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { activityQueryKey, fetchOwnActivity, type AuditEntry } from '../../api/admin'
import { actionLabel } from '../../lib/auditLabels'

/** One page of the feed. The server clamps this too. */
const PAGE_SIZE = 50

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
 * a subject search and kind filters — all computed over the pages already
 * loaded, client-side, because the feed is the account's own and its size
 * bounds the work. The infinite scroll stays: the chart summarizes what was
 * loaded, it does not claim the whole history.
 */
export function ActivitySection() {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState<Kind>('all')

  // useInfiniteQuery rather than a cursor in state plus an accumulating array:
  // the accumulation, the deduplication and "is there another page" are exactly
  // what it does, and the hand-rolled version had to append from inside the
  // fetcher — a side effect in a function React Query is free to re-run on
  // focus, which is why it needed a dedupe set to stay correct.
  const feed = useInfiniteQuery({
    queryKey: activityQueryKey,
    queryFn: ({ pageParam }) => fetchOwnActivity(pageParam),
    initialPageParam: undefined as number | undefined,
    // The keyset cursor is the last id on the page. A short page is the end of
    // the feed — asking again would return the same nothing.
    getNextPageParam: (last: AuditEntry[]) =>
      last.length < PAGE_SIZE ? undefined : last.at(-1)?.id,
  })

  // Memoized: pages.flat() would allocate a fresh array per render and defeat
  // the chart/groups memos below — every keystroke in the search box would
  // re-bucket and re-format the whole feed.
  const entries = useMemo(() => feed.data?.pages.flat() ?? [], [feed.data])

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
    for (const e of entries) {
      const bucket = byKey.get(dayKey(e.created_at))
      if (bucket) bucket.count++
    }
    return days
  }, [entries, t])

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

  // The two empty states mean different things and must not read alike: an
  // account with no history vs. filters that match nothing of what loaded.
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
                {t('account.activity_total_loaded', { count: entries.length })}
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
              <div className="fx-acc2-day" key={day.label}>
                <div className="fx-acc2-day-head">
                  <span>{day.label}</span>
                  <span className="fx-acc2-day-count">
                    {t('account.activity_day_count', { count: day.items.length })}
                  </span>
                </div>
                {day.items.map((a) => (
                  <div className="fx-acc2-act-row" key={a.id} data-testid="fx-activity-row">
                    <span className="fx-acc2-act-time">{a.time}</span>
                    <span className="fx-acc2-act-kind">
                      <span className={'fx-acc2-act-dot ' + a.dot} aria-hidden="true" />
                      {a.label}
                    </span>
                    {/* Rendered as TEXT. The subject is a title the user typed, and
                        the one thing that must never happen to it is being parsed as
                        markup — note bodies have a sanitizer for that reason, and a
                        label has no business carrying any. */}
                    <span className="fx-acc2-act-title" title={a.title}>{a.title}</span>
                    <span className="fx-acc2-act-ip">{a.ip}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>

          {feed.hasNextPage && (
            <button
              type="button"
              className="fx-acc2-btn-outline fx-acc2-mt16"
              disabled={feed.isFetchingNextPage}
              onClick={() => void feed.fetchNextPage()}
            >
              {t('admin.activity_more')}
            </button>
          )}
        </>
      )}
    </div>
  )
}

function i18nLocale(): string | undefined {
  const loc = typeof document !== 'undefined' ? document.documentElement.lang : ''
  return loc || undefined
}

