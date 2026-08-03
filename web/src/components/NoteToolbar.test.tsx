import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NoteToolbar } from './NoteToolbar'
import { renderWithProviders } from '../test/renderWithProviders'
import type { Editor } from '@tiptap/react'

// Chainable command recorder — every command returns the chain, run() ends it.
// We record the command names so tests can assert the toolbar wires each button
// to the right editor command without a real ProseMirror instance (jsdom can't
// run Tiptap).
function makeFakeEditor() {
  const calls: string[] = []
  const chain: Record<string, (...a: unknown[]) => unknown> = {}
  const methods = [
    'focus', 'toggleBold', 'toggleItalic', 'toggleUnderline', 'toggleStrike',
    'toggleHeading', 'toggleBulletList', 'toggleOrderedList', 'setTextAlign',
    'toggleBlockquote', 'toggleCode', 'setColor', 'setFontFamily', 'unsetFontFamily',
    'setLink', 'unsetLink', 'extendMarkRange',
  ]
  for (const m of methods) {
    chain[m] = (...args: unknown[]) => {
      calls.push(args.length ? `${m}(${JSON.stringify(args[0])})` : m)
      return chain
    }
  }
  chain.run = () => true
  const editor = {
    isActive: () => false,
    getAttributes: () => ({}),
    chain: () => chain,
  } as unknown as Editor
  return { editor, calls }
}

let fake: ReturnType<typeof makeFakeEditor>

beforeEach(() => {
  fake = makeFakeEditor()
})

describe('NoteToolbar', () => {
  it('renders nothing without an editor', () => {
    const { container } = renderWithProviders(<NoteToolbar editor={null} onInsertImage={vi.fn()} />)
    expect(container.querySelector('.fx-tt-toolbar')).toBeNull()
  })

  it('renders the formatting controls', () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    expect(screen.getByRole('toolbar')).toBeInTheDocument()
    // A representative sample of the requested controls.
    for (const name of [/bold/i, /italic/i, /underline/i, /numbered list|numerada/i, /align center|centralizar|centrar/i, /text color|cor do texto|color del texto/i]) {
      expect(screen.getByLabelText(name)).toBeInTheDocument()
    }
  })

  it('wires bold / ordered list / center align to editor commands', async () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/bold|negrito|negrita/i))
    await user.click(screen.getByLabelText(/numbered list|numerada/i))
    await user.click(screen.getByLabelText(/align center|centralizar|centrar/i))
    expect(fake.calls).toContain('toggleBold')
    expect(fake.calls).toContain('toggleOrderedList')
    expect(fake.calls).toContain('setTextAlign("center")')
  })

  it('sets a heading level', async () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    await userEvent.setup().click(screen.getByLabelText(/heading 1|título 1/i))
    expect(fake.calls).toContain('toggleHeading({"level":1})')
  })

  it('triggers image insert callback', async () => {
    const onInsertImage = vi.fn()
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={onInsertImage} />)
    await userEvent.setup().click(screen.getByLabelText(/insert image|inserir imagem|insertar imagen/i))
    expect(onInsertImage).toHaveBeenCalled()
  })

  it('wires remaining format commands', async () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/italic/i))
    await user.click(screen.getByLabelText(/underline/i))
    await user.click(screen.getByLabelText(/strikethrough|strike/i))
    await user.click(screen.getByLabelText(/heading 2/i))
    await user.click(screen.getByLabelText(/heading 3/i))
    await user.click(screen.getByLabelText(/bullet list/i))
    await user.click(screen.getByLabelText(/align left/i))
    await user.click(screen.getByLabelText(/align right/i))
    await user.click(screen.getByLabelText(/justify/i))
    await user.click(screen.getByLabelText(/quote|blockquote/i))
    await user.click(screen.getByLabelText(/inline code|^code$/i))
    expect(fake.calls).toEqual(expect.arrayContaining([
      'toggleItalic',
      'toggleUnderline',
      'toggleStrike',
      'toggleHeading({"level":2})',
      'toggleHeading({"level":3})',
      'toggleBulletList',
      'setTextAlign("left")',
      'setTextAlign("right")',
      'setTextAlign("justify")',
      'toggleBlockquote',
      'toggleCode',
    ]))
  })

  it('sets font family and clears default', async () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    const select = screen.getByLabelText(/^Font$/i)
    fireEvent.change(select, { target: { value: 'Georgia, Cambria, "Times New Roman", serif' } })
    expect(fake.calls.some((c) => c.startsWith('setFontFamily'))).toBe(true)
    fireEvent.change(select, { target: { value: '' } })
    expect(fake.calls).toContain('unsetFontFamily')
  })

  it('sets text color from the color input', async () => {
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    const color = screen.getByLabelText(/text color/i)
    fireEvent.change(color, { target: { value: '#ff0000' } })
    expect(fake.calls).toContain('setColor("#ff0000")')
  })

  it('sets a link via prompt and unsets when empty', async () => {
    const promptSpy = vi.spyOn(window, 'prompt')
    promptSpy.mockReturnValueOnce('https://example.com')
    renderWithProviders(<NoteToolbar editor={fake.editor} onInsertImage={vi.fn()} />)
    await userEvent.setup().click(screen.getByLabelText(/^Link$/i))
    expect(fake.calls.some((c) => c.startsWith('setLink'))).toBe(true)

    fake.calls.length = 0
    promptSpy.mockReturnValueOnce('   ')
    await userEvent.setup().click(screen.getByLabelText(/^Link$/i))
    expect(fake.calls).toContain('unsetLink')

    fake.calls.length = 0
    promptSpy.mockReturnValueOnce(null)
    await userEvent.setup().click(screen.getByLabelText(/^Link$/i))
    expect(fake.calls).toHaveLength(0)
  })

  it('marks active buttons when editor.isActive is true', () => {
    const { editor } = makeFakeEditor()
    ;(editor as any).isActive = (name: unknown) => name === 'bold' || (typeof name === 'object' && (name as any).textAlign === 'center')
    renderWithProviders(<NoteToolbar editor={editor} onInsertImage={vi.fn()} />)
    expect(screen.getByLabelText(/bold/i)).toHaveAttribute('aria-pressed', 'true')
  })
})

