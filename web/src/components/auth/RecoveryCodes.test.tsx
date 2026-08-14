import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RecoveryCodes } from './RecoveryCodes'
import { renderWithProviders } from '../../test/renderWithProviders'

const codes = ['AAAAA-BBBBB', 'CCCCC-DDDDD', 'EEEEE-FFFFF']

// navigator.clipboard is a getter-only property in jsdom, so Object.assign
// throws — it has to be redefined.
function stubClipboard(writeText: ReturnType<typeof vi.fn>) {
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
    writable: true,
  })
  return writeText
}

afterEach(() => vi.restoreAllMocks())

describe('RecoveryCodes', () => {
  it('lists every code', () => {
    renderWithProviders(<RecoveryCodes codes={codes} onDone={() => {}} />)
    codes.forEach((c) => expect(screen.getByText(c)).toBeInTheDocument())
  })

  /**
   * The gate that matters. The server keeps only a keyed digest of each code, so this
   * really is the only time they exist in readable form — clicking past without
   * copying them has no undo. The checkbox is what turns "we warned you" into
   * "you confirmed".
   */
  it('will not continue until the user acknowledges them', async () => {
    const user = userEvent.setup()
    const onDone = vi.fn()
    renderWithProviders(<RecoveryCodes codes={codes} onDone={onDone} />)

    const cont = screen.getByRole('button', { name: /continue/i })
    expect(cont).toBeDisabled()
    await user.click(cont)
    expect(onDone).not.toHaveBeenCalled()

    await user.click(screen.getByRole('checkbox'))
    await user.click(cont)
    expect(onDone).toHaveBeenCalledTimes(1)
  })

  it('copies every code, newline separated', async () => {
    // The stub goes in AFTER userEvent.setup(), which installs a clipboard of
    // its own and would otherwise replace this one.
    const user = userEvent.setup()
    const writeText = stubClipboard(vi.fn().mockResolvedValue(undefined))
    renderWithProviders(<RecoveryCodes codes={codes} onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: /copy codes/i }))
    expect(writeText).toHaveBeenCalledWith(codes.join('\n'))
    expect(await screen.findByRole('button', { name: /copied/i })).toBeInTheDocument()
  })

  // Clipboard access is denied in an insecure context and absent in older
  // browsers. The codes are on screen either way, so failing loudly would be an
  // error the user can do nothing about.
  it('stays usable when the clipboard is unavailable', async () => {
    const user = userEvent.setup()
    stubClipboard(vi.fn().mockRejectedValue(new Error('denied')))

    renderWithProviders(<RecoveryCodes codes={codes} onDone={() => {}} />)

    await user.click(screen.getByRole('button', { name: /copy codes/i }))
    // No error surfaced, and the label does not lie about having copied.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /copy codes/i })).toBeInTheDocument()
  })

  it('offers a .txt download containing the codes', async () => {
    const user = userEvent.setup()
    const created: Blob[] = []
    const createObjectURL = vi.fn((b: Blob) => {
      created.push(b)
      return 'blob:codes'
    })
    vi.stubGlobal('URL', { ...URL, createObjectURL, revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderWithProviders(<RecoveryCodes codes={codes} onDone={() => {}} />)
    await user.click(screen.getByRole('button', { name: /download/i }))

    expect(click).toHaveBeenCalled()
    expect(created).toHaveLength(1)
    expect(await created[0].text()).toBe(codes.join('\n') + '\n')
    vi.unstubAllGlobals()
  })
})
