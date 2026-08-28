import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  auditQueryKey, auditStatsQueryKey, exportAuditCsv, fetchAudit, fetchAuditStats,
  fetchIPBlocks, ipBlocksQueryKey,
  type AuditCategory, type AuditQuery, type AuditWindow,
} from '../../api/admin'
import { useAuth } from '../../auth/AuthProvider'
import { AUDIT_WINDOWS } from './auditFormat'
import { actionLabel } from '../../lib/auditLabels'
import { AuditDaysChart, AuditDistribution, AuditMetrics } from './AuditCharts'
import { AuditActors, AuditBlocklist, AuditOrigins, AuditRiskCard } from './AuditSignals'
import { AuditTimeline } from './AuditTimeline'

/** How long the search box waits before it becomes a request. */
const SEARCH_DEBOUNCE_MS = 300

/**
 * The administrative trail — ADR-46.
 *
 * Two queries, one filter. The header aggregates depend only on the WINDOW, so
 * they keep their own key and are not refetched when the action chip or the
 * search term changes: those narrow the list, not the period the numbers
 * describe. Sharing one key would make every chip click re-run six aggregate
 * queries to display the same header.
 *
 * Pagination is keyset (`before` = the last id shown), not offset: the trail
 * grows at its head, so an offset-paged second page would repeat rows the first
 * already showed as soon as anything was written between the two requests.
 */
