#!/usr/bin/env node
/*
 * `.fx-shell button` (web/src/styles/foldex.css) resets border, background,
 * padding, colour and font on EVERY button in the signed-in shell. It is
 * (0,1,1), so it out-specifies a bare component class (0,1,0): a
 * `<button className="fx-thing">` whose rule is written `.fx-thing { … }`
 * silently renders with none of its own background or padding.
 *
 * That is not theoretical — it shipped. The settings hub's scope segment and
 * the topbar's view segment rendered their ACTIVE option byte-for-byte
 * identical to the inactive one (no pill, no accent, no padding), because
 * `.fx-hub-seg-btn-active` and `.fx-vs-active` were bare. Showing which half
 * you are on is the one thing a segmented control exists to do.
 *
 * No unit test can see this. The markup was always correct — class applied,
 * aria-pressed set — jsdom loads no stylesheet, and `vitest.config.ts` sets
 * `css: false`, which resolves a `?raw` CSS import to the EMPTY STRING. A guard
 * written in Vitest therefore passes while asserting nothing; that was tried
 * first and it went green against the broken file. Same reason
 * test-nginx-headers.sh boots nginx rather than reading the config: the cascade
 * decides the outcome, not the source text.
 *
 * The list below is EXPLICIT, and that is a real limitation stated rather than
 * hidden: it pins the classes that have been checked, not every button class in
 * the tree. Deriving it automatically was tried — 92 classes land on a
 * `<button>`, 56 of them have a bare rule setting a reset property, and most of
 * those are fine because a LATER chained rule (usually in overrides.css) wins.
 * Separating the two needs a real cascade solver; a guard that reported all 56
 * would be red on day one and read as noise. Adding the tree-wide version is a
 * follow-up. What this catches today is a regression on the classes named here,
 * which is where the bug actually shipped.
 */
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const HERE = dirname(fileURLToPath(import.meta.url))
// `--self-test` runs the guard against two fixtures instead of the real
// stylesheets. Its own history is the reason: the first version of this check
// was a Vitest test that went green against the BROKEN file, so a guard whose
// failure path is never exercised is exactly the thing this repo has been
// burned by. `bad.css` must exit 1 and `good.css` must exit 0.
const SELF_TEST = process.argv.includes('--self-test')
const STYLES = SELF_TEST ? join(HERE, 'fixtures') : join(HERE, '..', 'web', 'src', 'styles')
// auth.css is read too: `.fx-auth-locale` lives there, and a guard that only
// knew about foldex.css would silently stop covering a class the day it moved.
const FILES = SELF_TEST
  ? [process.argv.includes('--good') ? 'button-reset-good.css' : 'button-reset-bad.css']
  : ['foldex.css', 'overrides.css', 'auth.css']
// Shorthands, matched together with their longhands: the reset sets
// `background`, so a bare `background-color` loses to it just the same, and an
// earlier version of this guard passed every longhand mutation.
const RESET_PROPS = ['background', 'color', 'padding', 'border']
const GUARDED = [
  'fx-aud-window',
  'fx-aud-window-on',
  'fx-aud-chip',
  'fx-aud-chip-on',
  'fx-aud-event-row',
  'fx-aud-event-open',
  'fx-aud-risk-cta',
  'fx-hub-seg-btn',
  'fx-hub-seg-btn-active',
  'fx-vs',
  'fx-vs-active',
  'fx-rowact',
  'fx-rowact-danger',
  'fx-pweye',
  'fx-auth-locale',
  'fx-btn',
  'fx-btn-primary',
  'fx-btn-danger',
  'fx-acard',
  'fx-acct-railbtn',
  'fx-acct-railbtn-active',
  'fx-bkp-tab',
  'fx-bkp-tab-on',
  'fx-bkp-mode',
  'fx-bkp-mode-on',
  'fx-bkp-dayset',
  'fx-bkp-day',
  'fx-bkp-day-on',
  'fx-bkp-preset',
  'fx-bkp-preset-on',
  'fx-bkp-filter',
  'fx-bkp-filter-on',
  'fx-bkp-job-run',
  'fx-bkp-add',
  'fx-bkp-job-head',
  'fx-bkp-sha-btn',
  'fx-bkp-time-remove',
  'fx-abuse-reset',
]

