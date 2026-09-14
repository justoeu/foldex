const MAX_LEN = 80
const COMBINING_MARK = /\p{Mn}/gu

/** Client-side slugify mirroring backend pkg/slug (ASCII-ish, hyphenated). */
export function slugifyClient(title: string): string {
  const folded = title
    .normalize('NFD')
    .replace(COMBINING_MARK, '')
    .normalize('NFC')
    .toLowerCase()
  return truncate(hyphenateAscii(folded), MAX_LEN)
}

function hyphenateAscii(value: string): string {
  let out = ''
  let pendingHyphen = false
  for (const ch of value) {
    if ((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
      if (pendingHyphen && out.length > 0) out += '-'
      out += ch
      pendingHyphen = false
    } else {
      pendingHyphen = true
    }
  }
  return out
}

function trimHyphens(value: string): string {
  let start = 0
  let end = value.length
  while (start < end && value[start] === '-') start++
  while (end > start && value[end - 1] === '-') end--
  return value.slice(start, end)
}

function truncate(value: string, maxLen: number): string {
  if (value.length <= maxLen) return value
  let cut = value.slice(0, maxLen)
  if (value.charAt(maxLen) !== '-') {
    const i = cut.lastIndexOf('-')
    if (i > 0) cut = cut.slice(0, i)
  }
  return trimHyphens(cut)
}
