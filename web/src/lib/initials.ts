/**
 * "Valmir Justo" → "VJ"; "grace" → "G"; "" → "?".
 *
 * The `?` placeholder is what keeps the avatar a stable square instead of
 * collapsing when the name is empty. Falls back to the e-mail, because an
 * account can legitimately have no display name.
 */
export function initialsOf(name: string, email: string): string {
  const source = name.trim() || email
  const parts = source.split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0]!.slice(0, 1).toUpperCase()
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase()
}
