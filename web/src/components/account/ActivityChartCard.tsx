import { useTranslation } from 'react-i18next'
import { CHART_DAYS, type ActivityDayBucket } from './activityModel'

/** The two-week activity bar chart card (presentational). */
export function ActivityChartCard({
  chart,
  totalLoaded,
}: Readonly<{
  chart: ActivityDayBucket[]
  totalLoaded: number
}>) {
  const { t } = useTranslation()
  const maxCount = Math.max(...chart.map((c) => c.count), 1)
  return (
    <div className="fx-acc2-chart-card">
      <div className="fx-acc2-chart-head">
        <span>{t('account.activity_window', { count: CHART_DAYS })}</span>
        <span className="fx-acc2-chart-total">
          {t('account.activity_total_loaded', { count: totalLoaded })}
        </span>
      </div>
      <div className="fx-acc2-chart-bars" role="img" aria-label={t('account.activity_chart_aria')}>
        {chart.map((c) => (
          <div
            className="fx-acc2-chart-col"
            key={c.key}
            role="img"
            aria-label={t('account.activity_day_tip', { day: c.label, count: c.count })}
            data-tooltip={t('account.activity_day_tip', { day: c.label, count: c.count })}
            data-tooltip-side="top"
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
  )
}
