import { useState } from 'react'

/**
 * Copy a value to the clipboard and report whether THAT value was copied.
 *
 * `copied` is derived from the value itself, not held as a boolean, and that is
 * the whole point. A boolean survives the secret changing underneath it: the
 * API-token band and the generated-password band both stay mounted across a
 * second mint, so create → Copy → create again left the button reading
 * "Copied" about a credential nobody had copied — and the reader dismissed a
 * value the server cannot show again. The first version of this hook exposed a
 * `reset()` for callers to fix that by hand; no caller called it, which is
 * exactly what a rule enforced by discipline gets you.
 *
 * The failure is SWALLOWED, deliberately: clipboard access is denied in an
 * insecure context and absent in older browsers, and there the property access
 * itself throws synchronously rather than rejecting. Every value copied through
 * this hook is already on screen — that is what the surfaces using it are for —
 * so an error the user can do nothing about would be pure noise. `copied`
 * simply stays false, which is the honest signal.
 */
export function useCopy() {
  const [copiedValue, setCopiedValue] = useState<string | null>(null)

  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value)
      setCopiedValue(value)
    } catch {
      // See the note above: intentionally silent.
    }
  }

  return {
    /** Whether THIS exact value is the one that was copied. */
    copied: (value: string) => copiedValue !== null && copiedValue === value,
    copy,
  }
}
