import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FolderCard } from './FolderCard'
import type { Folder, PreviewTile } from '../api/types'

function makeTile(i: number, og?: string): PreviewTile {
  return { id: i, title: `link-${i}`, og_image_url: og ?? null, favicon_url: null }
}

function makeFolder(opts: Partial<Folder> & { link_count: number; preview_links: PreviewTile[] }): Folder {
  return {
    id: 1,
    name: opts.name ?? 'Trabalho',
    color: opts.color ?? '#0EA5E9',
    link_count: opts.link_count,
    folder_count: opts.folder_count ?? 0,
    preview_links: opts.preview_links,
    preview_folders: opts.preview_folders ?? [],
    has_password: opts.has_password ?? false,
    created_at: new Date().toISOString(),
  }
}

describe('FolderCard', () => {
  it('renders folder name and link count', () => {
    render(<FolderCard folder={makeFolder({ link_count: 3, preview_links: [] })} onOpen={vi.fn()} />)
    expect(screen.getByText('Trabalho')).toBeInTheDocument()
    expect(screen.getByText(/3 link/)).toBeInTheDocument()
  })

  it('pluralizes 1 link correctly', () => {
    render(<FolderCard folder={makeFolder({ link_count: 1, preview_links: [makeTile(1)] })} onOpen={vi.fn()} />)
    expect(screen.getByText(/1 link/)).toBeInTheDocument()
  })

  it('shows folder count when there are subfolders', () => {
    render(
      <FolderCard
        folder={makeFolder({ link_count: 2, folder_count: 3, preview_links: [] })}
        onOpen={vi.fn()}
      />,
    )
    expect(screen.getByText(/2 links · 3 folders/)).toBeInTheDocument()
  })

  it('pluralizes 1 subfolder correctly', () => {
    render(
      <FolderCard
        folder={makeFolder({ link_count: 0, folder_count: 1, preview_links: [] })}
        onOpen={vi.fn()}
      />,
    )
    expect(screen.getByText(/0 links · 1 folder/)).toBeInTheDocument()
  })

  it('hides the folder count when there are no subfolders', () => {
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 5, folder_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
      />,
    )
    const host = container.querySelector('.fx-card-host')
    // "5 links" — no " · 0 folders" segment
    expect(host?.textContent?.trim()).toBe('5 links')
  })

  it('shows "Pasta vazia" overlay when link_count is 0', () => {
    render(<FolderCard folder={makeFolder({ link_count: 0, preview_links: [] })} onOpen={vi.fn()} />)
    expect(screen.getByText(/Empty folder/i)).toBeInTheDocument()
  })

  it('renders subfolder tiles when the folder has no direct links', () => {
    const { container } = render(
      <FolderCard
        folder={makeFolder({
          link_count: 0,
          folder_count: 2,
          preview_links: [],
          preview_folders: [
            { id: 10, name: 'Sub1', color: '#06B6D4' },
            { id: 11, name: 'Sub2', color: '#10B981' },
          ],
        })}
        onOpen={vi.fn()}
      />,
    )
    expect(container.querySelectorAll('.fx-folder-tile-subfolder').length).toBe(2)
    expect(screen.queryByText(/Empty folder/i)).toBeNull()
  })

  it('shows "Pasta vazia" only when BOTH links and subfolders are empty', () => {
    render(
      <FolderCard
        folder={makeFolder({
          link_count: 0,
          folder_count: 0,
          preview_links: [],
          preview_folders: [],
        })}
        onOpen={vi.fn()}
      />,
    )
    expect(screen.getByText(/Empty folder/i)).toBeInTheDocument()
  })

  it('renders +N overlay when there are more than 4 links', () => {
    const tiles = [1, 2, 3, 4].map((i) => makeTile(i))
    render(<FolderCard folder={makeFolder({ link_count: 10, preview_links: tiles })} onOpen={vi.fn()} />)
    expect(screen.getByText('+6')).toBeInTheDocument()
  })

  it('does not render +N when link_count <= 4', () => {
    const tiles = [1, 2, 3, 4].map((i) => makeTile(i))
    const { container } = render(
      <FolderCard folder={makeFolder({ link_count: 4, preview_links: tiles })} onOpen={vi.fn()} />,
    )
    expect(container.querySelector('.fx-folder-tile-more')).toBeNull()
  })

  it('calls onOpen when the preview is clicked', async () => {
    const onOpen = vi.fn()
    render(<FolderCard folder={makeFolder({ link_count: 0, preview_links: [] })} onOpen={onOpen} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /Open folder Trabalho/i }))
    expect(onOpen).toHaveBeenCalledWith(1)
  })

  it('accepts a link drop and calls onDropLink with (sourceId, folderId)', () => {
    const onDropLink = vi.fn()
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
        onDropLink={onDropLink}
      />,
    )
    const root = container.querySelector('.fx-folder-card') as HTMLElement
    fireEvent.drop(root, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: (k: string) => (k === 'application/x-foldex-link' ? '42' : ''),
      },
    })
    expect(onDropLink).toHaveBeenCalledWith(42, 1)
  })

  it('ignores drops without the foldex link payload', () => {
    const onDropLink = vi.fn()
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
        onDropLink={onDropLink}
      />,
    )
    const root = container.querySelector('.fx-folder-card') as HTMLElement
    fireEvent.drop(root, {
      dataTransfer: {
        types: [],
        getData: () => '',
      },
    })
    expect(onDropLink).not.toHaveBeenCalled()
  })

  it('accepts a folder drop and calls onDropFolder with (sourceId, folderId)', () => {
    const onDropFolder = vi.fn()
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
        onDropFolder={onDropFolder}
      />,
    )
    const root = container.querySelector('.fx-folder-card') as HTMLElement
    fireEvent.drop(root, {
      dataTransfer: {
        types: ['application/x-foldex-folder'],
        getData: (k: string) => (k === 'application/x-foldex-folder' ? '17' : ''),
      },
    })
    expect(onDropFolder).toHaveBeenCalledWith(17, 1)
  })

  it('ignores a folder drop on itself (same id)', () => {
    const onDropFolder = vi.fn()
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
        onDropFolder={onDropFolder}
      />,
    )
    const root = container.querySelector('.fx-folder-card') as HTMLElement
    fireEvent.drop(root, {
      dataTransfer: {
        types: ['application/x-foldex-folder'],
        getData: (k: string) => (k === 'application/x-foldex-folder' ? '1' : ''),
      },
    })
    expect(onDropFolder).not.toHaveBeenCalled()
  })

  it('exposes the folder id via the foldex-folder MIME type on drag start', () => {
    const setData = vi.fn()
    const { container } = render(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
      />,
    )
    const root = container.querySelector('.fx-folder-card') as HTMLElement
    fireEvent.dragStart(root, { dataTransfer: { setData, effectAllowed: '' } })
    expect(setData).toHaveBeenCalledWith('application/x-foldex-folder', '1')
  })

  it('renders the edit affordance only when onEdit is provided', () => {
    const { rerender } = render(
      <FolderCard folder={makeFolder({ link_count: 0, preview_links: [] })} onOpen={vi.fn()} />,
    )
    expect(screen.queryByLabelText(/edit folder/i)).toBeNull()
    rerender(
      <FolderCard
        folder={makeFolder({ link_count: 0, preview_links: [] })}
        onOpen={vi.fn()}
        onEdit={vi.fn()}
      />,
    )
    expect(screen.getByLabelText(/edit folder/i)).toBeInTheDocument()
  })

  describe('compact mode', () => {
    it('hides the 2x2 preview area when compact', () => {
      const { container } = render(
        <FolderCard
          folder={makeFolder({ link_count: 3, preview_links: [makeTile(1), makeTile(2)] })}
          onOpen={vi.fn()}
          compact
        />,
      )
      expect(container.querySelector('.fx-folder-preview')).toBeNull()
      expect(container.querySelector('.fx-folder-card-compact')).not.toBeNull()
    })

    it('still renders the name, count and open button in compact mode', () => {
      render(
        <FolderCard
          folder={makeFolder({ link_count: 4, preview_links: [] })}
          onOpen={vi.fn()}
          compact
        />,
      )
      expect(screen.getByText('Trabalho')).toBeInTheDocument()
      expect(screen.getByText(/4 link/)).toBeInTheDocument()
      expect(screen.getByLabelText(/open folder/i)).toBeInTheDocument()
    })

    it('shows the RapidView popover on hover when compact and the folder has content', async () => {
      render(
        <FolderCard
          folder={makeFolder({
            name: 'Pesquisa',
            link_count: 2,
            folder_count: 1,
            preview_links: [
              { id: 1, title: 'Artigo alpha', og_image_url: null, favicon_url: null },
              { id: 2, title: 'Artigo beta', og_image_url: null, favicon_url: null },
            ],
            preview_folders: [{ id: 99, name: 'Subpasta zeta', color: '#10B981' }],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      // Initial: popover not mounted.
      expect(document.querySelector('.fx-rapidview')).toBeNull()
      // The RapidView trigger wraps the title — fire the hover on it directly
      // so we don't need to coordinate userEvent + fake timers.
      const titleBtn = screen.getByRole('button', { name: 'Pesquisa' })
      const trigger = titleBtn.closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      // 220ms show delay + a small render buffer.
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).not.toBeNull(),
        { timeout: 1000 },
      )
      const popover = document.querySelector('.fx-rapidview')!
      // Subfolder listed first, then both links.
      expect(popover.textContent).toMatch(/Subpasta zeta/)
      expect(popover.textContent).toMatch(/Artigo alpha/)
      expect(popover.textContent).toMatch(/Artigo beta/)
    })

    it('shows "+N more" in the popover when the folder has more items than fit', async () => {
      const previewLinks = Array.from({ length: 4 }, (_, i) => ({
        id: i + 1,
        title: `link-${i + 1}`,
        og_image_url: null,
        favicon_url: null,
      }))
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 50,
            folder_count: 0,
            preview_links: previewLinks,
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const titleBtn = screen.getByRole('button', { name: 'Trabalho' })
      const trigger = titleBtn.closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview-more')).not.toBeNull(),
        { timeout: 1000 },
      )
      // Backend's preview window already caps at 4; the +N more footer fills
      // the gap to the folder's actual total count.
      expect(document.querySelector('.fx-rapidview-more')?.textContent).toMatch(/\+46 more/)
    })

    it('does not render the RapidView popover for an empty folder', async () => {
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 0,
            folder_count: 0,
            preview_links: [],
            preview_folders: [],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const titleBtn = screen.getByRole('button', { name: 'Trabalho' })
      const trigger = titleBtn.closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      // Wait past the show delay, then assert the popover never appeared.
      await new Promise((r) => setTimeout(r, 400))
      expect(document.querySelector('.fx-rapidview')).toBeNull()
    })

    it('closes the RapidView popover when Escape is pressed', async () => {
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 3,
            preview_links: [makeTile(1), makeTile(2), makeTile(3)],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const trigger = screen
        .getByRole('button', { name: 'Trabalho' })
        .closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).not.toBeNull(),
        { timeout: 1000 },
      )
      fireEvent.keyDown(window, { key: 'Escape' })
      // Esc handler calls setOpen(false) synchronously; the popover should be
      // gone on the next tick.
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).toBeNull(),
        { timeout: 200 },
      )
    })

    it('closes the RapidView popover on mouseLeave', async () => {
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 2,
            preview_links: [makeTile(1), makeTile(2)],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const trigger = screen
        .getByRole('button', { name: 'Trabalho' })
        .closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).not.toBeNull(),
        { timeout: 1000 },
      )
      fireEvent.mouseLeave(trigger)
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).toBeNull(),
        { timeout: 200 },
      )
    })

    it('compact=false (default) never opens the RapidView popover on hover', async () => {
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 3,
            preview_links: [makeTile(1), makeTile(2), makeTile(3)],
          })}
          onOpen={vi.fn()}
        />, // compact omitted → undefined → passthrough
      )
      const trigger = screen
        .getByRole('button', { name: 'Trabalho' })
        .closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      // Wait past the show delay, then assert nothing mounted.
      await new Promise((r) => setTimeout(r, 400))
      expect(document.querySelector('.fx-rapidview')).toBeNull()
    })

    it('cancels the pending show-timer when mouseLeave fires before the delay elapsed', async () => {
      // Distinct from the other mouseLeave test: there we wait for the popover
      // to appear before leaving. Here we leave SYNCHRONOUSLY while the
      // setTimeout is still pending — covers the `clearTimeout` branch of
      // `cancelOpen`, not the `setOpen(false)` one.
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 2,
            preview_links: [makeTile(1), makeTile(2)],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const trigger = screen
        .getByRole('button', { name: 'Trabalho' })
        .closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      // Leave well before the 220 ms show-delay would fire.
      fireEvent.mouseLeave(trigger)
      // Wait past what the timer WOULD have been; nothing should appear.
      await new Promise((r) => setTimeout(r, 400))
      expect(document.querySelector('.fx-rapidview')).toBeNull()
    })

    it('refuses to render an unsafe favicon URL in the RapidView popover', async () => {
      // Belt-and-suspenders for the safeImageUrl audit. If a future commit
      // forgets to wrap a call site, this test catches the regression at the
      // integration layer — not just at the unit level.
      render(
        <FolderCard
          folder={makeFolder({
            link_count: 1,
            preview_links: [
              { id: 1, title: 'poisoned', og_image_url: null, favicon_url: 'javascript:alert(1)' },
            ],
          })}
          onOpen={vi.fn()}
          compact
        />,
      )
      const trigger = screen
        .getByRole('button', { name: 'Trabalho' })
        .closest('.fx-rapidview-trigger') as HTMLElement
      fireEvent.mouseEnter(trigger)
      await waitFor(
        () => expect(document.querySelector('.fx-rapidview')).not.toBeNull(),
        { timeout: 1000 },
      )
      // Title is rendered (textContent escape is React's default), but no <img>
      // anywhere in the popover — the helper collapsed the unsafe URL to
      // undefined, so the fallback link icon ran instead.
      const popover = document.querySelector('.fx-rapidview')!
      expect(popover.textContent).toMatch(/poisoned/)
      expect(popover.querySelector('img')).toBeNull()
    })
  })
})
