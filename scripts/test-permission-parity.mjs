#!/usr/bin/env node
// The permission vocabulary lives in TWO languages, and nothing compiled
// checks that they agree.
//
// Go's authctx.AllPermissions is the authority: it gates every route and is
// locked from both sides by TestAllPermissions_IsExactlyTheDeclaredVocabulary.
// The TypeScript union in web/src/auth/types.ts is what the roles-and-
// permissions grid renders from, so a permission missing there is a row the
// owner cannot see or grant — the feature exists on the server and is
// unreachable from the screen.
//
// It has already happened: `instance.ip_block` shipped with ADR-46 and never
// reached the union. Neither `tsc` nor `go vet` can see it, because neither
// language knows the other's file exists. This script is the seam.
//
// Direction matters and both are failures, for different reasons:
//   Go has it, TS does not  → the grid cannot offer a permission that exists.
//   TS has it, Go does not  → the grid offers a permission nothing enforces,
//                             which reads as a granted power that does nothing.
import { readdirSync, readFileSync } from 'node:fs'

const goSrc = readFileSync('backend/internal/pkg/authctx/permissions.go', 'utf8')
const tsSrc = readFileSync('web/src/auth/types.ts', 'utf8')

// The const block is the declaration; AllPermissions is the list. Read the
// DECLARATIONS rather than the list, so a constant declared and left out of
// AllPermissions is caught here too — Go's own guard covers that, but reading
// the same source twice is what keeps the two guards from disagreeing.
const goPerms = new Set(
  [...goSrc.matchAll(/Perm[A-Za-z0-9]+\s+Permission\s*=\s*"([^"]+)"/g)].map((m) => m[1]),
)

const unionMatch = tsSrc.match(/export type Permission\s*=([\s\S]*?)\n\n/)
if (!unionMatch) {
  console.error('FAIL: could not find `export type Permission` in web/src/auth/types.ts')
  process.exit(1)
}
const tsPerms = new Set([...unionMatch[1].matchAll(/'([^']+)'/g)].map((m) => m[1]))

if (goPerms.size === 0) {
  console.error('FAIL: parsed zero permissions from permissions.go — the regex stopped matching')
  process.exit(1)
}

let bad = false
for (const p of goPerms) {
  if (!tsPerms.has(p)) {
    console.error(`FAIL: ${p} is enforced by the backend and missing from the Permission union —`)
    console.error('      the roles grid cannot render or grant it')
    bad = true
  }
}
for (const p of tsPerms) {
  if (!goPerms.has(p)) {
    console.error(`FAIL: ${p} is in the Permission union and enforced nowhere —`)
    console.error('      the grid would offer a power that does nothing')
    bad = true
  }
}
// The third link in the same chain. The roles grid renders each permission's
// description with t('admin.perm_' + p.replaceAll('.', '_')), so a permission
// with no key shows the raw id as its own explanation — a cell the owner is
// asked to grant without being told what it does. `instance.rate_limits`
// shipped exactly like that, one commit after the Go↔TS half was closed.
// Derived from the directory, never listed here: a fourth locale added to the
// app would otherwise go uncovered in silence, which is the same "a count in
// prose is not a guard" this file enforces one layer up.
const locales = readdirSync('web/src/i18n/locales')
  .filter((f) => f.endsWith('.json'))
  .map((f) => f.replace(/\.json$/, ''))
if (locales.length === 0) {
  console.error('FAIL: no locale files found under web/src/i18n/locales')
  process.exit(1)
}
for (const loc of locales) {
  const raw = readFileSync(`web/src/i18n/locales/${loc}.json`, 'utf8')
  const messages = JSON.parse(raw)
  const admin = messages.admin ?? {}
  for (const p of goPerms) {
    // replaceAll, matching RolesMatrix.tsx — which uses replace(). They agree
    // only because every permission today has exactly one dot; the first
    // two-dot name would give a green guard and a cell rendering the raw id.
    const key = `perm_${p.split('.').join('_')}`
    if (typeof admin[key] !== 'string' || admin[key].trim() === '') {
      console.error(`FAIL: ${loc}.json is missing admin.${key} for permission ${p} —`)
      console.error('      the roles grid would render the raw id as the description')
      bad = true
    }
  }
}

if (bad) process.exit(1)
console.log(
  `ok: the permission vocabulary matches across Go, TypeScript and ${locales.length} locales ` +
    `(${goPerms.size} permissions)`,
)
