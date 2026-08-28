import type {
  AuditDayBucket, AuditEntry, AuditSeverity, AuditWindow,
} from '../../api/admin'

/** The periods the server accepts. Anything else is a 400, not a wider page. */
export const AUDIT_WINDOWS: AuditWindow[] = ['24h', '7d', '30d']

/**
 * How a headline number compares to the preceding window of equal length.
 *
 * `tone` is the DIRECTION OF CONCERN, not the sign: more sign-ins is neutral,
 * more failed logins is bad, and fewer of them is good. Colouring by sign alone
 * would paint a drop in failures red, which is the opposite of what it means.
 */
export type Delta = { label: string; tone: 'good' | 'bad' | 'neutral' }

export function delta(now: number, prev: number, worseWhen: 'up' | 'down' | 'never'): Delta {
  const diff = now - prev
  if (diff === 0 || worseWhen === 'never') {
    return { label: diff === 0 ? '—' : signed(diff), tone: 'neutral' }
  }
  const worse = worseWhen === 'up' ? diff > 0 : diff < 0
  return { label: signed(diff), tone: worse ? 'bad' : 'good' }
}

function signed(n: number): string {
  return n > 0 ? `+${n}` : String(n)
}

/**
 * Percentage change, for the cards that read better as a rate than a count.
 *
 * A previous window of zero has no percentage — "+∞%" is not information — so
 * it falls back to the absolute number.
 */
export function deltaPercent(now: number, prev: number, worseWhen: 'up' | 'down' | 'never'): Delta {
  if (prev === 0) return delta(now, prev, worseWhen)
  const pct = Math.round(((now - prev) / prev) * 100)
  const base = delta(now, prev, worseWhen)
  return { ...base, label: pct > 0 ? `+${pct}%` : `${pct}%` }
}

/** One column of the stacked chart, in percentages of the tallest day. */
export type DayColumn = {
  day: string
  label: string
  total: number
  /** Percentage heights, one per series. */
  logins: number
  failed: number
  admin: number
  content: number
  /** The raw counts, for the tooltip. Carried rather than looked back up in
   *  the source array: a `find` keyed by the day would need a fallback for a
   *  row that cannot be missing, and an unreachable fallback next to a number
   *  reads as a case somebody considered. */
  counts: { logins: number; failed: number; admin: number; content: number }
}

/**
 * Scales the daily buckets to percentage heights.
 *
 * Percentages rather than pixels so the chart is responsive without measuring:
 * the track has the height, and each segment is a share of the tallest column.
 *
 * A day with events must never round to zero height — a bar that is there and
 * invisible is worse than one that is absent, because the axis label below it
 * says something happened. Hence the floor on any non-zero segment.
 */
export function dayColumns(days: AuditDayBucket[], locale: string): DayColumn[] {
  const totals = days.map((d) => d.logins + d.failed + d.admin + d.content)
  const max = Math.max(1, ...totals)
  return days.map((d, i) => ({
    day: d.day,
    label: dayLabel(d.day, locale),
    total: totals[i],
    logins: share(d.logins, max),
    failed: share(d.failed, max),
    admin: share(d.admin, max),
    content: share(d.content, max),
    counts: { logins: d.logins, failed: d.failed, admin: d.admin, content: d.content },
  }))
}

const MIN_VISIBLE_SHARE = 2

function share(n: number, max: number): number {
  if (n === 0) return 0
  return Math.max(MIN_VISIBLE_SHARE, Math.round((n / max) * 100))
}

/**
 * The axis label.
 *
 * Parsed as a date and formatted in the viewer's locale rather than sliced out
 * of the ISO string: the server sends a UTC instant, and "2026-08-27" read as
 * text would label a bar with a day the viewer never had.
 */
export function dayLabel(iso: string, locale: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleDateString(locale, { day: '2-digit', month: '2-digit' })
}

/** A distribution bar's width, as a share of the largest slice. */
export function distributionWidth(count: number, max: number): number {
  if (max <= 0) return 0
  return Math.max(MIN_VISIBLE_SHARE, Math.round((count / max) * 100))
}

/** The class suffix a severity renders under. */
export function severityClass(severity: AuditSeverity): string {
  return `fx-aud-sev fx-aud-sev-${severity}`
}

/**
 * Who an entry names.
 *
 * A content row has no e-mail by construction — the server withholds it — and
 * carries an opaque account id instead. Returning a discriminated shape rather
 * than a formatted string keeps the decision here and lets the component render
 * the pseudonym through i18n, where the word "usuário" belongs.
 */
export type Actor =
  | { kind: 'email'; email: string }
  | { kind: 'ref'; ref: number }
  | { kind: 'none' }

export function actorOf(entry: AuditEntry): Actor {
  if (entry.actor_email) return { kind: 'email', email: entry.actor_email }
  if (entry.actor_ref != null) return { kind: 'ref', ref: entry.actor_ref }
  return { kind: 'none' }
}

/**
 * Whether an address may be offered for blocking.
 *
 * Affordance, not enforcement, and it mirrors only the ONE rail a browser can
 * actually evaluate: loopback is recognisable from the string. The other three
 * are the server's — it cannot know the trusted-proxy set, and it does not know
 * its own public address (the address the instance SEES is the one behind any
 * NAT and proxy in between, which is exactly why that rail lives where the
 * request arrives). Those refusals come back with their own code and are
 * rendered verbatim.
 */
export function blockable(ip: string | null): boolean {
  if (!ip) return false
  return !isLoopback(ip)
}

function isLoopback(ip: string): boolean {
  return ip === '::1' || ip === '0.0.0.0' || ip === '::' || ip.startsWith('127.')
}
