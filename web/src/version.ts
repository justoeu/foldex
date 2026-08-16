import pkg from '../package.json'

declare const __FOLDEX_BUILD_DATE__: string

export const VERSION: string = pkg.version

// Vite injects the production date; tests and development use a stable fallback.
export const BUILD_DATE: string =
  typeof __FOLDEX_BUILD_DATE__ !== 'undefined' ? __FOLDEX_BUILD_DATE__ : 'dev'
