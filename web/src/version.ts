// Single source of truth: web/package.json is bumped by `make release-{patch,
// minor,major}` (which runs scripts/release.sh — also bumps
// extension/manifest.json and creates a git tag `vX.Y.Z`). Pushing that tag
// fires ci.yml's `tags: ['v*']` trigger and publishes Docker images
// `:vX.Y.Z` + `:vX.Y` + `:vX` + `:latest`. This file just re-exports
// pkg.version so the sidebar footer always matches the released tag.
//
// BUILD_DATE is injected at build time by Vite via `define` (see
// vite.config.ts). In tests / dev without the define (vitest doesn't share
// vite.config.ts's `define` block), the runtime check falls back to a
// stable placeholder so the test suite doesn't depend on the clock.
import pkg from '../package.json'

declare const __FOLDEX_BUILD_DATE__: string

export const VERSION: string = pkg.version

export const BUILD_DATE: string =
  typeof __FOLDEX_BUILD_DATE__ !== 'undefined' ? __FOLDEX_BUILD_DATE__ : 'dev'
