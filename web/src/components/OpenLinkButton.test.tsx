import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CopyLinkButton, OpenLinkButton } from './OpenLinkButton'
import { ListView } from './ListView'
import { LinkCard } from './LinkCard'
import { CompactGrid } from './CompactGrid'
import { CopyToastHost, showCopyToast } from './CopyToast'
import { renderWithProviders } from '../test/renderWithProviders'
import type { Link } from '../api/types'

const baseLink = {
  id: 1,
  url: 'https://example.com/article',
  title: 'Example Article',
  created_at: '2026-10-01T10:00:00Z',
  tags: [],
  click_count: 0,
  pinned: false,
  preview_status: 'ok',
} as unknown as Link

/** jsdom ships a NATIVE navigator.clipboard whose object the page code already
 *  holds; replacing the object loses the race. Spying the METHOD on the live
 *  object is the mock that survives — and vi.restoreAllMocks puts it back. */
/** userEvent.setup() installs user-event's own navigator.clipboard (that is
 *  the createClipboardItem implementation the clicks otherwise hit), so the
 *  mock has to be stamped AFTER setup and before the click — replacing the
 *  whole object wins because the component reads it per call. */
function clipboardMock(user: ReturnType<typeof userEvent.setup>, ok = true) {
  const writeText = vi.fn().mockImplementation(() =>
    ok ? Promise.resolve() : Promise.reject(new Error('denied')),
  )
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true, writable: true })
  return writeText
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('CopyLinkButton', () => {
  // The whole point of the button: the URL lands on the clipboard, raw and
  // complete — anything transformed here (redirect wrapper, tracking) would
  // paste as garbage somewhere else.
  it('writes the link url to the clipboard', async () => {
    renderWithProviders(<CopyLinkButton link={baseLink} />)
    const user = userEvent.setup()
    const writeText = clipboardMock(user)
    await user.click(screen.getByRole('button', { name: /copy the link/i }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://example.com/article'))
  })

  it('flips to a check so the copy is visibly confirmed', async () => {
    renderWithProviders(<CopyLinkButton link={baseLink} />)
    const user = userEvent.setup()
    clipboardMock(user)
    await user.click(screen.getByRole('button', { name: /copy the link/i }))
    expect(await screen.findByRole('button', { name: /copied/i })).toBeInTheDocument()
  })

  // A denied clipboard is environmental (non-secure origin, focus race); the
  // button must not claim success it did not get.
  it('stays unconfirmed when the clipboard refuses', async () => {
    renderWithProviders(<CopyLinkButton link={baseLink} />)
    const user = userEvent.setup()
    clipboardMock(user, false)
    await user.click(screen.getByRole('button', { name: /copy the link/i }))
    expect(screen.queryByRole('button', { name: /copied/i })).not.toBeInTheDocument()
  })
})

describe('OpenLinkButton', () => {
  it('opens through the redirect wrapper, not the raw url', () => {
    renderWithProviders(<OpenLinkButton link={baseLink} />)
    const go = screen.getByRole('link', { name: /open example article/i }) as HTMLAnchorElement
    expect(go.getAttribute('href')).toContain('/go/')
  })

  // Cards clip their own overflow, so the menu MUST portal out of the card
  // subtree — an in-place menu would render cut in half. The portal target
  // is the actual assertion: merely finding role=menu would pass either way.
  it('portals the menu to document.body, outside the rendered subtree', async () => {
    const { container } = renderWithProviders(<OpenLinkButton link={baseLink} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /more access options/i }))

    const menu = screen.getByRole('menu')
    expect(menu.parentElement).toBe(document.body)
    expect(container.contains(menu)).toBe(false)
  })

  it('menu copies the link', async () => {
    renderWithProviders(<OpenLinkButton link={baseLink} />)
    const user = userEvent.setup()
    const writeText = clipboardMock(user)
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: /copy link/i }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://example.com/article'))
  })

  // Incognito is the one flow that cannot end at the click: the browser has
  // no API to open the window, so the copy MUST be followed by the two
  // keystrokes the toast spells out.
  it('incognito item copies and explains the remaining steps', async () => {
    renderWithProviders(
      <>
        <OpenLinkButton link={baseLink} />
        <CopyToastHost />
      </>,
    )
    const user = userEvent.setup()
    const writeText = clipboardMock(user)
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(screen.getByRole('menuitem', { name: /incognito/i }))

    expect(writeText).toHaveBeenCalledWith('https://example.com/article')
    expect(await screen.findByRole('status')).toHaveTextContent(/incognito window/i)
  })

  it('closes the menu on Escape and on an outside click', async () => {
    renderWithProviders(
      <>
        <OpenLinkButton link={baseLink} />
        <p>elsewhere</p>
      </>,
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(screen.getByText('elsewhere'))
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('toasts failure rather than silence when the clipboard refuses', async () => {
    renderWithProviders(
      <>
        <OpenLinkButton link={baseLink} />
        <CopyToastHost />
      </>,
    )
    const user = userEvent.setup()
    clipboardMock(user, false)
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(screen.getByRole('menuitem', { name: /incognito/i }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/could not copy/i))
  })
})

describe('the three views wire the group', () => {
  // The wiring is where the small-variant lives; a view forgetting the copy
  // button would pass every component test above.
  it('ListView link rows carry the copy button and the open menu', async () => {
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    clipboardMock(user)
    expect(screen.getByRole('button', { name: /copy the link example article/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })

  it('LinkCard carries the copy button and its menu copies the url', async () => {
    renderWithProviders(
      <LinkCard
        link={{
          ...baseLink,
          slug: 'example-article',
          description: 'Body',
          favicon_url: null,
          og_image_url: null,
          preview_error: null,
          last_clicked_at: null,
          updated_at: '2026-10-01T10:00:00Z',
        } as Link}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onPin={vi.fn()}
        onRefreshPreview={vi.fn()}
        onAddImage={vi.fn()}
        onMarkSeen={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    const writeText = clipboardMock(user)
    expect(screen.getByRole('button', { name: /copy the link example article/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: /copy link/i }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://example.com/article'))
  })

  it('CompactGrid link rows carry the copy button and its menu copies the url', async () => {
    renderWithProviders(
      <CompactGrid
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    const writeText = clipboardMock(user)
    expect(screen.getByRole('button', { name: /copy the link example article/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /more access options/i }))
    await user.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: /copy link/i }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://example.com/article'))
  })
})

describe('CopyToastHost', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  // The toast is instructions the user still has to act on, but it is not a
  // dialog: it must clear itself or the next copy's message is unreadable
  // behind a stale one. Fake timers pin the exact 3200 ms lifetime — one
  // millisecond early it must still be up, at 3200 it must be gone.
  it('auto-dismisses after 3200 ms and not before', () => {
    vi.useFakeTimers()
    renderWithProviders(<CopyToastHost />)

    act(() => showCopyToast('Copied — paste it in an incognito window'))
    expect(screen.getByRole('status')).toHaveTextContent(/incognito window/i)

    act(() => vi.advanceTimersByTime(3199))
    expect(screen.getByRole('status')).toBeInTheDocument()

    act(() => vi.advanceTimersByTime(1))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('a second toast restarts the dismissal clock', () => {
    vi.useFakeTimers()
    renderWithProviders(<CopyToastHost />)

    act(() => showCopyToast('first message'))
    act(() => vi.advanceTimersByTime(3000))
    act(() => showCopyToast('second message'))
    expect(screen.getByRole('status')).toHaveTextContent('second message')

    act(() => vi.advanceTimersByTime(3000))
    expect(screen.getByRole('status')).toHaveTextContent('second message')

    act(() => vi.advanceTimersByTime(200))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
