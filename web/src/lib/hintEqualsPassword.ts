export function hintEqualsPassword(hint: string, password: string): boolean {
  const h = hint.trim()
  const p = password.trim()
  return h !== '' && p !== '' && h.toLowerCase() === p.toLowerCase()
}
