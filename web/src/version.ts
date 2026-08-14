// Single source of truth: web/package.json is bumped by `make release-{patch,
// minor,major}` (which runs scripts/release.sh — also bumps
// extension/manifest.json). A manual release workflow dispatch from main
// validates the target before creating `vX.Y.Z` and publishing Docker images.
// This file just re-exports pkg.version so the sidebar footer matches it.
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
