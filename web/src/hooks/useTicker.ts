import { useEffect, useState } from 'react'

/**
 * A clock that only ticks while something on screen depends on it.
 *
 * `periodMs === null` registers no interval and freezes the value. That arm is
 * the point of the hook: a screen with an elapsed counter it COULD show must
 * not re-render once a second forever on the days nothing is running, and the
 * caller decides that from its own data rather than the hook guessing.
 *
 * The value is `Date.now()`, so everything downstream stays a pure function of
 * (data, now) and can be asserted without fake timers.
 */
export function useTicker(periodMs: number | null): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (periodMs === null) return
    /* Resuming after a frozen stretch: the held value is as old as the pause,
       so the first paint of a re-armed counter would show a stale number for
       one whole period. */
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), periodMs)
    return () => clearInterval(id)
  }, [periodMs])
  return now
}
