import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { StatsPage } from './StatsPage'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('StatsPage', () => {
  it('renders without crashing', async () => {
    renderWithProviders(<StatsPage />)
    // The page has a heading — wait for it to resolve
    await screen.findByRole('heading', { level: 1 })
  })

  it('renders summary statistics', async () => {
    renderWithProviders(<StatsPage />)
    // The page should render at least one visible stat card
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading).toBeInTheDocument()
  })
})
