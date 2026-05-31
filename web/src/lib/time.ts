// Light-weight relative time formatter. The Topbar's clock-icon tooltips
// and the "update detected ago" badge both want short copy like "2h ago" /
// "há 3d" / "hace 5 sem", so a small helper hand-rolled against the
// i18n key set we already maintain beats pulling in date-fns.
export function relativeTime(
  isoOrDate: string | Date,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  const then = typeof isoOrDate === 'string' ? new Date(isoOrDate) : isoOrDate
  if (isNaN(then.getTime())) return ''
  const diffMs = Date.now() - then.getTime()
  // Future dates collapse to "now" — keeps the UI readable when a server
  // clock skew makes the stamp land slightly ahead.
  const sec = Math.max(0, Math.floor(diffMs / 1000))
  if (sec < 45) return t('common.time_now', { defaultValue: 'now' })
  const min = Math.floor(sec / 60)
  if (min < 60) return t('common.time_min_ago', { count: min, defaultValue: '{{count}}m ago' })
  const hr = Math.floor(min / 60)
  if (hr < 24) return t('common.time_hr_ago', { count: hr, defaultValue: '{{count}}h ago' })
  const day = Math.floor(hr / 24)
  if (day < 7) return t('common.time_day_ago', { count: day, defaultValue: '{{count}}d ago' })
  const wk = Math.floor(day / 7)
  if (wk < 5) return t('common.time_wk_ago', { count: wk, defaultValue: '{{count}}w ago' })
  // For older dates just show the date itself — relative time loses meaning
  // past a few weeks and the user usually wants the absolute marker anyway.
  return then.toISOString().slice(0, 10)
}

export type CheckInterval = 'hourly' | 'daily' | 'weekly'

const intervalMs: Record<CheckInterval, number> = {
  hourly: 60 * 60 * 1000,
  daily: 24 * 60 * 60 * 1000,
  weekly: 7 * 24 * 60 * 60 * 1000,
}

// Forecast when the next changecheck scan will pick the link up. Mirrors
// `FindDueForCheck` in backend/internal/links/repository.go: due iff
// `last_checked_at IS NULL OR last_checked_at < now() - interval`. The
// backend predicate is strict `<`, so a link is due at `last + interval`.
// We round to a 60 s "soon" bucket to absorb the scan tick (also default
// 60 s) — telling the user "in 30 s" is noisier than truthful given the
// scheduler's resolution.
//
// `link` is the dialog's snapshot; PATCH bumps `last_checked_at` on the
// backend but the dialog closes on save, so the preview is always sourced
// from the snapshot the user opened.
export function nextCheckPreview(
  interval: CheckInterval | null | undefined,
  lastCheckedAt: string | null | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string,
  now: Date = new Date(),
): string {
  if (!interval) return ''
  // Defensive guard: TS callers can't reach this, but a stale call site
  // passing a string outside the union would otherwise produce "in NaNd".
  const step = intervalMs[interval]
  if (!step) return ''
  if (!lastCheckedAt) return t('common.next_check_soon', { defaultValue: 'soon (within a minute)' })
  const last = new Date(lastCheckedAt)
  if (isNaN(last.getTime())) return t('common.next_check_soon', { defaultValue: 'soon (within a minute)' })
  const nextMs = last.getTime() + step
  const remaining = nextMs - now.getTime()
  if (remaining <= 60_000) return t('common.next_check_soon', { defaultValue: 'soon (within a minute)' })
  const min = Math.floor(remaining / 60_000)
  if (min < 60) return t('common.next_check_in_min', { count: min, defaultValue: 'in {{count}}m' })
  const hr = Math.floor(min / 60)
  if (hr < 24) return t('common.next_check_in_hr', { count: hr, defaultValue: 'in {{count}}h' })
  const day = Math.floor(hr / 24)
  // Clock skew / hostile timestamp could land last_checked_at in the
  // far future and produce "in 3650000d". Past a year we fall back to
  // an ISO date stamp — same shape `relativeTime` uses for ancient values.
  if (day > 365) return new Date(nextMs).toISOString().slice(0, 10)
  return t('common.next_check_in_day', { count: day, defaultValue: 'in {{count}}d' })
}
