import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { StatsPage, formatChartDate } from './StatsPage'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  const fallback = vi.mocked(http.get).getMockImplementation()!
  vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
    if (url.startsWith('/api/stats/dashboard')) {
      return {
        data: {
          summary: state.statsSummary ?? {
            total_links: 0, total_tags: 0, total_clicks: 0,
            clicks_last_30d: 0, clicks_prev_30d: 0, new_links_last_30d: 0,
            top_host: '', top_host_clicks: 0,
          },
          daily: state.statsDaily ?? [],
          top: state.statsTop ?? [],
          tags: state.statsTags ?? [],
        },
      }
    }
    return fallback(url, ...rest)
  }) as never)
})

function seedStats(opts: {
  mom?: 'up' | 'down' | 'zero-prev' | 'zero-both'
  withChart?: boolean
  withTop?: boolean
  withTags?: boolean
  storage?: 'ok' | 'error' | 'bytes-small' | 'bytes-gb'
} = {}) {
  const mom = opts.mom ?? 'up'
  const clicksLast = mom === 'zero-both' ? 0 : mom === 'down' ? 40 : 120
  const clicksPrev = mom === 'zero-prev' || mom === 'zero-both' ? 0 : mom === 'down' ? 80 : 100
  state.statsSummary = {
    total_links: 10,
    total_tags: 4,
    total_clicks: 500,
    clicks_last_30d: clicksLast,
    clicks_prev_30d: clicksPrev,
    new_links_last_30d: 3,
    top_host: 'example.com',
    top_host_clicks: 42,
  }
  if (opts.withChart !== false) {
    state.statsDaily = Array.from({ length: 14 }, (_, i) => ({
      date: `2026-01-${String(i + 1).padStart(2, '0')}`,
      clicks: i === 0 ? 0 : i * 3,
    }))
  } else {
    state.statsDaily = []
  }
  if (opts.withTop !== false) {
    state.statsTop = [
      {
        id: 1, url: 'https://a.example', title: 'Alpha link', slug: 'alpha',
        host: 'a.example', clicks: 50, clicks_30d: 20, clicks_prev_30d: 10,
      },
      {
        id: 2, url: 'https://b.example', title: 'Beta link', slug: 'beta',
        host: 'b.example', clicks: 30, clicks_30d: 5, clicks_prev_30d: 0,
      },
      {
        id: 3, url: 'https://c.example', title: 'Gamma link', slug: 'gamma',
        host: 'c.example', clicks: 10, clicks_30d: 0, clicks_prev_30d: 0,
      },
      {
        id: 4, url: 'https://d.example', title: 'Delta link', slug: 'delta',
        host: 'd.example', clicks: 8, clicks_30d: 4, clicks_prev_30d: 8,
      },
    ]
  } else {
    state.statsTop = []
  }
  if (opts.withTags !== false) {
    state.statsTags = [
      { id: 1, name: 'work', color: '#6366F1', clicks: 40, links: 3 },
      { id: 2, name: 'play', color: '#10B981', clicks: 15, links: 2 },
    ]
  } else {
    state.statsTags = []
  }
  if (opts.storage === 'error') {
    state.statsStorageError = true
  } else if (opts.storage === 'bytes-small') {
    state.statsStorage = { objects: 2, total_bytes: 512 }
  } else if (opts.storage === 'bytes-gb') {
    state.statsStorage = { objects: 99, total_bytes: 2.5 * 1024 * 1024 * 1024 }
  } else {
    state.statsStorage = { objects: 12, total_bytes: 1536 * 1024 }
  }
}

