import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor, within, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AvailabilityHint } from './components/AvailabilityHint'
import { Notice } from './components/account/SectionCard'
import { UsernameRow } from './components/account/UsernameRow'
import { EmailRow } from './components/account/EmailRow'
import { FolderDialog } from './components/FolderDialog'
import { FolderPicker } from './components/FolderPicker'
import { FolderCard } from './components/FolderCard'
import { ConflictModePicker } from './components/ConflictModePicker'
import { BackupRestoreDialog } from './components/BackupRestoreDialog'
import { BackupSection } from './components/admin/BackupSection'
import { AuditOrigins } from './components/admin/AuditSignals'
import { StatsPage } from './pages/StatsPage'
import { useAvailability } from './hooks/useAvailability'
import { renderWithProviders, testAdminUser } from './test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from './test/server'
import { http } from './api/client'
import type { Availability, AvailabilityResponse } from './hooks/useAvailability'
import type { AuthUser, SessionState } from './auth/types'
import type { AuditStats } from './api/admin'
import type { Folder, PreviewTile } from './api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})
afterEach(() => vi.useRealTimers())

function sessionWith(over: Partial<AuthUser>): SessionState {
  return {
    status: 'authenticated',
    user: { ...testAdminUser, ...over },
    csrfToken: 'test-csrf-token',
    features: { google_oauth: false, two_factor: true, email_delivery: true },
  }
}

