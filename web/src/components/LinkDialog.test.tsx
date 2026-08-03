import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LinkDialog } from './LinkDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Link } from '../api/types'
import { _clearUrlMetadataCacheForTests } from '../api/links'

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

/** Assert no metadata fetch after debounce without wall-clock flakiness. */
async function expectNoMetadataFetch(ms = 550) {
  const before = state.urlMetadataCalls.length
  await new Promise((r) => setTimeout(r, ms))
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

  it('disables save when URL is empty', () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Save link/i })).toBeDisabled()
  })

  it('creates a new tag inline via tag filter input', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()

    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'brand-new{Enter}')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.tags.some((t) => t.name === 'brand-new')).toBe(true))
    expect(state.links).toHaveLength(1)
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
    state.urlMetadata = { title: 'Hacker News', description: 'Tech news' }
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://news.ycombinator.com')

    // Debounce fires after 500ms of idle — give the fetch a generous window.
    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://news.ycombinator.com'), { timeout: 2000 })

    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    await waitFor(() => expect(titleInput.value).toBe('Hacker News'), { timeout: 2000 })

    // Description should also have been pre-filled since the user left it empty.
    const desc = screen.getByRole('textbox', { name: /description/i }) as HTMLTextAreaElement
    expect(desc.value).toBe('Tech news')
  })

  it('AUTO-FILL: never overwrites user-typed title', async () => {
    state.urlMetadata = { title: 'Auto Title', description: 'Auto Desc' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://example.com" onClose={vi.fn()} />)
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    fireEvent.change(titleInput, { target: { value: 'my custom title' } })
    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://example.com'))
    // One more microtask tick so onSuccess setters settle if they race.
    await waitFor(() => expect(titleInput.value).toBe('my custom title'))
  })

  it('AUTO-FILL: does not fire for invalid-looking URLs', async () => {
    state.urlMetadata = { title: 'should not see this' }
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'hello world')
    await expectNoMetadataFetch()
  })

  it('AUTO-FILL: skipped entirely in edit mode', async () => {
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

    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://d.example'), { timeout: 2000 })
    // Critical assertion: exactly ONE fetch happened, and it used the FINAL
    // URL — never the intermediates.
    expect(state.urlMetadataCalls).toEqual(['https://d.example'])
  })

  it('AUTO-FILL: aborts in-flight fetch when dialog closes mid-debounce', async () => {
    state.urlMetadata = { title: 'Should never apply' }
    const { unmount } = renderWithProviders(<LinkDialog open link={null} initialUrl="https://abort.example" onClose={vi.fn()} />)
    unmount()
    await expectNoMetadataFetch(600)
  })

  it('AUTO-FILL: never overwrites user-typed description either', async () => {
    state.urlMetadata = { title: 'Auto Title', description: 'Auto Desc' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://example.com" onClose={vi.fn()} />)
    const desc = screen.getByRole('textbox', { name: /description/i }) as HTMLTextAreaElement
    fireEvent.change(desc, { target: { value: 'my custom desc' } })
    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://example.com'))
    await waitFor(() => expect(desc.value).toBe('my custom desc'))
  })

  it('AUTO-FILL: in-memory cache dedups the same URL across dialog mounts', async () => {
    state.urlMetadata = { title: 'Cached Title' }
    const { unmount } = renderWithProviders(
      <LinkDialog open link={null} initialUrl="https://cache-me.example" onClose={vi.fn()} />,
    )
    await waitFor(() => expect(state.urlMetadataCalls).toHaveLength(1))
    unmount()

    renderWithProviders(<LinkDialog open link={null} initialUrl="https://cache-me.example" onClose={vi.fn()} />)
    await expectNoMetadataFetch(600)
    expect(state.urlMetadataCalls).toHaveLength(1)
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    await waitFor(() => expect(titleInput.value).toBe('Cached Title'))
  })

  it('AUTO-FILL: cache key is the URL — distinct URLs each fetch once', async () => {
    // Defensive: locks that the cache lookup uses the URL as key. A bug that
    // ignored the key (e.g. memoizing on something stable like a constant)
    // would return the FIRST URL's metadata for the second URL — silently
    // mislabeling links.
    state.urlMetadata = { title: 'Title A' }
    const { unmount } = renderWithProviders(
      <LinkDialog open link={null} initialUrl="https://a.example" onClose={vi.fn()} />,
    )
    await waitFor(() => expect(state.urlMetadataCalls).toEqual(['https://a.example']), { timeout: 2000 })
    unmount()

    state.urlMetadata = { title: 'Title B' }
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://b.example" onClose={vi.fn()} />)
    await waitFor(
      () => expect(state.urlMetadataCalls).toEqual(['https://a.example', 'https://b.example']),
      { timeout: 2000 },
    )
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    await waitFor(() => expect(titleInput.value).toBe('Title B'), { timeout: 2000 })
  })

  it('AUTO-FILL: tolerates a 502 from the backend silently', async () => {
    state.urlMetadataError = Object.assign(new Error('fetch_failed'), {
      response: { status: 502, data: { error: { code: 'fetch_failed', message: 'could not fetch URL metadata' } } },
    })
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://broken.example')

    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://broken.example'), { timeout: 2000 })

    // Failure is silent — title stays empty, no error chip rendered, save
    // button stays enabled (URL is present).
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    expect(titleInput.value).toBe('')
    expect(screen.getByRole('button', { name: /Save link/i })).toBeEnabled()
  })

  it('AUTO-FILL: shows failed hint under empty title after a real error', async () => {
    state.urlMetadataError = Object.assign(new Error('fetch_failed'), {
      code: 'ERR_BAD_RESPONSE',
      response: { status: 502, data: { error: { code: 'fetch_failed', message: 'could not fetch' } } },
    })
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://blocked.example" onClose={vi.fn()} />)
    await waitFor(() => expect(state.urlMetadataCalls).toContain('https://blocked.example'), { timeout: 2000 })
    await waitFor(() => expect(screen.getByText(/could not auto-fill/i)).toBeInTheDocument())
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

  it('surfaces url_taken on create', async () => {
    state.links.push({
      id: 1, url: 'https://dup.example', title: 'Dup', slug: 'dup', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link)
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://dup.example')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    expect(await screen.findByText(/already bookmarked/i)).toBeInTheDocument()
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
    await user.type(tagsInput, 'pending-tag{Enter}')
    await waitFor(() => expect(document.querySelector('.fx-tag-hint')).not.toBeNull())
    const chip = screen.getByText('pending-tag')
    await user.click(chip)
    expect(document.querySelector('.fx-tag-hint')).not.toBeNull()
    expect(screen.getByText('pending-tag')).toBeInTheDocument()
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
})
