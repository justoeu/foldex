#!/usr/bin/env node
/**
 * Every `fx-*` class in the markup must have a rule in some stylesheet.
 *
 * This defect has now shipped SIX times, always the same way and always
 * invisible: `.fx-btn` and `.fx-btn-primary` (fourteen buttons rendering as
 * bare text), `.fx-inline-error` (four credential refusals as unstyled prose),
 * `.fx-btn-danger` (both "remove a second factor" buttons identical to the
 * neutral one beside them), and `.fx-acct-row .fx-auth-oauth`, which stopped
 * matching when the row class was renamed. Nothing fails when it happens:
 * TypeScript sees a string, jsdom loads no stylesheet, and `vitest.config.ts`
 * sets `css: false`, so a Vitest guard would read the file as the empty string.
 * Only a person looking at the screen notices, and only if they happen to know
 * what it should look like.
 *
 * Runs in CI beside test-css-button-reset.mjs and test-css-auth-overlay.mjs.
 *
 * The check is deliberately one-directional: a class in the CSS that nothing
 * uses is dead weight, but a class in the MARKUP that nothing styles is a
 * broken screen. Only the second is an error here.
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(fileURLToPath(new URL('.', import.meta.url)), '..')
const SRC = join(ROOT, 'web', 'src')
const STYLES = join(SRC, 'styles')
const SHEETS = ['foldex.css', 'overrides.css', 'auth.css']

/**
 * Orphans that PREDATE this guard, kept so it can be turned on at all.
 *
 * Every one is a real defect of the same family — `.fx-error` on the instance
 * policy form is a save failure rendered as unstyled prose, `.fx-chart-tooltip`
 * is a tooltip with no background over a chart — but they belong to screens
 * nobody was changing when the guard was written, and fixing six unrelated
 * surfaces to land a guard is how a guard ends up reverted.
 *
 * This list may only ever SHRINK. A new entry means the guard stopped guarding.
 */
const KNOWN_ORPHANS = new Set([
  'fx-confirm-msg',        // backup restore + import preview: partially inline-styled
  'fx-preview-img',
  'fx-segment-icon',
  'fx-hint',               // admin policy: renders at default paragraph size
  'fx-error',              // admin policy: a refused save, as plain text
  'fx-spark',
  'fx-chart-tooltip',
  'fx-chart-tooltip-date',
  'fx-chart-tooltip-value',
])

/**
 * Classes assembled at runtime from a value this script cannot resolve, listed
 * as PREFIXES. Each entry is a promise that the variants exist; keep it as
 * short as the code allows, because everything on it is unguarded.
 */
const DYNAMIC_PREFIXES = [
  'fx-sec-badge-',   // tone: on | off | warn
  'fx-sec-note-',    // tone: ok | bad | info
  'fx-sec-row-',     // tone: on | warn | danger  (plus the static row parts)
]

const css = SHEETS.map((f) => readFileSync(join(STYLES, f), 'utf8'))
  .join('\n')
  // A class named only inside a comment is not styled by it.
  .replace(/\/\*[\s\S]*?\*\//g, '')

const declared = new Set()
for (const m of css.matchAll(/\.(fx-[\w-]+)/g)) declared.add(m[1])

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) yield* walk(path)
    else if (/\.tsx$/.test(name) && !/\.test\.tsx$/.test(name)) yield path
  }
}

/**
 * Pulls the class tokens out of one className value.
 *
 * Chunks touching a `${…}` are dropped: `fx-sec-row-${tone}` yields a
 * `fx-sec-row-` fragment that is not a class anyone wrote, and reporting it
 * would train people to add entries to DYNAMIC_PREFIXES until the guard means
 * nothing.
 */
function classesIn(value) {
  const out = []
  for (const part of value.split(/\$\{[^}]*\}/g).entries()) {
    const [index, chunk] = part
    const pieces = chunk.split(/\s+/).filter(Boolean)
    // The first piece of any chunk after the first, and the last piece of any
    // chunk before the last, are adjacent to an interpolation.
    const cut = value.split(/\$\{[^}]*\}/g).length
    if (index > 0 && !/^\s/.test(chunk)) pieces.shift()
    if (index < cut - 1 && !/\s$/.test(chunk)) pieces.pop()
    out.push(...pieces)
  }
  return out.filter((c) => c.startsWith('fx-'))
}

const failures = []
for (const path of walk(SRC)) {
  const rel = relative(SRC, path).split('\\').join('/')
  readFileSync(path, 'utf8').split('\n').forEach((line, i) => {
    for (const m of line.matchAll(/className=(?:"([^"]*)"|\{`([^`]*)`\}|\{'([^']*)'\})/g)) {
      for (const cls of classesIn(m[1] ?? m[2] ?? m[3] ?? '')) {
        if (declared.has(cls)) continue
        if (KNOWN_ORPHANS.has(cls)) continue
        if (DYNAMIC_PREFIXES.some((p) => cls.startsWith(p))) continue
        failures.push(`${rel}:${i + 1}  .${cls}`)
      }
    }
  })
}

if (failures.length) {
  console.error('FAIL: these classes are in the markup and in no stylesheet.')
  console.error('They render as unstyled elements — and because `.fx-shell button`')
  console.error('and `.fx-shell input` strip border/background/padding, an unstyled')
  console.error('control is usually invisible rather than merely plain.\n')
  for (const f of [...new Set(failures)]) console.error('  ' + f)
  process.exit(1)
}

console.log(`ok: every fx-* class in the markup has a rule (${declared.size} declared)`)
