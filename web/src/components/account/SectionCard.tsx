import type { ReactNode } from 'react'
import { Icon, I } from '../icons'

/**
 * The card every account panel is made of.
 *
 * Extracted rather than copied. It was written for the two-factor panel and
 * lived there alone for one session, which was long enough for the other three
 * to look broken next to it — profile a bare form, sign-in two unframed rows,
 * sessions a sentence with two buttons. A second copy of this markup would
 * have drifted on whichever panel nobody re-opened.
 */
export function SectionCard({
  icon,
  title,
  subtitle,
  badge,
  children,
}: {
  icon: ReactNode
  title: string
  subtitle?: string
  badge?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="fx-card">
      <div className="fx-card-body fx-sec-body">
        <header className="fx-sec-head">
          <span className="fx-sec-head-icon">
            <Icon d={icon} size={17} />
          </span>
          <div className="fx-sec-head-text">
            <h3 className="fx-sec-title">{title}</h3>
            {subtitle && <p className="fx-sec-sub">{subtitle}</p>}
          </div>
          {badge}
        </header>
        {children}
      </div>
    </section>
  )
}

export function SectionBadge({
  tone = 'off',
  children,
}: {
  tone?: 'on' | 'off' | 'warn'
  children: ReactNode
}) {
  return (
    <span className={`fx-sec-badge fx-sec-badge-${tone}`}>
      <Icon d={tone === 'on' ? I.check : I.info} size={12} />
      {children}
    </span>
  )
}

/**
 * Everything a panel says back to the user, in one shape.
 *
 * `ok` and `bad` carry the live regions the old one-liners already had — a
 * saved change is a `status`, a refused credential an `alert` — but at a weight
 * that survives being glanced at. `info` is neither: it explains, so announcing
 * it would interrupt a screen reader for something that was always on screen.
 */
export function Notice({
  tone,
  children,
}: {
  tone: 'ok' | 'bad' | 'info'
  children: ReactNode
}) {
  const glyph = tone === 'ok' ? I.check : tone === 'bad' ? I.alert : I.info
  return (
    <p
      className={`fx-sec-note fx-sec-note-${tone}`}
      role={tone === 'ok' ? 'status' : tone === 'bad' ? 'alert' : undefined}
      style={{ margin: 0 }}
    >
      <Icon d={glyph} size={14} />
      <span>{children}</span>
    </p>
  )
}

export function SectionBlock({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="fx-sec-block">
      <span className="fx-sec-block-label">{label}</span>
      {children}
    </div>
  )
}

/**
 * One thing the account has, with its state and what can be done about it.
 *
 * `note` and `lock` differ in kind, not in wording: a note is information, a
 * lock is the reason an action the user expected is not there. Rendering the
 * second as grey prose at the foot of a card is how "the buttons are blocked"
 * got reported — it has to sit in the row it describes.
 */
export function SectionRow({
  icon,
  name,
  hint,
  tone,
  state,
  note,
  lock,
  action,
  children,
}: {
  icon: ReactNode
  name: string
  hint?: string
  /** `on` tints the row for a capability the account HAS. */
  tone?: 'on' | 'warn' | 'danger'
  state?: { label: string; on: boolean }
  note?: string
  lock?: string
  action?: ReactNode
  /** A form belonging to this row, revealed beneath it. */
  children?: ReactNode
}) {
  return (
    /*
      A labelled group, not a list item. Every row is a heading plus the
      controls that act on that one thing, so a screen reader announcing
      "Password, group" before the button is the useful framing — "item 1 of 3"
      is not. It is also what lets a test scope to one row instead of the card.
    */
    <div
      className={`fx-sec-row${tone ? ` fx-sec-row-${tone}` : ''}`}
      role="group"
      aria-label={name}
    >
      <span className="fx-sec-row-icon">
        <Icon d={icon} size={15} />
      </span>
      <div className="fx-sec-row-text">
        <span className="fx-sec-row-name">{name}</span>
        {hint && <span className="fx-sec-row-hint">{hint}</span>}
        {lock && (
          <span className="fx-sec-lock">
            <Icon d={I.lock} size={11} /> {lock}
          </span>
        )}
        {note && (
          <span className="fx-sec-row-note">
            <Icon d={I.info} size={11} /> {note}
          </span>
        )}
      </div>
      {state && (
        <span className={`fx-sec-state ${state.on ? 'fx-sec-state-on' : 'fx-sec-state-off'}`}>
          {state.label}
        </span>
      )}
      {action && <div className="fx-sec-row-action">{action}</div>}
      {children && <div className="fx-sec-row-form">{children}</div>}
    </div>
  )
}
