import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useConfirm, ConfirmProvider } from './ConfirmDialog'
import { renderWithProviders } from '../test/renderWithProviders'

function TriggerFlow({ title, message, destructive, onResolved }: {
  title: string
  message?: string
  destructive?: boolean
  onResolved?: (ok: boolean) => void
}) {
  const confirm = useConfirm()
  return (
    <button
      data-testid="trigger"
      onClick={async () => {
        const ok = await confirm({ title, message, destructive })
        onResolved?.(ok)
      }}
    >
      trigger
    </button>
  )
}

describe('ConfirmDialog', () => {
  it('opens a dialog with the given title', async () => {
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete item?" />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    expect(screen.getByText('Delete item?')).toBeInTheDocument()
  })

  it('shows the message body when provided', async () => {
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete" message="This cannot be undone" />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    expect(screen.getByText('This cannot be undone')).toBeInTheDocument()
  })

  it('resolves true on confirm click', async () => {
    const onResolved = vi.fn()
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete?" onResolved={onResolved} />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    const confirmBtn = screen.getByRole('button', { name: /confirm/i })
    await userEvent.setup().click(confirmBtn)
    expect(onResolved).toHaveBeenCalledWith(true)
  })

  it('resolves false on cancel click', async () => {
    const onResolved = vi.fn()
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete?" onResolved={onResolved} />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    const cancelBtn = screen.getByRole('button', { name: /cancel/i })
    await userEvent.setup().click(cancelBtn)
    expect(onResolved).toHaveBeenCalledWith(false)
  })

  it('closes the dialog after confirmation', async () => {
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete?" />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    expect(screen.getByText('Delete?')).toBeInTheDocument()
    const confirmBtn = screen.getByRole('button', { name: /confirm/i })
    await userEvent.setup().click(confirmBtn)
    expect(screen.queryByText('Delete?')).not.toBeInTheDocument()
  })

  it('renders destructive button with trash icon when destructive is true', async () => {
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Delete item?" destructive />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    // The confirm button should have the danger class
    const confirmBtn = screen.getByRole('button', { name: /confirm/i })
    expect(confirmBtn.className).toContain('fx-confirm-btn-danger')
    // And should contain a trash icon (SVG inside the button)
    const svg = confirmBtn.querySelector('svg')
    expect(svg).toBeInTheDocument()
  })

  it('renders normal confirm button (no danger style) when not destructive', async () => {
    renderWithProviders(
      <ConfirmProvider>
        <TriggerFlow title="Confirm action?" destructive={false} />
      </ConfirmProvider>,
    )
    await userEvent.setup().click(screen.getByTestId('trigger'))
    const confirmBtn = screen.getByRole('button', { name: /confirm/i })
    expect(confirmBtn.className).toContain('fx-confirm-btn-primary')
    expect(confirmBtn.className).not.toContain('fx-confirm-btn-danger')
  })
})
