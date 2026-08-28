import { useTranslation } from 'react-i18next'
import type { AuditStats } from '../../api/admin'
import { dayColumns, delta, deltaPercent, distributionWidth, type Delta } from './auditFormat'
import { actionLabel } from '../../lib/auditLabels'

/**
 * The four headline numbers.
 *
 * Each carries the same number for the PRECEDING window of equal length, which
 * the server computed — the client never derives a comparison it cannot see the
 * data for.
 */
export function AuditMetrics({ stats }: { stats: AuditStats }) {
  const { t } = useTranslation()
  const s = stats.totals
  const cards: { key: string; value: number; d: Delta; hint: string; tone: string }[] = [
    {
      key: 'events', value: s.events, tone: 'brand',
      d: deltaPercent(s.events, s.events_prev, 'never'),
      hint: t('admin.audit_metric_events_hint'),
    },
    {
      key: 'failures', value: s.failures, tone: 'danger',
      d: delta(s.failures, s.failures_prev, 'up'),
      hint: t('admin.audit_metric_failures_hint'),
    },
    {
      key: 'access', value: s.access_changes, tone: 'warn',
      d: delta(s.access_changes, s.access_changes_prev, 'up'),
      hint: t('admin.audit_metric_access_hint'),
    },
    {
      key: 'actors', value: s.actors, tone: 'ok',
      d: { label: t('admin.audit_metric_of_total', { total: s.active_users }), tone: 'neutral' },
      hint: t('admin.audit_metric_actors_hint'),
    },
  ]
  return (
    <div className="fx-aud-metrics">
      {cards.map((c) => (
        <div className="fx-aud-metric" key={c.key} data-testid={`fx-aud-metric-${c.key}`}>
          <div className="fx-aud-metric-head">
            <span className="fx-aud-metric-label">{t(`admin.audit_metric_${c.key}`)}</span>
            <span className={`fx-aud-dot fx-aud-dot-${c.tone}`} aria-hidden="true" />
          </div>
          <div className="fx-aud-metric-value">
            <strong>{c.value}</strong>
            <span className={`fx-aud-delta fx-aud-delta-${c.d.tone}`}>{c.d.label}</span>
          </div>
          <p className="fx-aud-metric-hint">{c.hint}</p>
        </div>
      ))}
    </div>
  )
}

/** The four series, in the order they stack. */
const SERIES = ['logins', 'failed', 'admin', 'content'] as const

/**
 * Events per day.
 *
 * Plain divs rather than a charting library: four stacked shares of a fixed
 * track is not a chart engine's worth of problem, and the alternative is a
 * dependency in the bundle of every visit for one screen an administrator opens
 * occasionally.
 */
export function AuditDaysChart({ stats }: { stats: AuditStats }) {
  const { t, i18n } = useTranslation()
  const columns = dayColumns(stats.days, i18n.language)
  const total = stats.days.reduce((a, d) => a + d.logins + d.failed + d.admin + d.content, 0)
  return (
    <section className="fx-aud-card fx-aud-chart" aria-labelledby="fx-aud-days-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-aud-days-title">{t('admin.audit_days_title')}</h3>
          <p>{t('admin.audit_days_desc', { days: stats.days.length, count: total })}</p>
        </div>
        <ul className="fx-aud-legend">
          {SERIES.map((k) => (
            <li key={k}>
              <span className={`fx-aud-swatch fx-aud-swatch-${k}`} aria-hidden="true" />
              {t(`admin.audit_series_${k}`)}
            </li>
          ))}
        </ul>
      </header>

      {/* One list, not a table: each column is a labelled value, and a table
          would promise a grid of relationships the chart does not have. */}
      <ul className="fx-aud-bars">
        {columns.map((c) => (
          <li key={c.day} className="fx-aud-bar" data-testid="fx-aud-bar">
            <div
              className="fx-aud-bar-track"
              title={t('admin.audit_days_tip', {
                day: c.label, failed: c.counts.failed, count: c.total,
              })}
            >
              {SERIES.map((k) =>
                c[k] > 0 ? (
                  <span
                    key={k}
                    className={`fx-aud-bar-seg fx-aud-bar-${k}`}
                    style={{ height: `${c[k]}%` }}
                  />
                ) : null,
              )}
            </div>
            <span className="fx-aud-bar-label">{c.label}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** Share of each action in the period. */
export function AuditDistribution({ stats }: { stats: AuditStats }) {
  const { t } = useTranslation()
  const max = Math.max(1, ...stats.distribution.map((d) => d.count))
  const total = stats.distribution.reduce((a, d) => a + d.count, 0)
  return (
    <section className="fx-aud-card" aria-labelledby="fx-aud-dist-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-aud-dist-title">{t('admin.audit_distribution_title')}</h3>
          <p>{t('admin.audit_distribution_desc')}</p>
        </div>
      </header>
      {stats.distribution.length === 0 ? (
        <p className="fx-empty">{t('admin.audit_empty')}</p>
      ) : (
        <ul className="fx-aud-dist">
          {stats.distribution.slice(0, 8).map((d) => (
            <li key={d.action}>
              <div className="fx-aud-dist-head">
                <span>
                  <span className={`fx-aud-swatch fx-aud-swatch-${d.category}`} aria-hidden="true" />
                  {actionLabel(t, d.action)}
                </span>
                <span className="fx-aud-dist-count">
                  {d.count} · {total > 0 ? Math.round((d.count / total) * 100) : 0}%
                </span>
              </div>
              <div className="fx-aud-dist-track">
                <span
                  className={`fx-aud-dist-fill fx-aud-dist-${d.category}`}
                  style={{ width: `${distributionWidth(d.count, max)}%` }}
                />
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
