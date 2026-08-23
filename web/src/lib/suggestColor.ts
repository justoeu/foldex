import { INLINE_PALETTE } from './inlinePalette'
import { primaryColor } from './tagColor'

/** Injected so a test can pin the choice; production uses Math.random. */
export type Pick = (upperExclusive: number) => number

const defaultPick: Pick = (n) => Math.floor(Math.random() * n)

/**
 * Proposes a colour for a NEW folder or tag.
 *
 * Every new one used to open on the same indigo, so a library built by
 * accepting the default is a wall of identical chips — the colour stops being
 * information. Suggesting one costs the user nothing: the picker is still right
 * there, and this only decides what it starts on.
 *
 * It is not a blind draw. A colour already in use is skipped while any unused
 * one remains, because two chips of the same colour is exactly what makes a
 * palette useless — and a plain `Math.random()` over twenty entries collides
 * about half the time by the seventh tag (birthday problem). Once every colour
 * is taken it falls back to the whole palette, which is the honest answer: at
 * that point no choice avoids a repeat.
 *
 * `taken` is compared through `primaryColor`, so a gradient counts as its first
 * stop — otherwise a gradient built from the palette's indigo would leave that
 * indigo looking free.
 */
export function suggestColor(taken: readonly string[] = [], pick: Pick = defaultPick): string {
  const used = new Set(
    taken
      .filter((c) => !!c)
      .map((c) => primaryColor(c).trim().toUpperCase()),
  )
  const free = INLINE_PALETTE.filter((c) => !used.has(c.toUpperCase()))
  const pool = free.length > 0 ? free : INLINE_PALETTE
  return pool[pick(pool.length)] ?? INLINE_PALETTE[0]
}
