import { useEffect, useRef, useState } from 'react'

export type AvailabilityReason = 'shape' | 'reserved' | 'taken' | 'empty' | 'pending'
export type Availability =
  | { state: 'idle' }
  | { state: 'checking' }
  | { state: 'free' }
  /** Usable, but with something the caller should know — an address somebody
   *  else is already moving to. It must NOT block: the server would accept the
   *  write, and a screen hiding a button the server allows reads as a missing
   *  feature (CLAUDE.md §5). */
  | { state: 'warn'; reason: AvailabilityReason }
  | { state: 'refused'; reason: AvailabilityReason }
  | { state: 'error' }

export type AvailabilityResponse = { available: boolean; reason?: AvailabilityReason }

/** What the hook calls. Injected rather than a URL string, so the route and the
 *  response type live in `api/*` beside every other server call — and so a
 *  component test that mocks the probe still cannot drift from the real one. */
export type Probe = (value: string, signal: AbortSignal) => Promise<AvailabilityResponse>

/**
 * Asks the server whether an identifier can be claimed, while it is typed.
 *
 * Debounced at 450 ms and aborted on every keystroke, because the endpoint is
 * rate-limited by design — it answers a question about somebody else's
 * identifier, so its budget is what stops it from being an enumeration API.
 * A field that fired per character would burn a human's whole allowance
 * halfway through one word.
 *
 * `initial` is the value the account already holds. Typing back to it returns
 * `idle`, not `free`: nothing is being claimed, so a green check there would
 * be reporting on an action the user is not taking.
 *
 * The server remains the authority. This only tells the user sooner; every
 * write is still refused by the handler, the repository and the database.
 */
export function useAvailability(probe: Probe, value: string, initial = ''): Availability {
  const [result, setResult] = useState<Availability>({ state: 'idle' })
  const trimmed = value.trim()

  // The probe is held in a ref and kept OUT of the effect's deps.
  //
  // With it in the deps, a caller passing an inline arrow gets a new identity
  // on every render, the effect tears down and re-runs, that sets state, which
  // renders again — an unbroken loop that ends in an out-of-memory kill, not a
  // warning. Both call sites happen to pass a stable module-level function, so
  // this would have shipped as a trap for the third one; it was found because a
  // test wrote the inline version. What the effect must react to is the VALUE.
  const probeRef = useRef(probe)
  probeRef.current = probe

  useEffect(() => {
    if (!trimmed || trimmed.toLowerCase() === initial.trim().toLowerCase()) {
      setResult({ state: 'idle' })
      return
    }
    setResult({ state: 'checking' })
    const ac = new AbortController()
    const timer = setTimeout(() => {
      probeRef.current(trimmed, ac.signal)
        .then((data) => {
          // The guard belongs on BOTH arms. abort() is a no-op on a promise
          // that already settled, so a response that landed just before the
          // next keystroke still resolves — and without this it writes
          // "Available" about the PREVIOUS value, under text nobody checked.
          // Because `blocked` is derived from this state, the other ordering
          // clears a legitimate refusal and re-enables Save for the debounce.
          if (ac.signal.aborted) return
          setResult(
            data.available && !data.reason
              ? { state: 'free' }
              : data.available
                ? { state: 'warn', reason: data.reason as AvailabilityReason }
                : { state: 'refused', reason: data.reason ?? 'taken' },
          )
        })
        .catch(() => {
          // An aborted request is the NEXT keystroke, not a failure: reporting
          // it would flash an error on every character typed.
          if (ac.signal.aborted) return
          setResult({ state: 'error' })
        })
    }, 450)

    return () => {
      clearTimeout(timer)
      ac.abort()
    }
  }, [trimmed, initial])

  return result
}
