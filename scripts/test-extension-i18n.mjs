#!/usr/bin/env node
/**
 * Extension locale parity: en/pt/es must ship identical chrome.i18n keys,
 * and the popup/options surfaces must not regress to English literals.
 */
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(fileURLToPath(new URL('.', import.meta.url)), '..')
const EXT = join(ROOT, 'extension')
const LOCALES = ['en', 'pt', 'es']

function fail(msg) {
  console.error(msg)
  process.exit(1)
}

const localesDir = join(EXT, '_locales')
let names
try {
  names = readdirSync(localesDir).sort()
} catch {
  fail('extension/_locales is missing')
}
if (names.join(',') !== LOCALES.slice().sort().join(',')) {
  fail(`expected locales ${LOCALES.join(', ')}; got ${names.join(', ')}`)
}

const keys = LOCALES.map((locale) => {
  const raw = readFileSync(join(localesDir, locale, 'messages.json'), 'utf8')
  return Object.keys(JSON.parse(raw)).sort()
})
if (keys[0].length === 0) fail('en messages.json is empty')
for (let i = 1; i < keys.length; i++) {
  if (keys[i].join('\0') !== keys[0].join('\0')) {
    fail(`${LOCALES[i]} message keys drift from en`)
  }
}

const manifest = JSON.parse(readFileSync(join(EXT, 'manifest.json'), 'utf8'))
if (manifest.default_locale !== 'en') fail('manifest.json default_locale must be en')

const banned = [
  'URL is required',
  'Saving…',
  'Saved ✓',
  'Requesting access…',
  'not signed in — set an API token in settings',
]
for (const name of ['popup.js', 'options.js']) {
  const src = readFileSync(join(EXT, name), 'utf8')
  for (const lit of banned) {
    if (src.includes(lit)) fail(`${name} still hardcodes ${JSON.stringify(lit)}`)
  }
}
for (const name of ['popup.html', 'options.html']) {
  const html = readFileSync(join(EXT, name), 'utf8')
  if (!html.includes('__MSG_')) fail(`${name} has no __MSG_ substitutions`)
}

console.log(`extension i18n ok (${keys[0].length} keys × ${LOCALES.length} locales)`)
