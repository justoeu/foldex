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
