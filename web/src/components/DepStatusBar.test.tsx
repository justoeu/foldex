import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { DepStatusBar } from './DepStatusBar'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('DepStatusBar', () => {
  it('stays hidden when every resource is up', async () => {
    state.depStatus = {
      resources: [{ id: 'object_store', state: 'ok' }],
    }
    const { container } = renderWithProviders(<DepStatusBar />)
    await waitFor(() => expect(state.depStatusCalls ?? 0).toBeGreaterThan(0))
    expect(container.querySelector('.fx-dep-status')).toBeNull()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('names a down object store', async () => {
    state.depStatus = {
      resources: [{ id: 'object_store', state: 'unreachable' }],
    }
    renderWithProviders(<DepStatusBar />)
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Could not connect: object storage',
    )
  })

  it('joins several down resources', async () => {
    state.depStatus = {
      resources: [
        { id: 'object_store', state: 'unreachable' },
        { id: 'mail_broker', state: 'unreachable' },
      ],
    }
    renderWithProviders(<DepStatusBar />)
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Could not connect: object storage, mail broker',
    )
  })

  it('ignores unknown resource ids', async () => {
    state.depStatus = {
      resources: [{ id: 'mystery', state: 'unreachable' }],
    }
    const { container } = renderWithProviders(<DepStatusBar />)
    await waitFor(() => expect(state.depStatusCalls ?? 0).toBeGreaterThan(0))
    expect(container.querySelector('.fx-dep-status')).toBeNull()
  })
})
