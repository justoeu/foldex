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
    await screen.findByRole('heading', { level: 1 })
  })

  it('displays the stats heading with correct title', async () => {
    renderWithProviders(<StatsPage />)
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Stats')
  })

  it('renders KPI cards section', async () => {
    renderWithProviders(<StatsPage />)
    await screen.findByRole('heading', { level: 1 })
    // KPIs are rendered as stat cards with labels
    expect(screen.getByText('Clicks · 30d')).toBeInTheDocument()
    expect(screen.getByText('Total links')).toBeInTheDocument()
    expect(screen.getByText('Top host')).toBeInTheDocument()
  })

  it('renders the daily clicks chart section', async () => {
    renderWithProviders(<StatsPage />)
    await screen.findByRole('heading', { level: 1 })
    // The daily clicks card has a title
    expect(screen.getByText('Daily clicks')).toBeInTheDocument()
  })

  it('renders the top links section', async () => {
    renderWithProviders(<StatsPage />)
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByText('Top links · 30d')).toBeInTheDocument()
  })

  it('renders the distribution by tag section', async () => {
    renderWithProviders(<StatsPage />)
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByText('Distribution by tag')).toBeInTheDocument()
  })
})