describe('StatsPage', () => {
  it('renders empty chart/list placeholders when there is no data', async () => {
    seedStats({ withChart: false, withTop: false, withTags: false, mom: 'zero-both' })
    state.statsSummary = {
      total_links: 0, total_tags: 0, total_clicks: 0,
      clicks_last_30d: 0, clicks_prev_30d: 0, new_links_last_30d: 0,
      top_host: '', top_host_clicks: 0,
    }
    renderWithProviders(<StatsPage />)
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByText('No clicks in the last 60 days')).toBeInTheDocument()
    expect(screen.getByText('Register your first links')).toBeInTheDocument()
    expect(screen.getByText('Create tags to see distribution')).toBeInTheDocument()
  })

  it('renders KPI values, sparkline, MoM bars, top links and tag distribution', async () => {
    seedStats({ mom: 'up' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText('example.com')).toBeInTheDocument())
    expect(screen.getByText('Alpha link')).toBeInTheDocument()
    expect(screen.getByText('Beta link')).toBeInTheDocument()
    expect(screen.getByText('work')).toBeInTheDocument()
    expect(screen.getByText('play')).toBeInTheDocument()
    expect(document.querySelector('.fx-spark')).toBeTruthy()
    expect(document.querySelector('.fx-chart')).toBeTruthy()
    expect(document.querySelector('.fx-mom')).toBeTruthy()
    expect(screen.getAllByText(/\+20%/).length).toBeGreaterThan(0)
    const paths = vi.mocked(http.get).mock.calls.map(([url]) => String(url).split('?')[0])
    expect(paths.filter((path) => path === '/api/stats/dashboard')).toHaveLength(1)
    expect(paths).not.toContain('/api/stats/summary')
    expect(paths).not.toContain('/api/stats/daily')
    expect(paths).not.toContain('/api/stats/top')
    expect(paths).not.toContain('/api/stats/tags')
  })

  it('passes numeric count into i18n for section subtitle (not toLocaleString)', async () => {
    // 14 days × (0 + 3+6+...+39) = sum i*3 for i=1..13 = 3*(13*14/2) = 273
    seedStats({ mom: 'up' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText(/Last 60 days · total 273/)).toBeInTheDocument())
    // MoM delta is numeric too (120 - 100 = 20)
    expect(screen.getByText(/Δ 20 clicks/)).toBeInTheDocument()
  })

  it('shows negative MoM delta when clicks fell', async () => {
    seedStats({ mom: 'down' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(document.querySelector('.fx-kpi-delta-down')).toBeTruthy())
    expect(screen.getAllByText(/-50%/).length).toBeGreaterThan(0)
  })

  it('treats zero previous baseline as +100% when current > 0', async () => {
    seedStats({ mom: 'zero-prev' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getAllByText(/\+100%/).length).toBeGreaterThan(0))
  })

  it('formats storage bytes and handles RustFS unavailable', async () => {
    seedStats({ storage: 'bytes-gb' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText('99')).toBeInTheDocument())
    expect(screen.getByText(/2\.5 GB/)).toBeInTheDocument()
  })

  it('shows storage unavailable when the endpoint fails', async () => {
    seedStats({ storage: 'error' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText(/RustFS unavailable/i)).toBeInTheDocument())
  })

  it('shows B unit for tiny storage totals', async () => {
    seedStats({ storage: 'bytes-small' })
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText(/512 B/)).toBeInTheDocument())
  })

  it('shows top-link delta branches (+pct, +100%, —, and down)', async () => {
    seedStats()
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(screen.getByText('Alpha link')).toBeInTheDocument())
    // Alpha: 20 vs 10 → +100%; Beta: prev 0 curr 5 → +100%; Gamma: both 0 → —; Delta: 4 vs 8 → -50%
    expect(screen.getByText('Gamma link').closest('li')?.textContent).toMatch(/—/)
    expect(screen.getByText('Delta link').closest('li')?.textContent).toMatch(/-50%/)
    expect(document.querySelector('.fx-toplink-delta-down')).toBeTruthy()
  })

  it('shows chart tooltip on hover including edge positions', async () => {
    seedStats()
    renderWithProviders(<StatsPage />)
    await waitFor(() => expect(document.querySelector('.fx-chart')).toBeTruthy())
    const zones = document.querySelectorAll('.fx-chart rect')
    expect(zones.length).toBeGreaterThan(2)
    fireEvent.mouseEnter(zones[0])
    await waitFor(() => expect(document.querySelector('.fx-chart-tooltip')).toBeInTheDocument())
    expect(document.querySelector('.fx-chart-tooltip')?.textContent).toMatch(/click/i)
    fireEvent.mouseLeave(zones[0])
    await waitFor(() => expect(document.querySelector('.fx-chart-tooltip')).toBeNull())

    fireEvent.mouseEnter(zones[zones.length - 1])
    await waitFor(() => expect(document.querySelector('.fx-chart-tooltip')).toBeInTheDocument())
    fireEvent.mouseLeave(zones[zones.length - 1])

    const mid = Math.floor(zones.length / 2)
    fireEvent.mouseEnter(zones[mid])
    await waitFor(() => expect(document.querySelector('.fx-chart-tooltip')).toBeInTheDocument())
  })

  it('renders the stats heading and section titles', async () => {
    seedStats()
    renderWithProviders(<StatsPage />)
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Stats')
    expect(screen.getByText('Clicks · 30d')).toBeInTheDocument()
    expect(screen.getByText('Total links')).toBeInTheDocument()
    expect(screen.getByText('Top host')).toBeInTheDocument()
    expect(screen.getByText('Daily clicks')).toBeInTheDocument()
    expect(screen.getByText('Top links · 30d')).toBeInTheDocument()
    expect(screen.getByText('Distribution by tag')).toBeInTheDocument()
  })
})

describe('formatChartDate', () => {
  it('parses YYYY-MM-DD as local calendar date (no UTC shift)', () => {
    const out = formatChartDate('2026-01-15')
    expect(out).toMatch(/15/)
    expect(out).toMatch(/·/)
  })

  it('handles ISO datetime by taking the date prefix', () => {
    const out = formatChartDate('2026-06-01T00:00:00Z')
    expect(out).toMatch(/1 /)
  })

  it('falls back to Date parse for non-ISO strings', () => {
    const out = formatChartDate('not-a-date')
    expect(typeof out).toBe('string')
  })
})
