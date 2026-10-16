import type { TFunction } from 'i18next'
import { actionCategory, actionLabel } from '../../lib/auditLabels'
import type { AuditEntry } from '../../api/admin'

/** Days in the activity overview window. */
export const CHART_DAYS = 14

export type ActivityDayBucket = { key: string; label: string; count: number }

/** Local calendar day (YYYY-MM-DD) for an ISO timestamp. */
export function dayKey(iso: string): string {
  return formatLocalYMD(new Date(iso))
}

export function formatLocalYMD(d: Date): string {
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${m}-${day}`
}

/** Per-day counts over the trailing CHART_DAYS window, oldest first. */
export function buildActivityHistogram(
  entries: AuditEntry[],
  locale: string | undefined,
  now = new Date(),
): ActivityDayBucket[] {
  const days: ActivityDayBucket[] = []
  const fmtDay = new Intl.DateTimeFormat(locale, { day: 'numeric' })
  for (let i = CHART_DAYS - 1; i >= 0; i--) {
    const d = new Date(now)
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
}

export type ActivityDayGroup = {
  key: string
  recent: boolean
  label: string
  items: { id: number; time: string; label: string; dot: string; title: string; ip: string }[]
}

function dotClass(action: string): string {
  return `fx-acc2-act-dot-${actionCategory(action)}`
}

const OPEN_DAYS = 3

/** True when `key` (YYYY-MM-DD) is today or one of the two days before it. */
function dayIsRecent(key: string): boolean {
  const day = new Date(`${key}T12:00:00`)
  const today = new Date()
  today.setHours(12, 0, 0, 0)
  const diff = Math.round((today.getTime() - day.getTime()) / 86_400_000)
  return diff >= 0 && diff < OPEN_DAYS
}

/** Entries grouped by calendar day, newest first, display-ready. */
export function groupActivityEntriesByDay(
  entries: AuditEntry[],
  locale: string | undefined,
  t: TFunction,
): ActivityDayGroup[] {
  const map = new Map<string, AuditEntry[]>()
  for (const e of entries) {
    const k = dayKey(e.created_at)
    const list = map.get(k)
    if (list) list.push(e)
    else map.set(k, [e])
  }
  const fmtDay = new Intl.DateTimeFormat(locale, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
  })
  const fmtTime = new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' })
  return [...map.entries()]
    .sort((a, b) => (a[0] < b[0] ? 1 : -1))
    .map(([key, items]) => ({
      key,
      recent: dayIsRecent(key),
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
}
