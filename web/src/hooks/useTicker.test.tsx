import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useTicker } from './useTicker'

afterEach(() => {
  vi.useRealTimers()
})

describe('useTicker', () => {
  it('advances the clock once per period', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-09T12:00:00Z'))
    const { result } = renderHook(() => useTicker(1000))
    const first = result.current

    act(() => { vi.advanceTimersByTime(3000) })
    expect(result.current - first).toBe(3000)
  })

  /*
   * The arm the hook exists for. A screen that COULD show a counter must not
   * re-render every second on the days nothing is running, so a null period
   * has to register no timer at all — asserting the value is frozen would pass
   * even if an interval were running and setting the same number.
   */
  it('registers no interval at all when the period is null', () => {
    vi.useFakeTimers()
    const spy = vi.spyOn(globalThis, 'setInterval')
    const { result } = renderHook(() => useTicker(null))
    const frozen = result.current

    expect(spy).not.toHaveBeenCalled()
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(result.current).toBe(frozen)
  })

  it('refreshes immediately when a frozen counter is re-armed', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-09T12:00:00Z'))
    const { result, rerender } = renderHook(({ p }: { p: number | null }) => useTicker(p), {
      initialProps: { p: null as number | null },
    })
    const frozen = result.current

    // Time passes while frozen; without the eager read the first paint of the
    // re-armed counter would show a number a whole period stale.
    act(() => { vi.setSystemTime(new Date('2026-09-09T12:00:42Z')) })
    rerender({ p: 1000 })
    expect(result.current - frozen).toBe(42_000)
  })

  it('clears its interval on unmount', () => {
    vi.useFakeTimers()
    const spy = vi.spyOn(globalThis, 'clearInterval')
    const { unmount } = renderHook(() => useTicker(1000))
    unmount()
    expect(spy).toHaveBeenCalled()
  })
})
