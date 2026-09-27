// Operator knobs. Nothing here has a default password: a load run that
// invents credentials would either fail closed or, worse, bootstrap an
// account the operator did not mean to create.

export const baseURL = (__ENV.K6_BASE_URL || 'http://127.0.0.1:9089').replace(/\/$/, '')
export const email = __ENV.K6_EMAIL || ''
export const password = __ENV.K6_PASSWORD || ''
export const flow = __ENV.K6_FLOW || 'smoke'
export const profile = __ENV.K6_PROFILE || 'smoke'
// FOLDEX_K6_* and not K6_VUS / K6_DURATION: k6 treats those two names as
// its own shortcut and throws away the scenarios block.
export const vus = positive(__ENV.FOLDEX_K6_VUS, 1)
export const duration = __ENV.FOLDEX_K6_DURATION || '15s'

function positive(raw, fallback) {
  const n = Number(raw)
  return Number.isFinite(n) && n > 0 ? n : fallback
}
