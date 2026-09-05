const MAX_LEN = 80

/** Client-side slugify mirroring backend pkg/slug (ASCII-ish, hyphenated). */
export function slugifyClient(title: string): string {
  const folded = title
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '')
    .normalize('NFC')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return truncate(folded, MAX_LEN)
}

function truncate(value: string, maxLen: number): string {
  if (value.length <= maxLen) return value
  let cut = value.slice(0, maxLen)
  if (value.charAt(maxLen) !== '-') {
    const i = cut.lastIndexOf('-')
    if (i > 0) cut = cut.slice(0, i)
  }
  return cut.replace(/^-+|-+$/g, '')
}
