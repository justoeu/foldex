import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  auditQueryKey, auditStatsQueryKey, fetchAudit, fetchAuditStats,
  fetchIPBlocks, ipBlocksQueryKey,
} from '../../api/admin'
import { useAuth } from '../../auth/AuthProvider'
import { AUDIT_WINDOWS, timelineActionChips } from './auditFormat'
import { actionLabel } from '../../lib/auditLabels'
import { AuditAnomalies } from './AuditAnomalies'
import { AuditDaysChart, AuditDistribution, AuditMetrics } from './AuditCharts'
import { AuditActors, AuditBlocklist, AuditOrigins, AuditRiskCard } from './AuditSignals'
import { AuditTimeline } from './AuditTimeline'
import { downloadAuditCsv } from './auditExport'
import { useAuditFilters } from './useAuditFilters'

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
  const {
    period, action, category, search, oldestFirst, pages, filter, timeline,
    inspectIP, setPeriod, setSearch, toggleOrder, setCategory, setAction,
    clearFilters, nextPage, prevPage,
  } = useAuditFilters()
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<string | null>(null)

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

  const entries = list.data ?? []

  return (
    <div className="fx-aud">
      <header className="fx-aud-head">
        <div className="fx-aud-head-text">
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
                onClick={() => setPeriod(w)}
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
                await downloadAuditCsv(filter)
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

      <AuditAnomalies canBlock={canBlock} onInspect={inspectIP} />

      <AuditBlocklist blocks={blocks.data ?? []} canBlock={canBlock} />

      <section
        className="fx-aud-card fx-aud-list"
        aria-labelledby="fx-aud-list-title"
        ref={timeline.ref}
        tabIndex={-1}
      >
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
                onChange={(e) => setSearch(e.target.value)}
              />
            </label>
            <button
              type="button"
              className="fx-pillbtn"
              aria-pressed={oldestFirst}
              onClick={toggleOrder}
            >
              {t(oldestFirst ? 'admin.audit_sort_oldest' : 'admin.audit_sort_newest')}
            </button>
          </div>
        </header>

        <div className="fx-aud-chips">
          <Chip active={action === '' && category === ''} onClick={clearFilters}>
            {t('admin.audit_filter_all')}
          </Chip>
          {(['identity', 'content'] as const).map((c) => (
            <Chip key={c} active={category === c} onClick={() => setCategory(c)}>
              {t(`admin.audit_category_${c}`)}
            </Chip>
          ))}
          {timelineActionChips(stats.data?.distribution ?? []).map((d) => (
            <Chip key={d.action} active={action === d.action} onClick={() => setAction(d.action)}>
              {actionLabel(t, d.action)}
              {d.count > 0 && <span className="fx-aud-chip-count">{d.count}</span>}
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
            <button type="button" className="fx-pillbtn" onClick={prevPage}>
              {t('admin.audit_prev')}
            </button>
          )}
          {entries.length > 0 && (
            <button
              type="button"
              className="fx-pillbtn"
              onClick={() => {
                const last = entries[entries.length - 1]
                if (last) nextPage(last.id)
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
