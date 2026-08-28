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
import { readFileSync } from 'node:fs'

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
if (bad) process.exit(1)
console.log(`ok: the permission vocabulary matches across Go and TypeScript (${goPerms.size} permissions)`)
