import { describe, it, expect, beforeEach } from 'vitest'
import { waitFor } from '@testing-library/react'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { fetchDepStatus, unreachableResources, useDepStatus } from './status'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('unreachableResources', () => {
  it('keeps only known ids that are down', () => {
    expect(unreachableResources({
      resources: [
        { id: 'object_store', state: 'ok' },
        { id: 'mail_broker', state: 'unreachable' },
        { id: 'mystery', state: 'unreachable' },
      ],
    }).map((r) => r.id)).toEqual(['mail_broker'])
  })

  it('treats a missing payload as nothing to show', () => {
    expect(unreachableResources(undefined)).toEqual([])
  })
})

describe('fetchDepStatus', () => {
  it('hits GET /api/status', async () => {
    state.depStatus = {
      resources: [{ id: 'object_store', state: 'unreachable' }],
    }
    await expect(fetchDepStatus()).resolves.toEqual(state.depStatus)
  })
})

function Probe() {
  const q = useDepStatus()
  if (!q.data) return <div>pending</div>
  return <div>{q.data.resources.map((r) => `${r.id}:${r.state}`).join('|') || 'none'}</div>
}

describe('useDepStatus', () => {
  it('returns the mocked snapshot', async () => {
    state.depStatus = {
      resources: [
        { id: 'object_store', state: 'ok' },
        { id: 'mail_broker', state: 'unreachable' },
      ],
    }
    const { getByText } = renderWithProviders(<Probe />)
    await waitFor(() => expect(getByText('object_store:ok|mail_broker:unreachable')).toBeInTheDocument())
  })
})
