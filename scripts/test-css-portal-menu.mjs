#!/usr/bin/env node
/**
 * A portaled menu must re-establish the shell's context.
 *
 * `.fx-shell` (web/src/styles/foldex.css) is what gives the product its look:
 * `box-sizing: border-box` on every descendant, the UI font, the 14px/600 type
 * scale, and `.fx-shell button { border: 0; background: transparent; ... }`
 * which strips native button chrome. Anything rendered through `createPortal`
 * to <body> lands OUTSIDE that element, so none of it applies — and a menu
 * whose CSS was written assuming it does renders with the UA's button border,
 * background and typeface, and with content-box items whose `width: 100%` plus
 * padding overflow their own container.
 *
 * That is not hypothetical. It is what the topbar user menu shipped as: bordered
 * boxes in the wrong font, spilling past the dropdown's edge, on a screen where
 * every other control was correct. Nothing in the tree can see it — TypeScript
 * sees a string, jsdom loads no stylesheet, and `css: false` in vitest.config.ts
 * resolves a `?raw` CSS import to the empty string — only a person looking at
 * the screen.
 *
 * The rule: a `createPortal` call whose subtree contains a <button> must give
 * its root element the `fx-portalmenu` class. A portal with no button (a
 * tooltip, a read-only popover) is exempt: it carries no control that the
 * missing reset could disfigure.
 *
 *   node scripts/test-css-portal-menu.mjs              # check the tree
 *   node scripts/test-css-portal-menu.mjs --self-test  # prove the guard bites
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = new URL('..', import.meta.url).pathname
const SRC = join(ROOT, 'web/src')
const CSS = join(ROOT, 'web/src/styles/foldex.css')
const CLASS = 'fx-portalmenu'

/** Every `createPortal(` call's argument text, one entry per call. */
export function portalCalls(source) {
  const out = []
  let at = 0
  for (;;) {
    const start = source.indexOf('createPortal(', at)
    if (start === -1) return out
    let depth = 0
    let i = start + 'createPortal'.length
    for (; i < source.length; i++) {
      if (source[i] === '(') depth++
      else if (source[i] === ')') {
        depth--
        if (depth === 0) break
      }
    }
    out.push(source.slice(start, i + 1))
    at = i + 1
  }
}

export function violations(source) {
  return portalCalls(source)
    .filter((call) => /<button[\s>]/.test(call))
    .filter((call) => !call.includes(CLASS))
}

function walk(dir, acc = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, acc)
    else if (name.endsWith('.tsx') && !name.includes('.test.')) acc.push(p)
  }
  return acc
}

if (process.argv.includes('--self-test')) {
  const bad = `createPortal(<div className="fx-usermenu"><button>x</button></div>, document.body)`
  const good = `createPortal(<div className="fx-portalmenu fx-usermenu"><button>x</button></div>, document.body)`
  const exempt = `createPortal(<div className="fx-tip">hello</div>, document.body)`
  const fail = (m) => {
    console.error(`SELF-TEST FAIL: ${m}`)
    process.exit(1)
  }
  if (violations(bad).length !== 1) fail('a portaled button without the class must be caught')
  if (violations(good).length !== 0) fail('the class must satisfy the rule')
  if (violations(exempt).length !== 0) fail('a portal with no button must be exempt')
  if (portalCalls(`${bad}\n${good}`).length !== 2) fail('each call must be extracted separately')
  console.log('SELF-TEST OK')
  process.exit(0)
}

const failures = []

// The class must actually do the job it is named for, or every call site is
// wearing a label with nothing behind it.
const css = readFileSync(CSS, 'utf8')
const root = css.match(new RegExp(`\\.${CLASS}\\s*\\{([^}]*)\\}`))
if (!root) failures.push(`.${CLASS} has no rule in foldex.css`)
else {
  for (const prop of ['font-family', 'font-weight', 'font-size', 'color']) {
    if (!root[1].includes(prop)) failures.push(`.${CLASS} does not set ${prop}`)
  }
}
if (!new RegExp(`\\.${CLASS}\\s*\\*`).test(css)) {
  failures.push(`.${CLASS} does not re-establish box-sizing on its descendants`)
}

for (const file of walk(SRC)) {
  const rel = file.slice(ROOT.length)
  for (const call of violations(readFileSync(file, 'utf8'))) {
    const head = call.slice(0, 120).replace(/\s+/g, ' ')
    failures.push(`${rel}: portaled button without .${CLASS} — ${head}…`)
  }
}

if (failures.length) {
  console.error('FAIL: portaled menus must re-establish the shell context\n')
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log('OK: every portaled button-bearing menu carries .fx-portalmenu')
