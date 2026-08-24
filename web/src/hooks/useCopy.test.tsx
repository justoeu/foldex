import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'
import { useCopy } from './useCopy'

afterEach(() => vi.restoreAllMocks())

/** navigator.clipboard is getter-only in jsdom, so it has to be redefined. */
function stubClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true, writable: true })
}

describe('useCopy', () => {
  it('reports success only after the write resolves', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard({ writeText })
    const { result } = renderHook(() => useCopy())

    expect(result.current.copied('secret')).toBe(false)
    await act(() => result.current.copy('secret'))

    expect(writeText).toHaveBeenCalledWith('secret')
    await waitFor(() => expect(result.current.copied('secret')).toBe(true))
  })

  // The defect a boolean had: the bands stay mounted across a second mint, so
  // create -> Copy -> create again left the button reading "Copied" about a
  // credential nobody had copied — and the reader dismissed a value the server
  // cannot show again. Derived from the value, that state cannot exist.
  it('does NOT claim a different secret was copied', async () => {
    stubClipboard({ writeText: vi.fn().mockResolvedValue(undefined) })
    const { result } = renderHook(() => useCopy())

    await act(() => result.current.copy('first-token'))
    await waitFor(() => expect(result.current.copied('first-token')).toBe(true))

    expect(result.current.copied('second-token')).toBe(false)
  })

  it('stays quiet when the clipboard REFUSES', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    stubClipboard({ writeText })
    const { result } = renderHook(() => useCopy())

    await act(() => result.current.copy('secret'))
    expect(result.current.copied('secret')).toBe(false)
  })

  it('stays quiet when there is no clipboard at all', async () => {
    // An insecure context: the property access itself throws SYNCHRONOUSLY
    // rather than rejecting, which a plain `.catch()` would not have caught.
    stubClipboard(undefined)
    const { result } = renderHook(() => useCopy())

    await act(() => result.current.copy('secret'))
    expect(result.current.copied('secret')).toBe(false)
  })
})
