// Single source of truth: web/package.json is bumped by release-please when
// it cuts a new version (see .github/workflows/release-please.yml). This
// file just re-exports the field so the sidebar footer renders the same
// version string that the Docker image was tagged with.
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
