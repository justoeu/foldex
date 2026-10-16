import { Icon, I } from '../icons'

type Props = Readonly<{
  message: string | null
}>

export function NoteConflictNotice({ message }: Props) {
  if (!message) return null
  return (
    <div role="alert" style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4, padding: '0 20px 8px' }}>
      <Icon d={I.alert} size={12} /> {message}
    </div>
  )
}
