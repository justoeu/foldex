#!/usr/bin/env node
/**
 * `.fx-auth` is an OVERLAY, and only the signed-out screens may wear it.
 *
 * auth.css declares it `position: fixed; inset: 0` with an opaque background.
 * Everything it styles that the signed-in shell also needs — the OTP field, the
 * QR, the recovery-code grid — is scoped `.fx-auth .thing`, so reaching those
 * once meant putting the overlay class on a div inside a card and cancelling
 * `position`/`padding`/`background` by hand.
 *
 * That cancellation held only while it was an INLINE style. Written as a class
 * in foldex.css it lost to `.fx-auth` on document order — auth.css loads last
 * and both are `(0,1,0)` — and the wrapper became a full-size opaque sheet
 * painted over the card: every label, heading and row background gone, the
 * layout untouched underneath, and a repaint storm that blocked the main
 * thread. It shipped, and the report was "the screen flickers and never
 * loads". `.fx-authfield` carries the same tokens and answers the same
 * descendant selectors with no layout of its own.
 *
 * No unit test can see the CSS half of this (`vitest.config.ts` sets
 * `css: false`), but the class name IS in the DOM — `TwoFactorSection.test.tsx`
 * asserts on it too. This script covers the whole tree at once, and runs in CI
 * beside scripts/test-css-button-reset.mjs.
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(fileURLToPath(new URL('.', import.meta.url)), '..')
const SRC = join(ROOT, 'web', 'src')

/** The two full-screen surfaces. Both ARE the overlay; both are signed-out. */
const ALLOWED = new Set(['components/auth/AuthShell.tsx', 'auth/AuthGate.tsx'])

/** `className="fx-auth"` or `"fx-auth ..."` — never the `fx-auth-*` children. */
const OVERLAY = /\bfx-auth(?![\w-])/

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) yield* walk(path)
    else if (/\.tsx$/.test(name)) yield path
  }
}

const failures = []
for (const path of walk(SRC)) {
  const rel = relative(SRC, path).split('\\').join('/')
  if (ALLOWED.has(rel)) continue
  readFileSync(path, 'utf8').split('\n').forEach((line, i) => {
    // Only class attributes: prose in a comment naming the class is fine, and
    // the CSS itself is out of scope here.
    for (const m of line.matchAll(/className=(?:"([^"]*)"|\{`([^`]*)`\})/g)) {
      const value = m[1] ?? m[2] ?? ''
      if (OVERLAY.test(value)) {
        failures.push(`${rel}:${i + 1}  className="${value.trim()}"`)
      }
    }
  })
}

if (failures.length) {
  console.error('FAIL: the .fx-auth OVERLAY is used inside the signed-in shell.')
  console.error('It is `position: fixed; inset: 0` with an opaque background —')
  console.error('it will paint over whatever card contains it. Use .fx-authfield,')
  console.error('which carries the same tokens and matches the same selectors.\n')
  for (const f of failures) console.error('  ' + f)
  process.exit(1)
}

// The tokens must reach `.fx-authfield`, or every cell it styles renders with
// unresolved `--fxa-*` custom properties and no border at all.
const css = readFileSync(join(SRC, 'styles', 'auth.css'), 'utf8')
if (!/\.fx-authfield\s*\{[^}]*--fxa-border:/s.test(css.replace(/\/\*[\s\S]*?\*\//g, ''))) {
  console.error('FAIL: .fx-authfield does not declare the --fxa-* token aliases.')
  console.error('Without them the OTP cells and recovery grid lose their borders.')
  process.exit(1)
}
if (!/:is\(\.fx-auth, \.fx-authfield\) \.fx-auth-otp-cell/.test(css)) {
  console.error('FAIL: .fx-auth-otp-cell no longer answers to .fx-authfield.')
  console.error('Every wrapper inside the shell uses that class; unscoping it')
  console.error('leaves the OTP field unstyled wherever it is not the overlay.')
  process.exit(1)
}

console.log('ok: the .fx-auth overlay stays on the signed-out screens')
