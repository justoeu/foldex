import { describe, expect, it } from 'vitest'
import { looksLikeUrl, safeImageUrl } from './url'

describe('looksLikeUrl', () => {
  it.each([
    ['https://example.com', true],
    ['http://example.com/path?q=1', true],
    ['example.com', true],
    ['www.github.com/anthropics', true],
    ['ftp://files.example.com/x', true],
    ['  https://example.com  ', true],
  ])('accepts URL-shaped %j → %s', (input, expected) => {
    expect(looksLikeUrl(input)).toBe(expected)
  })

  it.each([
    ['', false],
    ['   ', false],
    ['hello world', false],     // whitespace
    ['multi\nline', false],     // newlines
    ['just a word', false],     // no dot, has spaces
    ['hello', false],            // single word, no dot
    ['42', false],               // plain number
    ['mailto:foo@bar.com', false], // non-web scheme
    ['tel:+5511999999999', false],
    ['javascript:alert(1)', false], // not http/https/ftp/file
  ])('rejects %j → %s', (input, expected) => {
    expect(looksLikeUrl(input)).toBe(expected)
  })
})

describe('safeImageUrl', () => {
  it.each([
    ['https://example.com/icon.png', 'https://example.com/icon.png'],
    ['http://example.com/x.jpg', 'http://example.com/x.jpg'],
    ['HTTPS://EXAMPLE.COM/X', 'HTTPS://EXAMPLE.COM/X'], // case-insensitive scheme
    ['/static/icon.png', '/static/icon.png'],           // site-relative
    ['  https://example.com  ', 'https://example.com'], // trim
  ])('accepts %j', (input, expected) => {
    expect(safeImageUrl(input)).toBe(expected)
  })

  it.each([
    [null],
    [undefined],
    [''],
    ['   '],
    ['javascript:alert(1)'],         // the actual XSS vector
    ['JAVASCRIPT:alert(1)'],         // case-insensitive
    ['data:image/png;base64,AAAA'],  // data URI — could exfil via decoded blob
    ['file:///etc/passwd'],          // local file
    ['vbscript:msgbox(1)'],
    ['relative/no/slash.png'],       // not absolute, not site-relative
    ['example.com/icon.png'],        // missing scheme — ambiguous
    ['//cdn.example.com/x.png'],     // protocol-relative deliberately rejected (consumer should pick a scheme)
    ['https:javascript:alert(1)'],   // chimera — `new URL` accepts but no `//` after scheme, regex blocks
    ['\tjavascript:alert(1)'],       // leading tab (trim removes it then regex rejects)
    ['JavaScript:alert(1)'],         // mixed-case javascript scheme
    ['%6Aavascript:alert(1)'],       // URL-encoded scheme prefix
    ['blob:https://example.com/abc'],// blob URLs not allowed (only the LinkDialog path supplies blobs, and it bypasses the helper)
  ])('rejects %j', (input) => {
    expect(safeImageUrl(input as never)).toBeUndefined()
  })
})
