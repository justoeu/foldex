import { describe, it, expect } from 'vitest'
import { suggestColor } from './suggestColor'
import { INLINE_PALETTE } from './inlinePalette'

describe('suggesting a colour for a new folder or tag', () => {
  it('always proposes something from the curated palette', () => {
    expect(INLINE_PALETTE).toContain(suggestColor([]))
  })

  // The whole point: two chips of the same colour is what makes a palette
  // useless, so a colour in use is skipped while any unused one remains.
  it('never proposes a colour that is already taken', () => {
    const taken = INLINE_PALETTE.slice(0, 19)
    expect(suggestColor(taken)).toBe(INLINE_PALETTE[19])
  })

  // A gradient counts as its first stop — otherwise a gradient built from the
  // palette's indigo would leave that indigo looking free.
  it('counts a gradient as its first stop', () => {
    const taken = INLINE_PALETTE.slice(1).concat(
      `linear-gradient(135deg, ${INLINE_PALETTE[0]}, #FFFFFF)`,
    )
    // Every entry is now spoken for, so it falls back to the full palette
    // rather than pretending the gradient's indigo is free.
    expect(INLINE_PALETTE).toContain(suggestColor(taken))
  })

  it('is case-insensitive about what counts as taken', () => {
    const taken = INLINE_PALETTE.slice(0, 19).map((c) => c.toLowerCase())
    expect(suggestColor(taken)).toBe(INLINE_PALETTE[19])
  })

  // Once nothing is free, no choice avoids a repeat — so it draws from the
  // whole palette instead of returning nothing or always the same colour.
  it('falls back to the whole palette when every colour is taken', () => {
    const picked = suggestColor(INLINE_PALETTE, () => 5)
    expect(picked).toBe(INLINE_PALETTE[5])
  })

  it('tolerates junk in the taken list rather than throwing', () => {
    expect(INLINE_PALETTE).toContain(suggestColor(['', '   ', 'not-a-colour']))
  })

  // Without a real spread, "random" that always lands on one entry would pass
  // every test above.
  it('actually varies across calls', () => {
    const seen = new Set(Array.from({ length: 200 }, () => suggestColor([])))
    expect(seen.size).toBeGreaterThan(5)
  })
})
