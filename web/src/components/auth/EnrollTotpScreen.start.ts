import { useEffect, useRef } from 'react'
import {
  startEmailFactor,
  startTotp,
  type EmailFactorEnrollment,
  type FactorMethod,
  type TotpEnrollment,
} from '../../api/twofa'

/**
 * Starts the chosen method's enrollment exactly once.
 *
 * A ref, and DELIBERATELY no per-effect `alive` flag — see INV-120. What must
 * be prevented is the second REQUEST, not the second setState: starting an
 * enrollment mints a new secret (or mails a new code) and supersedes the
 * pending row. Both StrictMode's double mount and a mid-enrollment language
 * change would do that — `t` is a new function identity on every locale
 * switch, so it must not be a dependency here. The effect is keyed by METHOD
 * only.
 */
export function useEnrollStart(
  method: FactorMethod | null,
  onReady: {
    totp: (enrollment: TotpEnrollment) => void
    email: (enrollment: EmailFactorEnrollment) => void
    error: () => void
  },
) {
  const started = useRef<FactorMethod | null>(null)
  useEffect(() => {
    if (!method || started.current === method) return
    started.current = method
    if (method === 'totp') {
      startTotp().then(onReady.totp).catch(onReady.error)
    } else {
      startEmailFactor().then(onReady.email).catch(onReady.error)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- INV-120: keyed by METHOD only.
  }, [method])
}
