import { MIN_PASSWORD_LEN } from '../auth/types'

export type PasswordChecks = {
  length: boolean
  longer: boolean
  lower: boolean
  upper: boolean
  digit: boolean
  symbol: boolean
}

export function passwordChecks(pw: string): PasswordChecks {
  return {
    length: pw.length >= MIN_PASSWORD_LEN,
    longer: pw.length >= 12,
    lower: /[a-z]/.test(pw),
    upper: /[A-Z]/.test(pw),
    digit: /\d/.test(pw),
    symbol: /[^A-Za-z0-9]/.test(pw),
  }
}

// Score 0 (empty) → 4 (strong). Combines length and character-class variety.
export function passwordScore(pw: string): 0 | 1 | 2 | 3 | 4 {
  if (!pw) return 0
  const c = passwordChecks(pw)
  let points = 0
  if (c.length) points++
  if (c.longer) points++
  if (c.lower && c.upper) points++
  if (c.digit) points++
  if (c.symbol) points++
  if (points <= 1) return 1
  if (points === 2) return 2
  if (points === 3) return 3
  return 4
}
