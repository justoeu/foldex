import { describe, expect, it } from 'vitest'
import { looksLikeUrl } from './url'

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
