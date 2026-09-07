import { describe, expect, it } from 'vitest'
import en from './locales/en.json'
import pt from './locales/pt.json'
import es from './locales/es.json'

/**
 * Locale parity.
 *
 * en.json is the source of truth (CLAUDE.md §1). Until now nothing enforced
 * that pt and es kept up, and a missing key does not throw — i18next silently
 * falls back to the raw key, so a Portuguese user just sees `auth_login.title`
 * where a heading should be. The auth work grows the string surface by ~25%,
 * which is exactly when that drift starts happening unnoticed.
 */

type Tree = Record<string, unknown>

function flatten(obj: Tree, prefix = ''): string[] {
  return Object.entries(obj).flatMap(([k, v]) => {
    const path = prefix ? `${prefix}.${k}` : k
    return v && typeof v === 'object' && !Array.isArray(v)
      ? flatten(v as Tree, path)
      : [path]
  })
}

function flattenEntries(obj: Tree, prefix = ''): Array<[string, unknown]> {
  return Object.entries(obj).flatMap(([k, v]) => {
    const path = prefix ? `${prefix}.${k}` : k
    return v && typeof v === 'object' && !Array.isArray(v)
      ? flattenEntries(v as Tree, path)
      : [[path, v] as [string, unknown]]
  })
}

const enKeys = flatten(en as Tree)

describe('locale parity', () => {
  it.each([
    ['pt', pt],
    ['es', es],
  ])('%s has every key en has', (name, locale) => {
    const keys = new Set(flatten(locale as Tree))
    const missing = enKeys.filter((k) => !keys.has(k))
    expect(missing, `${name}.json is missing ${missing.length} key(s)`).toEqual([])
  })

  it.each([
    ['pt', pt],
    ['es', es],
  ])('%s has no keys en lacks', (name, locale) => {
    const enSet = new Set(enKeys)
    const extra = flatten(locale as Tree).filter((k) => !enSet.has(k))
    // An extra key is dead weight at best and, more often, a rename that landed
    // in one locale and not the source.
    expect(extra, `${name}.json has ${extra.length} key(s) en does not`).toEqual([])
  })

  it.each([
    ['en', en],
    ['pt', pt],
    ['es', es],
  ])('%s has no empty strings', (name, locale) => {
    const empties: string[] = []
    const walk = (obj: Tree, prefix = '') => {
      Object.entries(obj).forEach(([k, v]) => {
        const path = prefix ? `${prefix}.${k}` : k
        if (typeof v === 'string' && v.trim() === '') empties.push(path)
        else if (v && typeof v === 'object') walk(v as Tree, path)
      })
    }
    walk(locale as Tree)
    expect(empties, `${name}.json has blank values`).toEqual([])
  })

  // i18next v26 uses _one/_other. A legacy _plural suffix silently never
  // matches, so the interpolated count renders against the singular form.
  it('uses _one/_other and never the legacy _plural suffix', () => {
    expect(enKeys.filter((k) => k.endsWith('_plural'))).toEqual([])
  })

  // {{count}} keys WITHOUT _one/_other render "1 clicks" at count=1
  // (BUG-ART-105: clicks_count shipped that way in all three locales, and
  // nothing noticed). Exempted are the number-as-glyph labels — durations
  // ("3m ago"), deltas ("−2d"), "+N" chips — where the plural forms would
  // be byte-identical and carrying them is noise, not information.
  const COUNT_GLYPH_KEYS = new Set([
    'sidebar.load_more',
    'link_card.last_click_minutes',
    'link_card.last_click_hours',
    'link_card.last_click_days',
    'folder_card.more_overlay',
    'backup.history_title',
    'backup.summary_files_value',
    'stats.kpi_links_new_30d',
    'stats.section_clicks_day_sub',
    'stats.chart_days_ago',
  ])

  it.each([
    ['en', en],
    ['pt', pt],
    ['es', es],
  ])('%s pluralizes every count-bearing key', (name, locale) => {
    const entries = flattenEntries(locale as Tree)
    const paths = new Set(entries.map(([path]) => path))
    const pluralSuffix = /_(one|other|zero|two|few|many)$/
    const missing: string[] = []
    for (const [path, value] of entries) {
      if (typeof value !== 'string' || !value.includes('{{count}}')) continue
      if (pluralSuffix.test(path) || COUNT_GLYPH_KEYS.has(path)) continue
      if (!paths.has(`${path}_one`) || !paths.has(`${path}_other`)) missing.push(path)
    }
    expect(missing, `${name}.json count keys lacking _one/_other`).toEqual([])
  })
})
