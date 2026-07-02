import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import type { Editor } from '@tiptap/react'
import { Icon, I } from './icons'

// Font stacks offered by the toolbar. Values are plain family lists (no url(),
// no parentheses) so they pass the server-side font-family sanitizer allowlist
// verbatim. The empty value clears the FontFamily mark (back to the CSS default).
const FONT_STACKS = [
  { key: 'default', value: '' },
  { key: 'sans', value: 'ui-sans-serif, system-ui, sans-serif' },
  { key: 'serif', value: 'Georgia, Cambria, "Times New Roman", serif' },
  { key: 'mono', value: 'ui-monospace, SFMono-Regular, Menlo, monospace' },
]

type Props = {
  editor: Editor | null
  // Opens the parent's hidden file picker to upload+insert an image.
  onInsertImage: () => void
}

// NoteToolbar is the formatting bar above the Tiptap editor. It re-renders with
// NoteDialog on every editor transaction (useEditor subscribes the parent), so
// isActive(...) reflects the current selection without extra wiring.
export function NoteToolbar({ editor, onInsertImage }: Props) {
  const { t } = useTranslation()
  if (!editor) return null

  const currentColor = (editor.getAttributes('textStyle').color as string) || '#111111'
  const currentFont = (editor.getAttributes('textStyle').fontFamily as string) || ''

  const setLink = () => {
    const prev = (editor.getAttributes('link').href as string) || ''
    const url = window.prompt(t('note_toolbar.link_prompt'), prev)
    if (url === null) return
    if (url.trim() === '') {
      editor.chain().focus().extendMarkRange('link').unsetLink().run()
      return
    }
    editor.chain().focus().extendMarkRange('link').setLink({ href: url.trim() }).run()
  }

  return (
    <div className="fx-tt-toolbar" role="toolbar" aria-label={t('note_toolbar.aria')}>
      <select
        className="fx-tt-select"
        value={currentFont}
        onChange={(e) => {
          const v = e.target.value
          if (v === '') editor.chain().focus().unsetFontFamily().run()
          else editor.chain().focus().setFontFamily(v).run()
        }}
        aria-label={t('note_toolbar.font')}
        data-tooltip={t('note_toolbar.font')}
      >
        {FONT_STACKS.map((f) => (
          <option key={f.key} value={f.value}>
            {t(`note_toolbar.font_${f.key}`)}
          </option>
        ))}
      </select>

      <label className="fx-tt-color" data-tooltip={t('note_toolbar.color')}>
        <span className="fx-tt-color-swatch" style={{ background: currentColor }} />
        <input
          type="color"
          value={/^#[0-9a-fA-F]{6}$/.test(currentColor) ? currentColor : '#111111'}
          onChange={(e) => editor.chain().focus().setColor(e.target.value).run()}
          aria-label={t('note_toolbar.color')}
        />
      </label>

      <span className="fx-tt-sep" />

      <Btn label={t('note_toolbar.bold')} active={editor.isActive('bold')} onClick={() => editor.chain().focus().toggleBold().run()}>
        <Icon d={I.bold} size={15} stroke={2.2} />
      </Btn>
      <Btn label={t('note_toolbar.italic')} active={editor.isActive('italic')} onClick={() => editor.chain().focus().toggleItalic().run()}>
        <Icon d={I.italic} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.underline')} active={editor.isActive('underline')} onClick={() => editor.chain().focus().toggleUnderline().run()}>
        <Icon d={I.underline} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.strike')} active={editor.isActive('strike')} onClick={() => editor.chain().focus().toggleStrike().run()}>
        <Icon d={I.strike} size={15} />
      </Btn>

      <span className="fx-tt-sep" />

      {([1, 2, 3] as const).map((level) => (
        <Btn
          key={level}
          label={t('note_toolbar.heading', { level })}
          active={editor.isActive('heading', { level })}
          onClick={() => editor.chain().focus().toggleHeading({ level }).run()}
        >
          <span className="fx-tt-txt">H{level}</span>
        </Btn>
      ))}

      <span className="fx-tt-sep" />

      <Btn label={t('note_toolbar.bullet_list')} active={editor.isActive('bulletList')} onClick={() => editor.chain().focus().toggleBulletList().run()}>
        <Icon d={I.listBullet} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.ordered_list')} active={editor.isActive('orderedList')} onClick={() => editor.chain().focus().toggleOrderedList().run()}>
        <Icon d={I.listOrdered} size={15} />
      </Btn>

      <span className="fx-tt-sep" />

      <Btn label={t('note_toolbar.align_left')} active={editor.isActive({ textAlign: 'left' })} onClick={() => editor.chain().focus().setTextAlign('left').run()}>
        <Icon d={I.alignLeft} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.align_center')} active={editor.isActive({ textAlign: 'center' })} onClick={() => editor.chain().focus().setTextAlign('center').run()}>
        <Icon d={I.alignCenter} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.align_right')} active={editor.isActive({ textAlign: 'right' })} onClick={() => editor.chain().focus().setTextAlign('right').run()}>
        <Icon d={I.alignRight} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.align_justify')} active={editor.isActive({ textAlign: 'justify' })} onClick={() => editor.chain().focus().setTextAlign('justify').run()}>
        <Icon d={I.alignJustify} size={15} />
      </Btn>

      <span className="fx-tt-sep" />

      <Btn label={t('note_toolbar.blockquote')} active={editor.isActive('blockquote')} onClick={() => editor.chain().focus().toggleBlockquote().run()}>
        <Icon d={I.quote} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.code')} active={editor.isActive('code')} onClick={() => editor.chain().focus().toggleCode().run()}>
        <Icon d={I.code} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.link')} active={editor.isActive('link')} onClick={setLink}>
        <Icon d={I.link} size={15} />
      </Btn>
      <Btn label={t('note_toolbar.image')} active={false} onClick={onInsertImage}>
        <Icon d={I.image} size={15} />
      </Btn>
    </div>
  )
}

function Btn({
  label,
  active,
  onClick,
  children,
}: {
  label: string
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className={'fx-tt-btn' + (active ? ' fx-tt-active' : '')}
      // preventDefault on mousedown keeps the editor selection from collapsing
      // when the button steals focus — the command then applies to the right range.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      aria-pressed={active}
      aria-label={label}
      data-tooltip={label}
    >
      {children}
    </button>
  )
}
