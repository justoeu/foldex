import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LinkDialog } from './LinkDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Link } from '../api/types'
import { _clearUrlMetadataCacheForTests } from '../api/links'
import { http } from '../api/client'
import { INLINE_PALETTE } from '../lib/inlinePalette'

// A pending chip's colour is SUGGESTED now (a palette entry nothing else is
// using), so pinning an index would assert the old fixed default rather than
// the payload shape these tests are about.
const paletteRe = new RegExp(`^(${INLINE_PALETTE.join('|')})$`)


let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
  installAxiosMock(state)
  // Tests must NOT inherit cached metadata from a previous case — without
  // this reset, AUTO-FILL tests that share the same URL string would
  // silently get a cache hit and skip the mock route entirely.
  _clearUrlMetadataCacheForTests()
})

const METADATA_DEBOUNCE_MS = 500

afterEach(() => vi.useRealTimers())

async function advanceMetadataDebounce(ms = METADATA_DEBOUNCE_MS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

async function expectNoMetadataFetch() {
  const before = state.urlMetadataCalls.length
  await advanceMetadataDebounce()
  expect(state.urlMetadataCalls.length).toBe(before)
}

describe('LinkDialog', () => {
  it('does not show content when closed', () => {
    renderWithProviders(<LinkDialog open={false} link={null} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('creates a link with selected existing tag', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://example.com')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].url).toBe('https://example.com')
    expect(onClose).toHaveBeenCalled()
  })

  it('edits an existing link', async () => {
    const link: Link = {
      id: 7, url: 'https://x', title: 'old', slug: 'old', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link
    state.links.push(link)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    await user.clear(titleInput)
    await user.type(titleInput, 'renamed')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(state.links[0].title).toBe('renamed'))
    expect(onClose).toHaveBeenCalled()
  })

  it('sends the loaded version and keeps the edit open with translated guidance on conflict', async () => {
    const updatedAt = '2026-08-13T11:00:00Z'
    const link: Link = {
      id: 7, url: 'https://x', title: 'old', slug: 'old', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: updatedAt, tags: [],
    } as Link
    state.links.push(link)
    const conflict = Object.assign(new Error('conflict'), {
      response: { status: 409, data: { error: { message: 'link was modified; refetch and retry' } } },
    })
    vi.mocked(http.patch).mockRejectedValue(conflict)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)

    await userEvent.setup().click(screen.getByRole('button', { name: /Save changes/i }))

    expect(await screen.findByText(/changed elsewhere.*reload/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(vi.mocked(http.patch).mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({ if_match_updated_at: updatedAt }),
    )
  })

  it('Cancel closes without saving', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
    expect(state.links).toHaveLength(0)
  })

  it('uses initialUrl when no link is passed', () => {
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://pre" onClose={vi.fn()} />)
    expect((screen.getByRole('textbox', { name: /^URL$/i }) as HTMLInputElement).value).toBe('https://pre')
  })

  it('resets dirty slug and pending tags when switching link identity', async () => {
    const secondTag = { id: 2, name: 'second-tag', color: '#16a34a', icon: null }
    state.tags.push(secondTag)
    const first = {
      id: 7, url: 'https://first.example', title: 'First', slug: 'first', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [state.tags[0]],
    } as Link
    const second = {
      id: 8, url: 'https://second.example', title: 'Second', slug: 'second', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [secondTag],
    } as Link
    const rendered = renderWithProviders(<LinkDialog open link={first} onClose={vi.fn()} />)
    const slug = screen.getByRole('textbox', { name: /short url slug/i })
    fireEvent.change(slug, { target: { value: 'dirty-first' } })
    const tagsInput = screen.getByLabelText('tag filter')
    await userEvent.type(tagsInput, 'pending-first')
    await userEvent.keyboard('{Enter}')
    expect(screen.getByText('pending-first')).toBeInTheDocument()

    rendered.rerender(<LinkDialog open link={second} onClose={vi.fn()} />)

    await waitFor(() => expect(slug).toHaveValue('second'))
    expect(screen.queryByText('pending-first')).not.toBeInTheDocument()
    expect(document.querySelector('.fx-tagpicker')).toHaveTextContent('second-tag')
    expect(document.querySelector('.fx-tagpicker')).not.toHaveTextContent('jira')
  })

  it('updates the create destination when its default folder changes', async () => {
    state.folders.push(
      { id: 10, name: 'Alpha', color: '#111111', parent_id: null } as MockState['folders'][number],
      { id: 20, name: 'Beta', color: '#222222', parent_id: null } as MockState['folders'][number],
    )
    const rendered = renderWithProviders(
      <LinkDialog open link={null} defaultFolderId={10} onClose={vi.fn()} />,
    )
    const folderInput = screen.getByRole('textbox', { name: /^folder$/i })
    await waitFor(() => expect(folderInput).toHaveAttribute('placeholder', 'Alpha'))

    rendered.rerender(<LinkDialog open link={null} defaultFolderId={20} onClose={vi.fn()} />)

    await waitFor(() => expect(folderInput).toHaveAttribute('placeholder', 'Beta'))
  })

  it('disables save when URL is empty', () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Save link/i })).toBeDisabled()
  })

  it('sends existing tag IDs and cycled pending definitions in the link request', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()

    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'j')
    await user.click(await screen.findByText('jira'))
    await user.type(tagsInput, 'brand-new')
    await user.keyboard('{Enter}')
    await user.click(screen.getByText('brand-new'))
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.tags.some((t) => t.name === 'brand-new')).toBe(true))
    expect(state.links).toHaveLength(1)

    const parentCall = vi.mocked(http.post).mock.calls.find(([url]) => url === '/api/links')
    expect(parentCall?.[1]).toEqual(expect.objectContaining({
      tag_ids: [1],
      pending_tags: [{ name: 'brand-new', color: expect.stringMatching(paletteRe), icon: null }],
    }))
    expect(vi.mocked(http.post).mock.calls.some(([url]) => url === '/api/tags')).toBe(false)
  })

  it('does not create a standalone tag when the link request fails', async () => {
    const originalPost = vi.mocked(http.post).getMockImplementation()!
    const failure = Object.assign(new Error('save failed'), {
      response: { status: 500, data: { error: { code: 'server_error', message: 'save failed' } } },
    })
    vi.mocked(http.post).mockImplementation(((url: string, ...args: unknown[]) => {
      if (url === '/api/links') return Promise.reject(failure)
      return originalPost(url, ...args)
    }) as typeof http.post)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()

    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://fail.example')
    await user.type(screen.getByLabelText('tag filter'), 'must-not-orphan')
    await user.keyboard('{Enter}')
    await user.click(screen.getByRole('button', { name: /Save link/i }))

    expect(await screen.findByText('save failed')).toBeInTheDocument()
    expect(state.tags.some((tag) => tag.name === 'must-not-orphan')).toBe(false)
    expect(vi.mocked(http.post).mock.calls.some(([url]) => url === '/api/tags')).toBe(false)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('updates a link with pending tags in the same version-matched request', async () => {
    const link: Link = {
      id: 8, url: 'https://atomic.example', title: 'Atomic', slug: 'atomic', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: 'version-8', tags: [],
    } as Link
    state.links.push(link)
    renderWithProviders(<LinkDialog open link={link} onClose={vi.fn()} />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText('tag filter'), 'pending-update')
    await user.keyboard('{Enter}')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))

    await waitFor(() => expect(vi.mocked(http.patch)).toHaveBeenCalled())
    expect(vi.mocked(http.patch).mock.calls[0]?.[1]).toEqual(expect.objectContaining({
      if_match_updated_at: 'version-8',
      tag_ids: [],
      pending_tags: [{ name: 'pending-update', color: expect.stringMatching(paletteRe), icon: null }],
    }))
    expect(vi.mocked(http.post).mock.calls.some(([url]) => url === '/api/tags')).toBe(false)
  })

  it('picks an existing tag from the suggestions', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://y')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'j')
    const jiraChip = await screen.findByText('jira')
    await user.click(jiraChip)
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].tags[0].name).toBe('jira')
  })

  // ─── change-detection select (Phase 5) ─────────────────────────────────
  // The select drives link.check_interval — null/empty = opt-out,
  // 'hourly'/'daily'/'weekly' = opt-in. We assert each value lands on the
  // POST/PATCH body so the backend's tri-state DTO receives the explicit
  // value (or null) rather than "field absent".

  it('CREATE: ships check_interval=null when the select stays at "Disabled"', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x.test/a')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    // null in body == backend opt-out (default).
    expect(state.links[0].check_interval ?? null).toBeNull()
  })

  it('CREATE: ships check_interval=daily when the user picks "Every day"', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x.test/b')
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, 'daily')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].check_interval).toBe('daily')
  })

  it.each(['hourly', 'weekly'] as const)('CREATE: ships check_interval=%s', async (interval) => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), `https://x.test/${interval}`)
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, interval)
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].check_interval).toBe(interval)
  })

  it('EDIT: setting "Disabled" sends check_interval=null on PATCH', async () => {
    // Seed an opted-in link, open it for edit, switch the select to off.
    state.links = [{
      id: 42,
      url: 'https://x.test/edit',
      title: 'editme',
      slug: 'editme',
      description: null,
      favicon_url: null,
      og_image_url: null,
      click_count: 0,
      preview_status: 'ok',
      preview_error: null,
      last_clicked_at: null,
      pinned: false,
      folder_id: null,
      created_at: '',
      updated_at: '',
      check_interval: 'daily',
      tags: [],
    }]
    renderWithProviders(<LinkDialog open link={state.links[0]} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    expect((select as HTMLSelectElement).value).toBe('daily')
    await user.selectOptions(select, '')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(state.links[0].check_interval ?? null).toBeNull())
  })

  // Next-check preview hint — locks the conditional render so removing the
  // span fails a test rather than silently dropping the UX.
  it('NEXT-CHECK PREVIEW: hidden when interval stays "Disabled"', () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    expect(screen.queryByTestId('check-next-preview')).not.toBeInTheDocument()
  })

  it('NEXT-CHECK PREVIEW: appears when user picks an interval', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, 'daily')
    const hint = await screen.findByTestId('check-next-preview')
    // A fresh create (no last_checked_at) always renders the "soon" copy.
    expect(hint.textContent).toMatch(/Next check:/i)
    expect(hint.textContent).toMatch(/soon/i)
  })

  // ─── auto-fill from /api/links/url-metadata (v1.3) ────────────────────────
  // Contract: when the user types/pastes a URL, after 500ms of idle the
  // dialog calls the metadata endpoint and pre-fills empty Title/Description
  // fields. User-typed content is NEVER overwritten. Edit mode is skipped
  // entirely (the existing link already has its own copy).

  it('AUTO-FILL: fetches metadata after debounce and prefills empty title', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'Hacker News', description: 'Tech news' }
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    fireEvent.change(screen.getByRole('textbox', { name: /^URL$/i }), {
      target: { value: 'https://news.ycombinator.com' },
    })

    await advanceMetadataDebounce(METADATA_DEBOUNCE_MS - 1)
    expect(state.urlMetadataCalls).toEqual([])
    await advanceMetadataDebounce(1)
    expect(state.urlMetadataCalls).toEqual(['https://news.ycombinator.com'])

    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('Hacker News')

    // Description should also have been pre-filled since the user left it empty.
    const desc = screen.getByRole('textbox', { name: /description/i }) as HTMLTextAreaElement
    expect(desc.value).toBe('Tech news')
  })

  it('AUTO-FILL: never overwrites user-typed title', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'Auto Title', description: 'Auto Desc' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://example.com" onClose={vi.fn()} />)
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    fireEvent.change(titleInput, { target: { value: 'my custom title' } })
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://example.com'])
    expect(titleInput.value).toBe('my custom title')
  })

  it('AUTO-FILL: does not fire for invalid-looking URLs', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'should not see this' }
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    fireEvent.change(screen.getByRole('textbox', { name: /^URL$/i }), { target: { value: 'hello world' } })
    await expectNoMetadataFetch()
  })

  it('AUTO-FILL: skipped entirely in edit mode', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'should not see this' }
    const link: Link = {
      id: 99,
      url: 'https://existing.example',
      title: 'existing title',
      slug: 'existing',
      description: 'existing desc',
      favicon_url: null,
      og_image_url: null,
      folder_id: null,
      click_count: 0,
      preview_status: 'ok',
      pinned: false,
      preview_error: null,
      last_clicked_at: null,
      created_at: '',
      updated_at: '',
      check_interval: null,
      tags: [],
    }
    state.links.push(link)
    renderWithProviders(<LinkDialog open link={link} onClose={vi.fn()} />)
    await expectNoMetadataFetch()
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('existing title')
  })

  it('AUTO-FILL: rapid typing coalesces into a single request with the final URL', async () => {
    vi.useFakeTimers()
    // Locks the load-bearing behavior of the debounce: a fast typist hitting
    // multiple keys within the 500ms window must NOT trigger a request per
    // keystroke — only the LAST URL value should hit the network. A regression
    // that drops the `clearTimeout` in the effect's cleanup would issue one
    // fetch per keystroke.
    state.urlMetadata = { title: 'Final' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://a.example" onClose={vi.fn()} />)
    // Mutate URL synchronously a few times — each fireEvent.change flushes
    // React state in a microtask, so the effect re-runs and resets the timer
    // BEFORE the previous 500ms timer ever fires.
    const urlInput = screen.getByRole('textbox', { name: /^URL$/i }) as HTMLInputElement
    fireEvent.change(urlInput, { target: { value: 'https://b.example' } })
    fireEvent.change(urlInput, { target: { value: 'https://c.example' } })
    fireEvent.change(urlInput, { target: { value: 'https://d.example' } })

    await advanceMetadataDebounce()
    // Critical assertion: exactly ONE fetch happened, and it used the FINAL
    // URL — never the intermediates.
    expect(state.urlMetadataCalls).toEqual(['https://d.example'])
  })

  it('AUTO-FILL: aborts in-flight fetch when dialog closes mid-debounce', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'Should never apply' }
    const { unmount } = renderWithProviders(<LinkDialog open link={null} initialUrl="https://abort.example" onClose={vi.fn()} />)
    unmount()
    await expectNoMetadataFetch()
  })

  it('AUTO-FILL: never overwrites user-typed description either', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'Auto Title', description: 'Auto Desc' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://example.com" onClose={vi.fn()} />)
    const desc = screen.getByRole('textbox', { name: /description/i }) as HTMLTextAreaElement
    fireEvent.change(desc, { target: { value: 'my custom desc' } })
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://example.com'])
    expect(desc.value).toBe('my custom desc')
  })

  it('AUTO-FILL: in-memory cache dedups the same URL across dialog mounts', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: 'Cached Title' }
    const { unmount } = renderWithProviders(
      <LinkDialog open link={null} initialUrl="https://cache-me.example" onClose={vi.fn()} />,
    )
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toHaveLength(1)
    unmount()

    renderWithProviders(<LinkDialog open link={null} initialUrl="https://cache-me.example" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toHaveLength(1)
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('Cached Title')
  })

  it('AUTO-FILL: cache key is the URL — distinct URLs each fetch once', async () => {
    vi.useFakeTimers()
    // Defensive: locks that the cache lookup uses the URL as key. A bug that
    // ignored the key (e.g. memoizing on something stable like a constant)
    // would return the FIRST URL's metadata for the second URL — silently
    // mislabeling links.
    state.urlMetadata = { title: 'Title A' }
    const { unmount } = renderWithProviders(
      <LinkDialog open link={null} initialUrl="https://a.example" onClose={vi.fn()} />,
    )
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://a.example'])
    unmount()

    state.urlMetadata = { title: 'Title B' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://b.example" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://a.example', 'https://b.example'])
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('Title B')
  })

  it('AUTO-FILL: empty 200 (site blocked fetch) falls back to hostname', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: '', description: '' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://blocked.example/path" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('blocked.example')
    expect(screen.getByRole('button', { name: /Save link/i })).toBeEnabled()
  })

  it('AUTO-FILL: tolerates a 502 from the backend silently', async () => {
    vi.useFakeTimers()
    state.urlMetadataError = Object.assign(new Error('fetch_failed'), {
      response: { status: 502, data: { error: { code: 'fetch_failed', message: 'could not fetch URL metadata' } } },
    })
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    fireEvent.change(screen.getByRole('textbox', { name: /^URL$/i }), {
      target: { value: 'https://broken.example' },
    })

    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://broken.example'])

    // Failure is silent — title stays empty, no error chip rendered, save
    // button stays enabled (URL is present).
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('')
    expect(screen.getByRole('button', { name: /Save link/i })).toBeEnabled()
  })

  it('AUTO-FILL: empty page title does not overwrite a typed title', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: '', description: '' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://empty-title.example" onClose={vi.fn()} />)
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    fireEvent.change(titleInput, { target: { value: 'my custom title' } })
    await advanceMetadataDebounce()
    expect(titleInput.value).toBe('my custom title')
  })

  it('AUTO-FILL: empty page title falls back to the hostname', async () => {
    vi.useFakeTimers()
    state.urlMetadata = { title: '', description: '' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://www.s3-console.prontyx.com/auth/" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('s3-console.prontyx.com')
  })

  it('AUTO-FILL: shows the og:image from metadata in the preview panel', async () => {
    vi.useFakeTimers()
    state.urlMetadata = {
      title: 'Console',
      og_image_url: 'https://cdn.example/og.png',
    }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://cdn.example" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    expect(document.querySelector('.fx-modal-side-ogimg')).toHaveAttribute('src', 'https://cdn.example/og.png')
  })

  it('AUTO-FILL: shows failed hint under empty title after a real error', async () => {
    vi.useFakeTimers()
    state.urlMetadataError = Object.assign(new Error('fetch_failed'), {
      code: 'ERR_BAD_RESPONSE',
      response: { status: 502, data: { error: { code: 'fetch_failed', message: 'could not fetch' } } },
    })
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://blocked.example" onClose={vi.fn()} />)
    await advanceMetadataDebounce()
    expect(state.urlMetadataCalls).toEqual(['https://blocked.example'])
    expect(screen.getByText(/could not auto-fill/i)).toBeInTheDocument()
  })

  it('refuses to render an unsafe og_image_url as an img', () => {
    const link: Link = {
      id: 9, url: 'https://poison.example', title: 'poisoned', slug: 'poisoned', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
      og_image_url: 'javascript:alert(1)', description: null, favicon_url: null,
    } as Link
    renderWithProviders(<LinkDialog open link={link} onClose={vi.fn()} />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(document.querySelector('.fx-modal-side-ogimg')).toBeNull()
    expect(document.querySelector('img')).toBeNull()
  })

  it('stages an image file via the hidden input and uploads on create', async () => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:preview')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://img.example')
    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File(['png'], 'cover.png', { type: 'image/png' })
    await user.upload(fileInput, file)
    expect(screen.getByText(/will be saved with the link/i)).toBeInTheDocument()
    expect(document.querySelector('.fx-modal-side-ogimg')).toHaveAttribute('src', 'blob:preview')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    await waitFor(() => expect(state.links[0].og_image_url).toContain('/api/files/links/'))
    expect(onClose).toHaveBeenCalled()
  })

  it('rejects non-image files in the upload zone', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File(['x'], 'notes.txt', { type: 'text/plain' })
    fireEvent.change(fileInput, { target: { files: [file] } })
    expect(await screen.findByText(/must be an image/i)).toBeInTheDocument()
  })

  it('accepts a dropped image file on the upload zone', async () => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:drop')
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const zone = document.querySelector('.fx-img-upload-zone') as HTMLElement
    const file = new File(['x'], 'a.png', { type: 'image/png' })
    fireEvent.dragOver(zone)
    expect(zone.className).toMatch(/drag/)
    fireEvent.dragLeave(zone)
    fireEvent.drop(zone, { dataTransfer: { files: [file] } })
    expect(screen.getByText(/will be saved with the link/i)).toBeInTheDocument()
  })

  it('removes an existing og image on edit save when user clicks remove', async () => {
    const link: Link = {
      id: 3, url: 'https://has-img', title: 'Has img', slug: 'has-img', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
      og_image_url: '/api/files/links/3.jpg', description: null, favicon_url: null,
    } as Link
    state.links.push(link)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /remove image/i }))
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(state.links[0].og_image_url).toBeNull())
    expect(onClose).toHaveBeenCalled()
  })

  it('surfaces image-remove errors and keeps the dialog open', async () => {
    const link: Link = {
      id: 3, url: 'https://has-img', title: 'Has img', slug: 'has-img', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
      og_image_url: '/api/files/links/3.jpg', description: null, favicon_url: null,
    } as Link
    state.links.push(link)
    state.linkImageRemoveError = { message: 'disk full' }
    const onClose = vi.fn()
    const { unmount } = renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /remove image/i }))
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    expect(await screen.findByText(/disk full/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(state.links[0].og_image_url).toBe('/api/files/links/3.jpg')
    unmount()
    state.linkImageRemoveError = undefined
  })

  it('blocks save and offers to edit when the URL is already bookmarked', async () => {
    state.folders.push({
      id: 9, name: 'Server Local', color: '#000', parent_id: null, has_password: false,
      link_count: 1, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.links.push({
      id: 1, url: 'https://dup.example', title: 'Dup', slug: 'dup', click_count: 0,
      preview_status: 'ok', pinned: false, folder_id: 9, created_at: '', updated_at: '', tags: [],
    } as Link)
    const onOpenExisting = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} onOpenExisting={onOpenExisting} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://dup.example')
    expect(await screen.findByText(/already bookmarked/i)).toBeInTheDocument()
    expect(screen.getByText(/saved in server local/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save link/i })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: /edit saved link/i }))
    expect(onOpenExisting).toHaveBeenCalledWith(expect.objectContaining({ id: 1, folder_id: 9 }))
  })

  it('closes after save even if screenshot capture fails', async () => {
    state.linkScreenshotError = { code: 'private_target', message: 'private' }
    const onClose = vi.fn()
    const post = vi.spyOn(http, 'post')
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://ok.example')
    await user.click(screen.getByRole('button', { name: /capture page screenshot/i }))
    await user.click(screen.getByRole('button', { name: /save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(post.mock.calls.some(([url]) => String(url).includes('/screenshot'))).toBe(true)
    expect(onClose).toHaveBeenCalled()
  })

  it('still surfaces url_taken if save races the uniqueness probe', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://race.example" onClose={onClose} />)
    state.links.push({
      id: 1, url: 'https://race.example', title: 'Race', slug: 'race', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /save link/i }))
    expect(await screen.findByText(/already bookmarked/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('treats a failed screenshot on edit as a warning, not a blocked save', async () => {
    state.linkScreenshotError = { code: 'screenshot_failed', message: 'no print' }
    const link: Link = {
      id: 3, url: 'https://lan.example', title: 'LAN', slug: 'lan', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link
    state.links.push(link)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /capture page screenshot/i }))
    expect(await screen.findByText(/could not capture a screenshot/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save changes/i })).not.toBeDisabled()
    await user.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('surfaces slug_taken on create with a custom dirty slug', async () => {
    state.links.push({
      id: 1, url: 'https://a.example', title: 'A', slug: 'taken-slug', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link)
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://b.example')
    const slugInput = screen.getByRole('textbox', { name: /short url slug/i })
    fireEvent.change(slugInput, { target: { value: 'taken-slug' } })
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    expect(await screen.findByText(/already in use/i)).toBeInTheDocument()
  })

  it('auto-derives slug from title and allows reset after dirty edit', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const titleInput = screen.getByRole('textbox', { name: /Title/i })
    fireEvent.change(titleInput, { target: { value: 'Hello World' } })
    const slugInput = screen.getByRole('textbox', { name: /short url slug/i }) as HTMLInputElement
    await waitFor(() => expect(slugInput.value).toBe('hello-world'))
    fireEvent.change(slugInput, { target: { value: 'custom' } })
    expect(slugInput.value).toBe('custom')
    await userEvent.click(screen.getByRole('button', { name: /reset to auto-derived/i }))
    expect(slugInput.value).toBe('hello-world')
  })

  it('toggles pin and sends pinned=true on create', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://pin.example')
    await user.click(screen.getByRole('checkbox', { name: /pin/i }))
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links[0]?.pinned).toBe(true))
  })

  it('cycles color on a pending tag chip', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const tagsInput = screen.getByLabelText('tag filter')
    // Typed and submitted in two steps on purpose. Sending 'name{Enter}' as one
    // string races the controlled input: the keydown handler reads `tagFilter`
    // from its closure, and if React has not yet committed the final
    // keystroke's setState, `canCreateFromFilter` is still false and Enter is a
    // no-op — so no pending tag is ever queued. Asserting the input's value
    // first pins the commit, and only then is Enter meaningful.
    await user.type(tagsInput, 'pending-tag')
    await waitFor(() => expect(tagsInput).toHaveValue('pending-tag'))
    await user.keyboard('{Enter}')
    await waitFor(() => expect(document.querySelector('.fx-tag-hint')).not.toBeNull())
    const chip = screen.getByText('pending-tag')
    await user.click(chip)
    expect(document.querySelector('.fx-tag-hint')).not.toBeNull()
    expect(screen.getByText('pending-tag')).toBeInTheDocument()
  })

  // Regression: the URL field is focused a frame after mount (an iOS Safari
  // workaround), which used to yank focus away from whatever the user had
  // already started typing in. It surfaced as an intermittently failing tag
  // test; the underlying bug was that typing a tag name immediately after
  // opening the dialog scattered half the word into the URL field.
  it('does not steal focus from a field the user already started typing in', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const tagsInput = screen.getByLabelText('tag filter')

    await user.click(tagsInput)
    await user.type(tagsInput, 'my-new-tag')

    expect(tagsInput).toHaveValue('my-new-tag')
    expect(tagsInput).toHaveFocus()
    expect(screen.getByRole('textbox', { name: /^URL$/i })).toHaveValue('')
  })

  it('paginates registered tags when more than 7 are available', async () => {
    for (let i = 0; i < 10; i++) {
      state.tags.push({ id: 100 + i, name: `tag-${i}`, color: '#111', icon: null })
    }
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('1/2')).toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: /next page/i }))
    expect(screen.getByText('2/2')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /previous page/i }))
    expect(screen.getByText('1/2')).toBeInTheDocument()
  })

  it('closes via the X button', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    await userEvent.click(screen.getByRole('button', { name: /close/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('EDIT: ships a dirty empty slug as null (regenerate)', async () => {
    const link: Link = {
      id: 8, url: 'https://slug-edit', title: 'Slug Edit', slug: 'slug-edit', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link
    state.links.push(link)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    const slugInput = screen.getByRole('textbox', { name: /short url slug/i })
    await user.clear(slugInput)
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  // The card's add-image action is only useful if the dialog LANDS on the
  // upload zone. Opening on the URL field would drop the reader into the form
  // they did not ask for, with the panel they came for below the fold on a
  // narrow viewport (INV-165).
  it('lands focus on the image upload zone when opened with focus="image"', async () => {
    const link = {
      id: 42, url: 'https://example.test', title: 'No image', slug: 'no-image',
      description: '', favicon_url: null, og_image_url: null, click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as unknown as Link
    state.links.push(link)
    renderWithProviders(<LinkDialog open link={link} focus="image" onClose={vi.fn()} />)

    const zone = await screen.findByRole('button', { name: /drop or click to add image/i })
    await waitFor(() => expect(zone).toHaveFocus())
  })

  it('keeps focus on the URL field when no focus is asked for', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: /^URL$/i })).toHaveFocus()
    })
  })

  // Making the zone the dialog's focus target exposed that it was mouse-only:
  // a div with onClick and no role, tabIndex or key handler, unreachable by Tab.
  it('opens the file picker from the keyboard on the upload zone', async () => {
    const link = {
      id: 43, url: 'https://example.test/k', title: 'Keyboard', slug: 'kb',
      description: '', favicon_url: null, og_image_url: null, click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as unknown as Link
    state.links.push(link)
    const { container } = renderWithProviders(
      <LinkDialog open link={link} focus="image" onClose={vi.fn()} />,
    )
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    const clicked = vi.spyOn(input, 'click').mockImplementation(() => {})

    const zone = await screen.findByRole('button', { name: /drop or click to add image/i })
    fireEvent.keyDown(zone, { key: 'Enter' })
    expect(clicked).toHaveBeenCalled()

    // Space is half the contract, not a nicety: a div with role="button" gets
    // no native key handling, so whatever the handler omits simply does not
    // work — and Space is what most people press on something that looks like
    // a button.
    clicked.mockClear()
    fireEvent.keyDown(zone, { key: ' ' })
    expect(clicked).toHaveBeenCalled()

    clicked.mockClear()
    fireEvent.keyDown(zone, { key: 'a' })
    expect(clicked).not.toHaveBeenCalled()
  })

  // The zone is a div wearing role="button" (INV-154 forbids the real thing
  // here), so nothing announces the refusal for free: while an upload is in
  // flight both handlers no-op, and without aria-disabled a screen reader
  // presents a button that silently does nothing when pressed.
  it('announces itself as disabled while an upload is in flight', async () => {
    const link = {
      id: 44, url: 'https://example.test/busy', title: 'Busy', slug: 'busy',
      description: '', favicon_url: null, og_image_url: null, click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as unknown as Link
    state.links.push(link)

    // Never resolves: the assertion is about the window BETWEEN start and
    // finish, which any settling promise would close before we can look.
    const realPost = http.post
    const post = vi.spyOn(http, 'post').mockImplementation(((url: string, ...rest: never[]) =>
      String(url).endsWith('/image')
        ? new Promise(() => {})
        : realPost(url, ...rest)) as typeof http.post)

    const { container } = renderWithProviders(
      <LinkDialog open link={link} focus="image" onClose={vi.fn()} />,
    )
    const zone = await screen.findByRole('button', { name: /drop or click to add image/i })
    expect(zone).not.toHaveAttribute('aria-disabled')

    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, {
      target: { files: [new File(['x'], 'a.png', { type: 'image/png' })] },
    })
    fireEvent.click(screen.getByRole('button', { name: /save|salvar/i }))

    await waitFor(() => expect(zone).toHaveAttribute('aria-disabled', 'true'))
    post.mockRestore()
  })

  it('captures a screenshot immediately on an existing link', async () => {
    const link = {
      id: 51, url: 'https://www.youtube.com/watch?v=XLQ0El6V5kE', title: 'RustDesk',
      slug: 'rustdesk', click_count: 1, preview_status: 'failed', pinned: false,
      created_at: '', updated_at: '', tags: [], og_image_url: null,
    } as unknown as Link
    state.links.push(link)
    renderWithProviders(<LinkDialog open link={link} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /capture page screenshot/i }))
    await waitFor(() => expect(state.links[0].og_image_url).toContain('/api/files/screenshots/51'))
  })

  // Create then image-upload is two requests. If the image fails, the row
  // already exists — Save again must retry the image against that id, not
  // POST a second bookmark (and uniqueness would then lock the dialog).
  it('imageFailureRetriesAgainstCreatedId', async () => {
    state.linkImageUploadError = {
      status: 503,
      code: 'storage_unavailable',
      message: 'image storage is unavailable',
    }
    const onClose = vi.fn()
    const { container } = renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://img-retry.example')
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, {
      target: { files: [new File(['x'], 'cover.png', { type: 'image/png' })] },
    })

    await user.click(screen.getByRole('button', { name: /Save link/i }))
    expect(await screen.findByText(/image storage is unavailable/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(state.links).toHaveLength(1)
    const createdId = state.links[0].id
    const createPosts = () => vi.mocked(http.post).mock.calls.filter(([url]) => url === '/api/links')
    const imagePosts = () => vi.mocked(http.post).mock.calls.filter(
      ([url]) => url === `/api/links/${createdId}/image`,
    )
    expect(createPosts()).toHaveLength(1)
    expect(imagePosts()).toHaveLength(1)

    state.linkImageUploadError = undefined
    const title = screen.getByRole('textbox', { name: /^Title$/i })
    await user.clear(title)
    await user.type(title, 'Retry Title')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(createPosts()).toHaveLength(1)
    expect(imagePosts()).toHaveLength(2)
    expect(state.links).toHaveLength(1)
    expect(state.links[0].og_image_url).toContain(`/api/files/links/${createdId}`)
    const patches = vi.mocked(http.patch).mock.calls.filter(([url]) => url === `/api/links/${createdId}`)
    expect(patches).toHaveLength(1)
    expect(patches[0]?.[1]).toEqual(expect.objectContaining({ title: 'Retry Title' }))
  })

  it('names a missing object store instead of echoing axios 404', async () => {
    const link = {
      id: 52, url: 'https://example.test', title: 'X', slug: 'x',
      click_count: 0, preview_status: 'ok', pinned: false,
      created_at: '', updated_at: '', tags: [], og_image_url: '/gone.jpg',
    } as unknown as Link
    state.links.push(link)
    state.linkImageRemoveError = { status: 404, code: '', message: '' }
    renderWithProviders(<LinkDialog open link={link} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /remove image/i }))
    await user.click(screen.getByRole('button', { name: /save changes/i }))
    expect(await screen.findByText(/image storage is unavailable/i)).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    state.linkImageRemoveError = undefined
  })
})
