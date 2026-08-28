import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  anomaliesQueryKey, fetchAnomalies,
  type Anomaly, type AnomalyWindow,
} from '../../api/admin'
import { anomalySeverityClass, blockReasonKey, sortAnomalies, spanMinutes } from './abuseFormat'
import { blockable } from './auditFormat'
import { useBlockControls } from './AuditSignals'

/**
 * The lookbacks the anomalies endpoint accepts.
 *
 * Deliberately NOT the audit trail's `AUDIT_WINDOWS`: that vocabulary is
 * 24h/7d/30d and this one is 15m/1h/24h/7d, because an anomaly is a burst and
 * a burst is measured in minutes. Sharing the constant would have forced one
 * of the two screens to offer a period its endpoint answers 400 for.
 */
const ANOMALY_WINDOWS: AnomalyWindow[] = ['15m', '1h', '24h', '7d']

type Props = {
  /** Whether the blocklist may be written. Affordance; the route is the gate. */
  canBlock: boolean
  /** Hands an address to the trail below, already filtered. */
  onInspect: (ip: string) => void
}

/**
 * Origins the instance currently considers anomalous, worst first.
 *
 * Two things this panel has to say out loud, and both are why it exists:
 *
 *   - What the evidence IS, in numbers. "14 distinct accounts · 22 failures ·
 *     9 min" is what separates a spray from someone who forgot their password;
 *     a coloured badge alone would not.
 *   - Whether anything VOUCHED for the address. On an instance behind a proxy,
 *     an untrusted address is the proxy's own and the row is about everyone
 *     behind it. Hiding that is how an operator blocks their own nginx — the
 *     defect docs/SDD-ABUSE-DEFENSE.md was written about.
 *
 * The block is never automatic. It is permanent, and behind a proxy it is
 * collective, so it goes through the same confirmation the audit screen uses.
 */
export function AuditAnomalies({ canBlock, onInspect }: Props) {
  const { t } = useTranslation()
  const [window, setWindow] = useState<AnomalyWindow>('24h')
  const query = useQuery({
    queryKey: anomaliesQueryKey(window),
    queryFn: () => fetchAnomalies(window),
  })
  const controls = useBlockControls()

  const rows = sortAnomalies(query.data?.anomalies ?? [])
  const thresholds = query.data?.thresholds

  return (
    <section className="fx-aud-card" aria-labelledby="fx-anom-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-anom-title">{t('admin.anomaly_title')}</h3>
          <p>{t('admin.anomaly_desc')}</p>
          {/* The panel states the numbers it is applying rather than leaving
              the reader to open the limits screen to find out why a row is
              here — or, worse, why one is not. */}
          {thresholds && (
            <p className="fx-anom-thresholds">
              {t('admin.anomaly_thresholds', {
                spray: thresholds.spray_accounts,
                hammer: thresholds.hammer_failures,
                minutes: thresholds.window_minutes,
              })}
            </p>
          )}
        </div>
        <div className="fx-aud-windows" role="group" aria-label={t('admin.anomaly_window_label')}>
          {ANOMALY_WINDOWS.map((w) => (
            <button
              key={w}
              type="button"
              className={'fx-aud-window' + (window === w ? ' fx-aud-window-on' : '')}
              aria-pressed={window === w}
              onClick={() => setWindow(w)}
            >
              {t(`admin.anomaly_window_${w}`)}
            </button>
          ))}
        </div>
      </header>

      {query.isPending && <div className="fx-empty">{t('common.loading')}</div>}
      {query.isError && <div className="fx-empty">{t('admin.anomaly_unavailable')}</div>}
      {!query.isPending && !query.isError && rows.length === 0 && (
        <div className="fx-empty">
          <strong>{t('admin.anomaly_empty')}</strong>
          <span>{t('admin.anomaly_empty_hint')}</span>
        </div>
      )}

      {rows.length > 0 && (
        <ul className="fx-aud-rows">
          {rows.map((a) => (
            <AnomalyRow
              // first_seen is in the key because the pair (kind, ip) is the
              // aggregation's grouping and not a promise of uniqueness — two
              // rows for one pair would silently collapse into one.
              key={`${a.kind}:${a.ip}:${a.first_seen}`}
              anomaly={a}
              canBlock={canBlock}
              busy={controls.busy}
              onInspect={onInspect}
              onBlock={(ip, reason) => void controls.askBlock(ip, reason)}
            />
          ))}
        </ul>
      )}

      {controls.error && <p className="fx-aud-error" role="alert">{controls.error}</p>}
    </section>
  )
}

function AnomalyRow({
  anomaly, canBlock, busy, onInspect, onBlock,
}: {
  anomaly: Anomaly
  canBlock: boolean
  busy: boolean
  onInspect: (ip: string) => void
  onBlock: (ip: string, reason: string) => void
}) {
  const { t } = useTranslation()
  const minutes = spanMinutes(anomaly)
  const reason = t(blockReasonKey(anomaly.kind), {
    accounts: anomaly.distinct_accounts,
    failures: anomaly.failures,
    throttles: anomaly.throttles,
    minutes,
  })

  return (
    <li className="fx-aud-row fx-anom-row">
      <div className="fx-aud-row-main">
        <span className="fx-anom-head">
          <span className={anomalySeverityClass(anomaly.severity)}>
            {t(`admin.anomaly_kind_${anomaly.kind}`)}
          </span>
          <span className="fx-aud-row-ip">{anomaly.ip}</span>
        </span>
        <span className="fx-aud-row-sub">
          {t('admin.anomaly_evidence', {
            accounts: anomaly.distinct_accounts,
            failures: anomaly.failures,
            minutes,
          })}
        </span>
        {anomaly.ip_trusted ? (
          <span className="fx-aud-row-sub">{t('admin.audit_ip_trusted')}</span>
        ) : (
          <span className="fx-anom-provenance">{t('admin.anomaly_proxy_warning')}</span>
        )}
      </div>
      <div className="fx-anom-actions">
        <button type="button" className="fx-pillbtn" onClick={() => onInspect(anomaly.ip)}>
          {t('admin.anomaly_inspect')}
        </button>
        {anomaly.blocked ? (
          <span className="fx-aud-tag fx-aud-tag-blocked">{t('admin.anomaly_blocked')}</span>
        ) : canBlock && blockable(anomaly.ip) ? (
          <button
            type="button"
            className="fx-pillbtn"
            disabled={busy}
            onClick={() => onBlock(anomaly.ip, reason)}
          >
            {t('admin.anomaly_block')}
          </button>
        ) : null}
      </div>
    </li>
  )
}