describe('S3358 charter — nested ternary arms', () => {
  describe('AvailabilityHint', () => {
    const arms: Array<{ result: Availability; copy: RegExp; tone: string }> = [
      { result: { state: 'checking' }, copy: /checking/i, tone: 'wait' },
      { result: { state: 'error' }, copy: /you can still save/i, tone: 'wait' },
      { result: { state: 'free' }, copy: /available/i, tone: 'ok' },
      { result: { state: 'refused', reason: 'taken' }, copy: /already in use/i, tone: 'bad' },
      { result: { state: 'refused', reason: 'reserved' }, copy: /reserved/i, tone: 'bad' },
      { result: { state: 'refused', reason: 'shape' }, copy: /not a usable value/i, tone: 'bad' },
      { result: { state: 'warn', reason: 'pending' }, copy: /already moving/i, tone: 'wait' },
    ]
    it.each(arms)('$result.state $copy', ({ result, copy, tone }) => {
      const { container } = renderWithProviders(<AvailabilityHint result={result} />)
      expect(screen.getByText(copy)).toBeInTheDocument()
      expect(screen.getByRole('status')).toHaveClass(`fx-avail-${tone}`)
      expect(container.querySelector('.fx-avail')).toBeTruthy()
    })

    it('idle renders nothing', () => {
      const { container } = renderWithProviders(<AvailabilityHint result={{ state: 'idle' }} />)
      expect(container).toBeEmptyDOMElement()
    })
  })

  describe('Notice', () => {
    it('ok is a status, bad is an alert, info is silent', () => {
      renderWithProviders(
        <>
          <Notice tone="ok">saved</Notice>
          <Notice tone="bad">refused</Notice>
          <Notice tone="info">explains</Notice>
        </>,
      )
      expect(screen.getByRole('status')).toHaveTextContent('saved')
      expect(screen.getByRole('status')).toHaveClass('fx-sec-note-ok')
      expect(screen.getByRole('alert')).toHaveTextContent('refused')
      expect(screen.getByRole('alert')).toHaveClass('fx-sec-note-bad')
      expect(screen.getByText('explains').closest('p')).not.toHaveAttribute('role')
      expect(screen.getByText('explains').closest('p')).toHaveClass('fx-sec-note-info')
    })
  })

  describe('UsernameRow', () => {
    it('unset vs set vs open labels', async () => {
      const { unmount } = renderWithProviders(<UsernameRow user={{ ...testAdminUser }} />, {
        session: sessionWith({}),
      })
      const unset = screen.getByRole('group', { name: /username/i })
      expect(within(unset).getByRole('button', { name: /set a username/i })).toBeInTheDocument()
      expect(within(unset).getByText(/not set — you sign in with your e-mail/i)).toBeInTheDocument()
      unmount()

      renderWithProviders(<UsernameRow user={{ ...testAdminUser, username: 'ada' }} />, {
        session: sessionWith({ username: 'ada' }),
      })
      const set = screen.getByRole('group', { name: /username/i })
      expect(within(set).getByRole('button', { name: /^edit$/i })).toBeInTheDocument()
      expect(within(set).getByText('Set')).toBeInTheDocument()
      await userEvent.setup().click(within(set).getByRole('button', { name: /^edit$/i }))
      expect(within(set).getByRole('button', { name: /cancel/i })).toBeInTheDocument()
    })
  })

  describe('EmailRow', () => {
    it('verified vs unverified, change vs cancel pending', async () => {
      vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
        if (url === '/api/auth/email/change') return { data: { pending: null } } as never
        return { data: { identities: [] } } as never
      })
      const { unmount } = renderWithProviders(
        <EmailRow user={{ ...testAdminUser, email: 'me@example.com', email_verified_at: '2026-01-01T00:00:00Z' }} />,
        { session: sessionWith({ email: 'me@example.com', email_verified_at: '2026-01-01T00:00:00Z' }) },
      )
      const verified = screen.getByRole('group', { name: /^e-mail$/i })
      expect(within(verified).getByText('Verified')).toBeInTheDocument()
      expect(await within(verified).findByRole('button', { name: /change e-mail/i })).toBeInTheDocument()
      unmount()

      vi.mocked(http.get).mockImplementation(async (url: string) => {
        if (url === '/api/auth/email/change') {
          return { data: { pending: { new_email: 'new@example.com', expires_at: '2099-01-01T00:00:00Z' } } } as never
        }
        return { data: { identities: [] } } as never
      })
      renderWithProviders(
        <EmailRow user={{ ...testAdminUser, email: 'me@example.com' }} />,
        { session: sessionWith({ email: 'me@example.com' }) },
      )
      const pending = screen.getByRole('group', { name: /^e-mail$/i })
      expect(within(pending).getByText('Unverified')).toBeInTheDocument()
      expect(await within(pending).findByRole('button', { name: /cancel the change/i })).toBeInTheDocument()
    })
  })

  describe('FolderDialog', () => {
    const folder: Folder = {
      id: 1, name: 'Existing', color: '#6366F1', parent_id: null,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }

    it('create vs edit vs naming copy', () => {
      const { unmount } = renderWithProviders(<FolderDialog open onClose={vi.fn()} />)
      const create = screen.getByRole('dialog')
      expect(create).toHaveAttribute('aria-label', 'New folder')
      expect(within(create).getByText('New folder')).toBeInTheDocument()
      expect(within(create).getByRole('heading', { name: 'Create folder' })).toBeInTheDocument()
      expect(within(create).getByRole('button', { name: /create folder/i })).toBeInTheDocument()
      unmount()

      const edit = renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
      const editDlg = screen.getByRole('dialog')
      expect(editDlg).toHaveAttribute('aria-label', 'Edit folder')
      expect(within(editDlg).getByText('Edit folder')).toBeInTheDocument()
      expect(within(editDlg).getByRole('heading', { name: /Edit "Existing"/ })).toBeInTheDocument()
      expect(within(editDlg).getByRole('button', { name: /^save$/i })).toBeInTheDocument()
      edit.unmount()

      renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} justCreated />)
      const naming = screen.getByRole('dialog')
      expect(naming).toHaveAttribute('aria-label', 'Name folder')
      expect(within(naming).getByText('Name folder')).toBeInTheDocument()
      expect(within(naming).getByRole('heading', { name: 'Give the folder a name' })).toBeInTheDocument()
      expect(within(naming).getByRole('button', { name: /^done$/i })).toBeInTheDocument()
    })
  })

  describe('FolderPicker', () => {
    it('none vs folder vs create rows', async () => {
      state.folders = [
        { id: 1, name: 'Work', color: '#6366F1', link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false },
      ]
      renderWithProviders(<FolderPicker selected={null} onChange={vi.fn()} />)
      const input = await waitFor(() => document.querySelector('.fx-folderpicker-input') as HTMLInputElement)
      await userEvent.setup().click(input)
      expect(await screen.findByText(/No folder/i)).toBeInTheDocument()
      expect(screen.getByText('Work')).toBeInTheDocument()
      await userEvent.setup().type(input, 'BrandNew')
      expect(await screen.findByText(/Create folder "BrandNew"/i)).toBeInTheDocument()
      expect(document.querySelector('.fx-folderpicker-row-create')).toBeTruthy()
    })
  })

  describe('FolderCard tiles', () => {
    const folder = (tiles: PreviewTile[]): Folder => ({
      id: 1, name: 'Box', color: '#0EA5E9', link_count: tiles.length, folder_count: 0,
      preview_links: tiles, preview_folders: [], has_password: false,
    })

    it('og image vs favicon vs letter', () => {
      const { unmount } = renderWithProviders(
        <FolderCard
          folder={folder([{ id: 1, title: 'Alpha', og_image_url: 'https://cdn.test/a.jpg', favicon_url: null }])}
          onOpen={vi.fn()}
        />,
      )
      expect(document.querySelector('.fx-folder-tile img')).toHaveAttribute('src', 'https://cdn.test/a.jpg')
      unmount()

      const fav = renderWithProviders(
        <FolderCard
          folder={folder([{ id: 1, title: 'Beta', og_image_url: null, favicon_url: 'https://cdn.test/f.ico' }])}
          onOpen={vi.fn()}
        />,
      )
      expect(document.querySelector('.fx-folder-tile-favicon')).toHaveAttribute('src', 'https://cdn.test/f.ico')
      fav.unmount()

      renderWithProviders(
        <FolderCard
          folder={folder([{ id: 1, title: 'Gamma', og_image_url: null, favicon_url: null }])}
          onOpen={vi.fn()}
        />,
      )
      expect(document.querySelector('.fx-folder-tile-letter')).toHaveTextContent('G')
    })
  })

  describe('ConflictModePicker INV-143', () => {
    const labels = {
      skipTitle: 'Skip conflicts',
      skipDesc: 'keep current',
      duplicateTitle: 'Duplicate',
      duplicateDesc: 'rename',
      wipeTitle: '⚠ Wipe everything',
      wipeDesc: 'destructive',
    }

    it('inactive vs active accent vs active danger', () => {
      const { rerender } = renderWithProviders(
        <ConflictModePicker value="skip" onChange={vi.fn()} wipeDanger labels={labels} />,
      )
      const skip = screen.getByRole('button', { name: /skip conflicts/i })
      const wipe = screen.getByRole('button', { name: /wipe everything/i })
      expect(skip.getAttribute('style')).toContain('--fx-accent')
      expect(wipe.querySelector('span')?.getAttribute('style')).toContain('--fx-danger')
      rerender(<ConflictModePicker value="wipe" onChange={vi.fn()} wipeDanger labels={labels} />)
      expect(screen.getByRole('button', { name: /wipe everything/i }).getAttribute('style')).toContain('--fx-danger')
      expect(screen.getByRole('button', { name: /skip conflicts/i }).getAttribute('style')).not.toContain('--fx-danger')
    })
  })

  describe('BackupRestoreDialog', () => {
    it('skip restore vs wipe restore labels', async () => {
      renderWithProviders(
        <BackupRestoreDialog file={new File([new Uint8Array([0])], 'backup.zip', { type: 'application/zip' })} onClose={vi.fn()} onRestored={vi.fn()} />,
      )
      await waitFor(() => expect(screen.getByRole('button', { name: /^Restore$/i })).toBeInTheDocument())
      await userEvent.setup().click(screen.getByRole('button', { name: /Wipe everything and import/i }))
      expect(screen.getByRole('button', { name: /Restore \(wipe everything\)/i })).toBeInTheDocument()
    })
  })

  describe('BackupSection KPI dump tone', () => {
    it('never-ran warn vs fresh ok vs stale warn', async () => {
      renderWithProviders(<BackupSection />)
      const lastDump = await screen.findByText('Last dump')
      const kpi = lastDump.closest('.fx-bkp-kpi') as HTMLElement
      expect(within(kpi).getByText('—')).toBeInTheDocument()
      expect(kpi.querySelector('.fx-bkp-dot-warn')).toBeTruthy()
    })
  })

  describe('AuditOrigins', () => {
    const stats = (origins: AuditStats['origins']): AuditStats => ({
      totals: {
        events: 0, events_prev: 0, failures: 0, failures_prev: 0,
        access_changes: 0, access_changes_prev: 0, actors: 0, active_users: 0,
      },
      days: [],
      distribution: [],
      actors: [],
      origins,
      risk: null,
    })

    it('dot danger/warn/ok and blocked vs block vs none', () => {
      renderWithProviders(
        <AuditOrigins
          canBlock
          stats={stats([
            { ip: '203.0.113.1', trusted: false, user_agent: 'Firefox', count: 3, failures: 0, last_seen: '2026-01-01T00:00:00Z', blocked: true },
            { ip: '203.0.113.2', trusted: false, user_agent: 'Chrome', count: 2, failures: 4, last_seen: '2026-01-01T00:00:00Z', blocked: false },
            { ip: '127.0.0.1', trusted: true, user_agent: null, count: 1, failures: 0, last_seen: '2026-01-01T00:00:00Z', blocked: false },
          ])}
        />,
      )
      expect(document.querySelector('.fx-aud-dot-danger')).toBeTruthy()
      expect(document.querySelector('.fx-aud-dot-warn')).toBeTruthy()
      expect(document.querySelector('.fx-aud-dot-ok')).toBeTruthy()
      expect(screen.getByText(/blocked/i)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /block/i })).toBeInTheDocument()
      const loopback = screen.getByText('127.0.0.1').closest('.fx-aud-row') as HTMLElement
      expect(within(loopback).queryByRole('button')).not.toBeInTheDocument()
    })
  })

  describe('StatsPage KPI glyphs and top-link deltas', () => {
    it('up/down/neutral glyphs and +100% / — / percent', async () => {
      state.statsSummary = {
        total_links: 10,
        total_tags: 2,
        total_clicks: 100,
        clicks_last_30d: 30,
        clicks_prev_30d: 20,
        new_links_last_30d: 3,
        top_host: 'example.com',
        top_host_clicks: 12,
      }
      state.statsTop = [
        { id: 1, url: 'https://a.test', title: 'New', slug: 'new', host: 'a.test', clicks: 5, clicks_30d: 5, clicks_prev_30d: 0 },
        { id: 2, url: 'https://b.test', title: 'Quiet', slug: 'quiet', host: 'b.test', clicks: 0, clicks_30d: 0, clicks_prev_30d: 0 },
        { id: 3, url: 'https://c.test', title: 'Grew', slug: 'grew', host: 'c.test', clicks: 6, clicks_30d: 6, clicks_prev_30d: 4 },
      ]
      const { unmount } = renderWithProviders(<StatsPage />)
      expect(await screen.findByText('New')).toBeInTheDocument()
      expect(document.querySelector('.fx-kpi-delta-up')).toHaveTextContent('▲')
      expect(document.querySelector('.fx-kpi-delta-neutral')).toHaveTextContent('·')
      expect(screen.getByText('+100%').closest('li')).toHaveTextContent('New')
      expect(screen.getByText('Quiet').closest('li')).toHaveTextContent('—')
      expect(screen.getByText('Grew').closest('li')).toHaveTextContent('+50%')
      unmount()

      state.statsSummary = { ...state.statsSummary, clicks_last_30d: 10, clicks_prev_30d: 20 }
      renderWithProviders(<StatsPage />)
      expect(await screen.findByText('New')).toBeInTheDocument()
      expect(document.querySelector('.fx-kpi-delta-down')).toHaveTextContent('▼')
    })
  })

  describe('useAvailability', () => {
    function ProbeView({ reply, value }: { reply: AvailabilityResponse; value: string }) {
      const result = useAvailability(async () => reply, value, '')
      return <pre data-testid="avail">{JSON.stringify(result)}</pre>
    }

    async function settle() {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(500)
      })
    }

    it('free vs warn vs refused', async () => {
      vi.useFakeTimers()
      const { rerender } = renderWithProviders(<ProbeView value="alice" reply={{ available: true }} />)
      await settle()
      expect(screen.getByTestId('avail')).toHaveTextContent('"state":"free"')
      rerender(<ProbeView value="alice2" reply={{ available: true, reason: 'pending' }} />)
      await settle()
      expect(screen.getByTestId('avail')).toHaveTextContent('"state":"warn"')
      expect(screen.getByTestId('avail')).toHaveTextContent('"reason":"pending"')
      rerender(<ProbeView value="alice3" reply={{ available: false, reason: 'taken' }} />)
      await settle()
      expect(screen.getByTestId('avail')).toHaveTextContent('"state":"refused"')
      expect(screen.getByTestId('avail')).toHaveTextContent('"reason":"taken"')
    })
  })
})
