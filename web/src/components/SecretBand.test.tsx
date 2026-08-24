import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SecretBand } from './SecretBand'
import { renderWithProviders } from '../test/renderWithProviders'

afterEach(() => vi.restoreAllMocks())

function stubClipboard(writeText: ReturnType<typeof vi.fn>) {
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText }, configurable: true, writable: true,
  })
}

describe('SecretBand', () => {
  it('keeps the plaintext out of page translation', () => {
    // Chrome/Edge upload the page's visible text to translate.googleapis.com.
    // This is the whole reason the three surfaces share a component: each of
    // them had to remember, and none of them did.
    renderWithProviders(<SecretBand label="Key" value="s3cr3t" testId="v" />)
    expect(screen.getByTestId('v')).toHaveAttribute('translate', 'no')
  })

  it('copies the value it displays', async () => {
    // setup() installs userEvent's OWN clipboard stub, so it has to run
    // before ours or the assertion measures theirs.
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    renderWithProviders(<SecretBand label="Key" value="s3cr3t" />)

    await user.click(screen.getByRole('button', { name: /^copy$/i }))
    expect(writeText).toHaveBeenCalledWith('s3cr3t')
    expect(await screen.findByRole('button', { name: /^copied$/i })).toBeInTheDocument()
  })

  // The live bug the shared component had: the band stays MOUNTED across a
  // second mint (same position, no key), so a boolean `copied` survived the
  // secret changing underneath it. An admin then dismisses a token the server
  // cannot show again, believing they copied it.
  it('stops saying "Copied" when the secret underneath changes', async () => {
    const user = userEvent.setup()
    stubClipboard(vi.fn().mockResolvedValue(undefined))
    const { rerender } = renderWithProviders(<SecretBand label="Key" value="token-A" />)

    await user.click(screen.getByRole('button', { name: /^copy$/i }))
    expect(await screen.findByRole('button', { name: /^copied$/i })).toBeInTheDocument()

    rerender(<SecretBand label="Key" value="token-B" />)
    expect(screen.getByRole('button', { name: /^copy$/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^copied$/i })).toBeNull()
  })

  it('renders the hint and any trailing action beside copy', () => {
    renderWithProviders(
      <SecretBand label="Key" value="s3cr3t" hint={<span>write it down</span>}>
        <button>Done</button>
      </SecretBand>,
    )
    expect(screen.getByText('write it down')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Done' })).toBeInTheDocument()
  })
})
