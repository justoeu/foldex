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
})