export function AuditSection() {
  const { t } = useTranslation()
  const { session } = useAuth()
  const [period, setPeriod] = useState<AuditWindow>('7d')
  const [action, setAction] = useState('')
  const [category, setCategory] = useState<AuditCategory | ''>('')
  const [search, setSearch] = useState('')
  const [oldestFirst, setOldestFirst] = useState(false)
  const [pages, setPages] = useState<number[]>([])
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<string | null>(null)

  // Debounced so a typed address is one request, not one per keystroke. The
  // predicate behind it is a LIKE over the window and deliberately not indexed.
  const debounced = useDebounced(search, SEARCH_DEBOUNCE_MS)

  const filter: AuditQuery = useMemo(() => ({
    window: period,
    action: action || undefined,
    category: category || undefined,
    q: debounced || undefined,
    before: pages.length > 0 ? pages[pages.length - 1] : undefined,
    order: oldestFirst ? 'asc' : undefined,
  }), [period, action, category, debounced, pages, oldestFirst])

  const list = useQuery({ queryKey: auditQueryKey(filter), queryFn: () => fetchAudit(filter) })
  const stats = useQuery({
    queryKey: auditStatsQueryKey(period),
    queryFn: () => fetchAuditStats(period),
  })
  // Only the owner may write the blocklist, but anyone who can read the trail
  // should see what is on it — an admin who cannot tell an address is already
  // blocked has no way to interpret the silence from it.
  const blocks = useQuery({ queryKey: ipBlocksQueryKey, queryFn: fetchIPBlocks })

  // `instance.ip_block` is LOCKED and owner-only (ADR-46), so the client asks
  // the same question the server does — the role — rather than reading a
  // permission list that can never contain it for anyone else. Affordance, not
  // enforcement: the route is gated regardless of what renders here.
  const canBlock = session.status === 'authenticated' && session.user.role === 'owner'

  // Any change to what is being FILTERED restarts pagination: a cursor taken
  // from the previous result set points at an id this one may not contain.
  const resetPaging = () => setPages([])

  const entries = list.data ?? []

  return (
    <div className="fx-aud">
      <header className="fx-aud-head">
        <div className="fx-aud-head-text">
          <h2>{t('admin.audit_title')}</h2>
          <p>{t('admin.audit_lede')}</p>
        </div>
        <div className="fx-aud-head-tools">
          <div className="fx-aud-windows" role="group" aria-label={t('admin.audit_window_label')}>
            {AUDIT_WINDOWS.map((w) => (
              <button
                key={w}
                type="button"
                className={'fx-aud-window' + (period === w ? ' fx-aud-window-on' : '')}
                aria-pressed={period === w}
                onClick={() => { setPeriod(w); resetPaging() }}
              >
                {t(`admin.audit_window_${w}`)}
              </button>
            ))}
          </div>
          <button
            type="button"
            className="fx-btn"
            disabled={exporting}
            onClick={async () => {
              setExporting(true)
              setExportError(null)
              try {
                await downloadCsv(filter)
              } catch {
                setExportError(t('admin.audit_export_failed'))
              } finally {
                setExporting(false)
              }
            }}
          >
            {exporting ? t('admin.audit_exporting') : t('admin.audit_export')}
          </button>
        </div>
      </header>
      {exportError && <p className="fx-aud-error" role="alert">{exportError}</p>}

      {stats.isError && <div className="fx-empty">{t('admin.audit_unavailable')}</div>}
      {stats.data && (
        <>
          <AuditMetrics stats={stats.data} />
          <div className="fx-aud-grid fx-aud-grid-charts">
            <AuditDaysChart stats={stats.data} />
            <AuditDistribution stats={stats.data} />
          </div>
          <div className="fx-aud-grid fx-aud-grid-signals">
            <AuditActors stats={stats.data} />
            <AuditOrigins stats={stats.data} canBlock={canBlock} />
            <AuditRiskCard stats={stats.data} canBlock={canBlock} />
          </div>
        </>
      )}

      <AuditBlocklist blocks={blocks.data ?? []} canBlock={canBlock} />

      <section className="fx-aud-card fx-aud-list" aria-labelledby="fx-aud-list-title">
        <header className="fx-aud-card-head">
          <div>
            <h3 id="fx-aud-list-title">{t('admin.audit_timeline_title')}</h3>
            <p>{t('admin.audit_timeline_desc', { count: entries.length })}</p>
          </div>
          <div className="fx-aud-list-tools">
            <label className="fx-aud-search">
              <span className="fx-visually-hidden">{t('admin.audit_search_label')}</span>
              <input
                className="fx-input"
                type="search"
                value={search}
                placeholder={t('admin.audit_search_placeholder')}
                onChange={(e) => { setSearch(e.target.value); resetPaging() }}
              />
            </label>
            <button
              type="button"
              className="fx-pillbtn"
              aria-pressed={oldestFirst}
              onClick={() => { setOldestFirst((v) => !v); resetPaging() }}
            >
              {t(oldestFirst ? 'admin.audit_sort_oldest' : 'admin.audit_sort_newest')}
            </button>
          </div>
        </header>

        <div className="fx-aud-chips">
          <Chip active={action === '' && category === ''} onClick={() => {
            setAction(''); setCategory(''); resetPaging()
          }}>
            {t('admin.audit_filter_all')}
          </Chip>
          {(['identity', 'content'] as const).map((c) => (
            <Chip key={c} active={category === c} onClick={() => {
              setCategory(category === c ? '' : c); setAction(''); resetPaging()
            }}>
              {t(`admin.audit_category_${c}`)}
            </Chip>
          ))}
          {(stats.data?.distribution ?? []).slice(0, 6).map((d) => (
            <Chip key={d.action} active={action === d.action} onClick={() => {
              setAction(action === d.action ? '' : d.action); setCategory(''); resetPaging()
            }}>
              {actionLabel(t, d.action)}
              <span className="fx-aud-chip-count">{d.count}</span>
            </Chip>
          ))}
        </div>

        {list.isPending && <div className="fx-empty">{t('common.loading')}</div>}
        {list.isError && <div className="fx-empty">{t('admin.audit_unavailable')}</div>}
        {!list.isPending && !list.isError && entries.length === 0 && (
          <div className="fx-empty">
            <strong>{t('admin.audit_empty')}</strong>
            <span>{t('admin.audit_empty_hint')}</span>
          </div>
        )}
        {entries.length > 0 && <AuditTimeline entries={entries} />}

        <div className="fx-aud-pager">
          {pages.length > 0 && (
            <button type="button" className="fx-pillbtn" onClick={() => setPages((p) => p.slice(0, -1))}>
              {t('admin.audit_prev')}
            </button>
          )}
          {entries.length > 0 && (
            <button
              type="button"
              className="fx-pillbtn"
              onClick={() => {
                const last = entries[entries.length - 1]
                if (last) setPages((p) => [...p, last.id])
              }}
            >
              {t('admin.audit_next')}
            </button>
          )}
        </div>
      </section>
    </div>
  )
}

function Chip({
  active, onClick, children,
}: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      className={'fx-aud-chip' + (active ? ' fx-aud-chip-on' : '')}
      aria-pressed={active}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

/**
 * Saves the CSV the server streamed.
 *
 * The blob is fetched through the axios client (cookies and the CSRF header)
 * and handed to the browser through an object URL. The URL is revoked in a
 * `finally`, because leaking one pins the whole file in memory for the life of
 * the document — and this file is deliberately allowed to be large.
 */
async function downloadCsv(filter: AuditQuery) {
  const blob = await exportAuditCsv(filter)
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = `foldex-audit-${new Date().toISOString().slice(0, 10)}.csv`
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

/**
 * Delays a value until it stops changing.
 *
 * The timer is cleared on every change AND on unmount — without the cleanup a
 * component unmounted mid-type would set state on a gone tree, which React
 * tolerates silently and which leaves the request in flight for nothing.
 */
function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(id)
  }, [value, ms])
  return settled
}
