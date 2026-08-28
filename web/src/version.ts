import pkg from '../package.json'

declare const __FOLDEX_BUILD_DATE__: string

export const VERSION: string = pkg.version

// Vite injects the production date; tests and development use a stable fallback.
export const BUILD_DATE: string =
  typeof __FOLDEX_BUILD_DATE__ !== 'undefined' ? __FOLDEX_BUILD_DATE__ : 'dev'

/**
 * ISO `YYYY-MM-DD` → `DD/MM/YYYY`. Parsed with explicit Y/M/D rather than
 * `new Date(iso)`, which reads the string as UTC midnight and shifts the day
 * back for anyone west of Greenwich.
 *
 * Shared so the sidebar and the signed-out screen cannot disagree about the
 * same build.
 */
export function formatBuildDate(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso)
  if (!m) return iso
  return `${m[3]}/${m[2]}/${m[1]}`
}
