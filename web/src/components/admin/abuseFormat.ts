import type { AbuseBound, AbuseObserved, AbusePolicy, Anomaly, AnomalyKind } from '../../api/admin'
import { severityClass } from './auditFormat'

/** One knob, named exactly as the server names it in `bounds` and in a 400. */
export type AbuseField = keyof AbusePolicy

/**
 * One band of the form.
 *
 * `enforces` is the field the whole screen turns on: six of these numbers
 * REFUSE requests and three only decide what the anomalies panel calls
 * anomalous. Encoding the difference here — rather than leaving it to a
 * paragraph someone may not read — is what lets the band render its own
 * disclaimer and what a test can assert.
 */
export type AbuseBand = {
  id: 'login' | 'api' | 'public' | 'detection'
  fields: AbuseField[]
  enforces: boolean
}

/**
 * The one knob whose zero is a MEANING rather than an absence.
 *
 * Named because three places have to agree about it — the null-resolving seed,
 * the "0 turns it off" hint, and the bound lookup behind both — and three bare
 * copies of the same string are three places that can be edited apart.
 */
export const COALESCE_FIELD: AbuseField = 'public_click_coalesce_seconds'

export const ABUSE_BANDS: AbuseBand[] = [
  {
    id: 'login',
    fields: [
      'login_distinct_accounts_per_ip',
      'login_failures_per_account',
      'login_window_minutes',
    ],
    enforces: true,
  },
  {
    id: 'api',
    fields: ['api_writes_per_minute', 'api_expensive_per_hour'],
    enforces: true,
  },
  {
    id: 'public',
    fields: ['public_click_coalesce_seconds'],
    enforces: true,
  },
  {
    id: 'detection',
    fields: ['anomaly_spray_accounts', 'anomaly_hammer_failures', 'anomaly_window_minutes'],
    enforces: false,
  },
]

/**
 * The advertised range for one knob, looked up BY NAME.
 *
 * By name and never by position: `abusepolicy.Bounds()` appends the nullable
 * coalesce knob after the loop, so the array's order is not the form's order
 * and an index would silently pair a field with another field's limits.
 *
 * A knob the payload said nothing about answers null, and the caller renders
 * the input without a range rather than dropping it — a field that disappears
 * because an aggregate went missing reads as a broken screen.
 */
export function boundOf(bounds: AbuseBound[], field: string): AbuseBound | null {
  return bounds.find((b) => b.field === field) ?? null
}

/**
 * What this instance actually saw for the knob being typed.
 *
 * Three of the nine have a measurement; the rest answer null and render
 * nothing, because "no measurement exists for this knob" and "the measurement
 * is empty" are different statements and only the second is news.
 *
 * A ZERO is returned as zero, not as null: it means nothing was observed, and
 * the caller says so in words. Printing the digit would read as a measured
 * peak of zero and invite tuning the limit down to meet it.
 */
export function observedFor(field: AbuseField, observed?: AbuseObserved): number | null {
  switch (field) {
    case 'login_distinct_accounts_per_ip':
      return observed?.max_distinct_accounts_per_ip ?? 0
    case 'login_failures_per_account':
      return observed?.max_failures_per_account ?? 0
    case 'api_writes_per_minute':
      return observed?.peak_writes_per_minute ?? 0
    default:
      return null
  }
}

/**
 * Restores one band to the defaults the SERVER advertised.
 *
 * Per band rather than per document, because the bands are separate decisions:
 * an owner who widened the login budget on purpose should be able to undo a
 * botched API number without losing it. A knob whose bound is missing is left
 * exactly as it was — there is no default to restore it to, and inventing one
 * would be the second copy this screen exists to avoid.
 */
export function restoreBandDefaults(
  policy: AbusePolicy,
  bounds: AbuseBound[],
  band: AbuseBand,
): AbusePolicy {
  // Spread with a computed key rather than an indexed write: assigning through
  // a union-typed key needs a cast, and a cast here would silence exactly the
  // check that says a knob still holds a number.
  return band.fields.reduce<AbusePolicy>((acc, field) => {
    const bound = boundOf(bounds, field)
    return bound ? { ...acc, [field]: bound.default } : acc
  }, policy)
}

/**
 * The badge class for an anomaly's severity.
 *
 * The anomaly contract says `warn`; the trail's stylesheet says `warning`, and
 * `.fx-aud-sev-warn` does not exist. Passing the value straight through would
 * ask for a class nothing declares and render an unstyled badge — the exact
 * defect INV-159 exists for, and one no type error catches because both are
 * strings.
 */
export function anomalySeverityClass(severity: 'critical' | 'warn'): string {
  return severityClass(severity === 'warn' ? 'warning' : 'critical')
}

/**
 * Severity first, most recent first inside a severity.
 *
 * A copy, never the argument: the array comes from the query cache, and
 * sorting it in place would reorder what React Query hands the next render
 * without a state change to explain it.
 */
export function sortAnomalies(list: Anomaly[]): Anomaly[] {
  const rank = (a: Anomaly) => (a.severity === 'critical' ? 0 : 1)
  return [...list].sort(
    (a, b) => rank(a) - rank(b) || Date.parse(b.last_seen) - Date.parse(a.last_seen),
  )
}

/**
 * How long the signal lasted, in whole minutes, for the evidence line.
 *
 * Clamped at zero and NaN-guarded on purpose: a clock skew between the rows
 * would otherwise print "-3 min", and an unparseable timestamp "NaN min".
 * Both read as a broken panel rather than as the data problem they are.
 */
export function spanMinutes(a: { first_seen: string; last_seen: string }): number {
  const ms = Date.parse(a.last_seen) - Date.parse(a.first_seen)
  if (!Number.isFinite(ms) || ms <= 0) return 0
  return Math.round(ms / 60_000)
}

/**
 * The i18n key for the reason a block is filed under.
 *
 * DERIVED from the signal rather than typed, exactly as the audit screen's
 * origin and burst blocks are: the operator is looking at the evidence when
 * they press the button, and a free-text field yields either that same
 * sentence or an empty string.
 */
export function blockReasonKey(kind: AnomalyKind): string {
  return `admin.anomaly_block_reason_${kind}`
}
