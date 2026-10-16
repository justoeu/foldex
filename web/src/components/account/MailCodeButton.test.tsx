import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MailCodeButton } from './MailCodeButton'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'

describe('MailCodeButton', () => {
  beforeEach(() => vi.restoreAllMocks())

  it('surfaces a failed send instead of silently resetting', async () => {
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 429, data: { error: { code: 'too_many_attempts', message: 'slow down' } } },
    })
    renderWithProviders(<MailCodeButton disabled={false} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /mail.*code/i }))
    expect(await screen.findByText(/slow down|too many/i)).toBeInTheDocument()
  })

  it('reports success after the code is sent', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<MailCodeButton disabled={false} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /mail.*code/i }))
    expect(await screen.findByText(/code sent/i)).toBeInTheDocument()
  })
})