const css = FILES.map((f) => readFileSync(join(STYLES, f), 'utf8')).join('\n').replace(
  /\/\*[\s\S]*?\*\//g,
  '',
)
const failures = []

// The reset must still exist and still set these properties, or the guard is
// false reassurance rather than a test.
const reset = css.match(/\.fx-shell button\s*\{([^}]*)\}/)
if (!reset) {
  console.error('FAIL: .fx-shell button reset not found — re-scope or delete this guard')
  process.exit(1)
}
for (const p of RESET_PROPS) {
  if (!new RegExp(`(^|[;{\\s])${p}\\s*:`).test(reset[1])) {
    failures.push(`the reset no longer sets '${p}'; re-scope or delete this guard`)
  }
}

/**
 * Specificity of one selector part, as [ids, classes, elements], compared
 * against the reset's (0,1,1).
 *
 * `:where()` contributes nothing — including its contents — so it is dropped
 * whole. `:is()`/`:not()` contribute their most specific argument, approximated
 * here as one class when any argument names a class. Attribute selectors and
 * pseudo-classes count as classes; pseudo-elements count as elements.
 */
function atLeastResetSpecificity(part) {
  let s = part.trim()
  s = s.replace(/:where\([^)]*\)/g, ' ')
  let classes = 0
  s = s.replace(/:(?:is|not|has)\(([^)]*)\)/g, (_, args) => {
    classes += /[.#[]/.test(args) ? 1 : 0
    return ' '
  })
  const ids = (s.match(/#[\w-]+/g) || []).length
  classes += (s.match(/\.[\w-]+/g) || []).length
  classes += (s.match(/\[[^\]]*\]/g) || []).length
  classes += (s.match(/(?<!:):[a-z-]+(?:\([^)]*\))?/gi) || []).length
  const elements =
    (s.match(/(?:^|[\s>+~])([a-z][\w-]*)/gi) || []).length + (s.match(/::[a-z-]+/gi) || []).length
  // [ids, classes, elements] >= [0, 1, 1]
  if (ids > 0) return true
  if (classes > 1) return true
  return classes === 1 && elements >= 1
}

/** Split a selector list on top-level commas — `:is(a, b)` is ONE part. */
function splitSelector(sel) {
  const parts = []
  let depth = 0
  let buf = ''
  for (const c of sel) {
    if (c === '(') depth++
    else if (c === ')') depth--
    if (c === ',' && depth === 0) {
      parts.push(buf)
      buf = ''
    } else buf += c
  }
  if (buf.trim()) parts.push(buf)
  return parts.map((p) => p.trim())
}

for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
  const [, sel, body] = m
  if (sel.trim().startsWith('@')) continue
  const declared = body
    .split(';')
    .map((d) => d.split(':')[0]?.trim())
    .filter(Boolean)
  const setsReset = declared.some((d) =>
    RESET_PROPS.some((p) => d === p || d.startsWith(p + '-')),
  )
  if (!setsReset) continue

  for (const part of splitSelector(sel)) {
    const named = GUARDED.some((c) => new RegExp(`\\.${c}(?![\\w-])`).test(part))
    if (!named) continue
    // The rule must reach at least (0,1,1) to beat the reset. Ties win on
    // document order, because every guarded rule sits below it in the file.
    //
    // A boolean "does it look chained?" was tried and was wrong in both
    // directions: it passed `:is(.fx-vs-active)` (which is (0,1,0) and loses)
    // and `:where(…)` (which is (0,0,0) and loses hard), while failing
    // `.fx-vs-active[data-on]` (which is (0,2,0) and wins) — a false CI break
    // on correct code. Counting is the only thing that answers this.
    if (!atLeastResetSpecificity(part)) {
      failures.push(`'${part.trim()}' sets a reset property but is (0,1,0) — it loses to '.fx-shell button'`)
    }
  }
}

if (failures.length) {
  for (const f of failures) console.error('FAIL: ' + f)
  process.exit(1)
}
console.log('ok: guarded segment classes are all chained past the button reset')
