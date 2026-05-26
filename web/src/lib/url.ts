// Heuristic: is the clipboard payload "URL-shaped enough" to open the
// New Link dialog with? We're deliberately permissive — paste behaviour
// favours dynamism over correctness (a false-positive lands in the URL
// field that the user can fix; a false-negative silently does nothing
// and loses the gesture).
//
// Rules:
//   - parses as a real URL via `new URL` (strict path, accepts `http(s)://`,
//     `ftp://`, custom schemes), OR
//   - parses as a URL once we prepend `https://`, AND
//     - the resolved hostname contains at least one dot (so we can rule
//       out plain words like "hello"), AND
//     - the original string contains no whitespace (multi-line clipboard
//       content is almost never a URL).
//
// Returned URL is the original string trimmed — the dialog's submit
// handler decides whether to require a scheme; the user can also edit
// the field before saving.
export function looksLikeUrl(raw: string): boolean {
  const trimmed = raw.trim()
  if (!trimmed) return false
  if (/\s/.test(trimmed)) return false

  // First try: a fully-qualified URL with explicit scheme. Reject any
  // protocol that isn't web-shaped — `new URL` accepts `mailto:`,
  // `tel:`, `javascript:`, etc., which we don't want to bookmark.
  try {
    const url = new URL(trimmed)
    if (!/^https?:|^ftp:|^file:/i.test(url.protocol)) return false
    return true
  } catch {
    // fall through to implicit-https attempt
  }

  // Implicit `https://` path. Require a dot in the ORIGINAL input — not
  // in the parsed hostname, because the URL parser quietly turns bare
  // integers into IPv4 octets ("42" → hostname "0.0.0.42"), which would
  // give every typed number a false positive.
  if (!trimmed.includes('.')) return false
  try {
    const url = new URL('https://' + trimmed)
    return url.hostname.includes('.')
  } catch {
    return false
  }
}

// Gate any URL we hand to `<img src>`. `favicon_url` and `og_image_url`
// are stamped by the preview worker from whatever the remote page declared
// (`<link rel="icon">` / `<meta property="og:image">`), so an attacker-
// controlled page could try to slip in `data:`, `file:`, `javascript:`,
// or other non-http schemes. React 19 blocks `javascript:` in `<img src>`
// in practice but `data:`/`file:` pass; we'd rather not depend on the
// renderer's default. Returns the URL when safe, `undefined` otherwise so
// `<img src={undefined}>` skips the network request entirely.
//
// Accepts: absolute http(s) URLs and site-relative paths starting with a
// single `/`. The site-relative branch is defensive — no current caller
// produces such URLs (the preview worker stamps absolute URLs only), so
// removing it would be a behavior change visible only to future writers.
// Protocol-relative `//host/path` is rejected deliberately — the caller
// can pick a scheme. Everything else (data:, file:, javascript:, vbscript:,
// bare hostnames) is rejected.
export function safeImageUrl(raw: string | null | undefined): string | undefined {
  if (!raw) return undefined
  const trimmed = raw.trim()
  if (!trimmed) return undefined
  // Single leading slash = site-relative path. `//` would be a
  // protocol-relative URL ("//host/x"), which we deliberately reject above.
  if (trimmed.startsWith('/') && !trimmed.startsWith('//')) return trimmed
  // Strict whitelist — `new URL` is too lenient (it happily parses
  // `javascript:alert(1)` as `protocol: 'javascript:'`).
  if (!/^https?:\/\//i.test(trimmed)) return undefined
  try {
    new URL(trimmed) // throws on malformed authority
    return trimmed
  } catch {
    return undefined
  }
}
