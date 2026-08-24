/**
 * The alphabet a generated temporary password is drawn from.
 *
 * `0`/`O`, `1`/`l`/`I` are absent because this password is TRANSCRIBED: the
 * administrator reads it out, writes it on paper or pastes it into a chat, and
 * the person it belongs to types it once. A pair that survives that trip is
 * worth more than the ~0.2 bits per character the excluded symbols would add —
 * at length 20 the alphabet still carries ~116 bits, which is far beyond what
 * a credential the server forces out on first use needs.
 *
 * Symbols are absent for the same reason and not because the server refuses
 * them: it validates length only.
 */
export const ALPHABET = 'abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789'

/** Floor for a generated value, independent of the instance's own floor. */
export const GENERATED_LENGTH = 20

/**
 * Ceiling, in characters. bcrypt truncates at 72 BYTES and the alphabet above
 * is ASCII, so the two units coincide here — generating past it would hand the
 * administrator a value whose tail is decorative.
 */
export const GENERATED_MAX_LENGTH = 72

/**
 * A cryptographically random password, at least `GENERATED_LENGTH` characters
 * and at least `minLength` (the instance's configured floor).
 *
 * Bytes outside the largest whole multiple of the alphabet are DISCARDED
 * rather than folded in with `%`: 256 is not a multiple of 57, so a plain
 * modulo would make the first 28 characters of the alphabet ~1.3× likelier
 * than the rest. The bias is small and completely avoidable, and rejection
 * sampling is what avoids it.
 */
export function generatePassword(minLength: number = GENERATED_LENGTH): string {
  const want = Math.min(
    Math.max(Number.isFinite(minLength) ? Math.trunc(minLength) : 0, GENERATED_LENGTH),
    GENERATED_MAX_LENGTH,
  )
  const limit = 256 - (256 % ALPHABET.length)
  const out: string[] = []

  while (out.length < want) {
    // Refilled rather than drawn one byte at a time: a rejected byte costs a
    // loop iteration, not a syscall.
    const buf = new Uint8Array(want - out.length)
    crypto.getRandomValues(buf)
    for (const b of buf) {
      if (b >= limit) continue
      out.push(ALPHABET[b % ALPHABET.length])
    }
  }

  return out.join('')
}
