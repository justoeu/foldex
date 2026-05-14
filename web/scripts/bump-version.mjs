#!/usr/bin/env node
// Bumps the patch component of the version in src/version.ts and refreshes
// BUILD_DATE. Runs as `prebuild` so every `bun run build` produces a new
// version that the sidebar footer renders.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const file = join(here, '..', 'src', 'version.ts')

const raw = readFileSync(file, 'utf8')
const m = raw.match(/VERSION = '(\d+)\.(\d+)\.(\d+)'/)
if (!m) {
  console.error('bump-version: could not find a semver in', file)
  process.exit(1)
}
const [, maj, min, patch] = m
const next = `${maj}.${min}.${Number(patch) + 1}`
const today = new Date().toISOString().slice(0, 10)

const out = raw
  .replace(/VERSION = '[^']+'/, `VERSION = '${next}'`)
  .replace(/BUILD_DATE = '[^']+'/, `BUILD_DATE = '${today}'`)

writeFileSync(file, out)
console.log(`bumped version → ${next} (${today})`)
