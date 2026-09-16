import { describe, it, expect, beforeEach } from 'vitest'
import { act, waitFor } from '@testing-library/react'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import {
  fetchDepStatus,
  objectStoreSighting,
  shouldRetryObjectStoreImages,
  unreachableResources,
  useDepStatus,
  useObjectStoreGeneration,
} from './status'

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

describe('objectStoreSighting', () => {
  it('reads ok, unreachable, or absent', () => {
    expect(objectStoreSighting({ resources: [{ id: 'object_store', state: 'ok' }] })).toBe('ok')
    expect(objectStoreSighting({ resources: [{ id: 'object_store', state: 'unreachable' }] })).toBe('unreachable')
    expect(objectStoreSighting({ resources: [{ id: 'mail_broker', state: 'ok' }] })).toBe('absent')
    expect(objectStoreSighting(undefined)).toBe('absent')
  })
})

describe('shouldRetryObjectStoreImages', () => {
  it('retries only on the unreachable → ok edge', () => {
    expect(shouldRetryObjectStoreImages('unreachable', 'ok')).toBe(true)
    expect(shouldRetryObjectStoreImages('ok', 'ok')).toBe(false)
    expect(shouldRetryObjectStoreImages('absent', 'ok')).toBe(false)
    expect(shouldRetryObjectStoreImages('ok', 'unreachable')).toBe(false)
  })
})

function GenerationProbe() {
  const gen = useObjectStoreGeneration()
  return <div>gen-{gen}</div>
}

describe('useObjectStoreGeneration', () => {
  it('bumps when the object store returns', async () => {
    state.depStatus = { resources: [{ id: 'object_store', state: 'unreachable' }] }
    const { getByText, client } = renderWithProviders(<GenerationProbe />)
    await waitFor(() => expect(client.getQueryData(['status', 'deps'])).toMatchObject({
      resources: [{ id: 'object_store', state: 'unreachable' }],
    }))
    expect(getByText('gen-0')).toBeInTheDocument()
    await act(async () => {
      client.setQueryData(['status', 'deps'], { resources: [{ id: 'object_store', state: 'ok' }] })
    })
    await waitFor(() => expect(getByText('gen-1')).toBeInTheDocument())
  })
})

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
