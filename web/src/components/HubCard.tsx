import type { ReactNode } from 'react'
import { Icon, I } from './icons'

type CardProps = {
  icon: ReactNode
  /** Tone modifier class (`fx-tone-*`) applied to the icon square. */
  tone: string
  title: string
  desc: string
  /** Label of the affordance in the card's footer. */
  action: string
  /** Optional state badge in the top-right corner. */
  status?: string
  statusTone?: string
  onClick: () => void
}

type ShortcutProps = Omit<CardProps, 'action' | 'status' | 'statusTone'>

/**
 * The one card the settings hub uses, in both scopes. Sharing it is what keeps
 * a spacing change from landing on one grid and not the other.
 */
export function HubCard({ icon, tone, title, desc, action, status, statusTone, onClick }: CardProps) {
  return (
    <button type="button" className="fx-acard" onClick={onClick}>
      <div className="fx-acard-head">
        <span className={'fx-acard-icon ' + tone}><Icon d={icon} size={19} /></span>
        {status && <span className={'fx-chip ' + (statusTone ?? '')}>{status}</span>}
      </div>
      <div>
        <div className="fx-acard-title">{title}</div>
        <div className="fx-acard-desc">{desc}</div>
      </div>
      <span className="fx-acard-action">
        {action} <Icon d={I.chevronRight} size={13} />
      </span>
    </button>
  )
}

/** Compact navigation card for the "data and shortcuts" strip. */
export function HubShortcut({ icon, tone, title, desc, onClick }: ShortcutProps) {
  return (
    <button type="button" className="fx-scut" onClick={onClick}>
      <span className={'fx-acard-icon ' + tone}><Icon d={icon} size={17} /></span>
      <span>
        <span className="fx-scut-title">{title}</span>
        <span className="fx-scut-desc">{desc}</span>
      </span>
    </button>
  )
}

/** Section heading whose rule runs to the container edge. */
export function HubRule({ label }: { label: string }) {
  return (
    <p className="fx-hub-rule">
      <span className="fx-hub-section-label">{label}</span>
    </p>
  )
}
