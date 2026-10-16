import { describe, it, expect, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { useAbortableValidation } from './useAbortableValidation'

describe('useAbortableValidation', () => {
  it('publishes the result and clears loading', async () => {
    const run = vi.fn().mockResolvedValue('ok')
    const { result } = renderHook(() => useAbortableValidation<string>(run, []))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.data).toBe('ok'))
    expect(result.current.loading).toBe(false)
    expect(result.current.errMsg).toBeNull()
  })

  it('surfaces a rejection as errMsg', async () => {
    const run = vi.fn().mockRejectedValue(new Error('boom'))
    const { result } = renderHook(() => useAbortableValidation<string>(run, []))
    await waitFor(() => expect(result.current.errMsg).not.toBeNull())
    expect(result.current.loading).toBe(false)
    expect(result.current.data).toBeNull()
  })

  it('abort suppresses the cancellation error and keeps loading set', async () => {
    let signal: AbortSignal | undefined
    const run = vi.fn((s: AbortSignal) => {
      signal = s
      return new Promise<string>((_resolve, reject) => {
        s.addEventListener('abort', () => reject(new Error('canceled')))
      })
    })
    const { result, unmount } = renderHook(() => useAbortableValidation<string>(run, []))
    await waitFor(() => expect(signal).toBeDefined())
    act(() => result.current.abort())
    expect(signal?.aborted).toBe(true)
    await act(async () => { await Promise.resolve() })
    expect(result.current.errMsg).toBeNull()
    expect(result.current.loading).toBe(true)
    expect(result.current.data).toBeNull()
    unmount()
  })

  it('reruns on deps change, aborting the superseded run', async () => {
    const signals: AbortSignal[] = []
    const run = vi.fn((s: AbortSignal) => {
      signals.push(s)
      return Promise.resolve(`run-${signals.length}`)
    })
    const { result, rerender } = renderHook(
      ({ dep }: { dep: number }) => useAbortableValidation<string>(run, [dep]),
      { initialProps: { dep: 1 } },
    )
    await waitFor(() => expect(result.current.data).toBe('run-1'))
    rerender({ dep: 2 })
    await waitFor(() => expect(result.current.data).toBe('run-2'))
    expect(signals).toHaveLength(2)
    expect(signals[0]?.aborted).toBe(true)
    expect(signals[1]?.aborted).toBe(false)
  })

  it('fires onReset at the start of every run', async () => {
    const onReset = vi.fn()
    const run = vi.fn().mockResolvedValue('ok')
    const { rerender } = renderHook(
      ({ dep }: { dep: number }) => useAbortableValidation<string>(run, [dep], { onReset }),
      { initialProps: { dep: 1 } },
    )
    await waitFor(() => expect(onReset).toHaveBeenCalledOnce())
    rerender({ dep: 2 })
    await waitFor(() => expect(onReset).toHaveBeenCalledTimes(2))
  })
})
