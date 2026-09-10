import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuditTimeline } from './AuditTimeline'
import { renderWithProviders } from '../../test/renderWithProviders'
import type { AuditEntry } from '../../api/admin'

const entry: AuditEntry = {
  id: 9,
  action: 'link.updated',
  category: 'content',
  severity: 'info',
  actor_email: null,
  actor_ref: 2,
  target_email: null,
  detail: null,
  ip: '192.168.107.5',
  ip_trusted: false,
  user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/152.0.0.0',
  created_at: '2026-09-04T15:25:54.000Z',
}

describe('AuditTimeline', () => {
  it('stacks the provenance panel under the row, not beside it', async () => {
    const { container } = renderWithProviders(<AuditTimeline entries={[entry]} />)
    await userEvent.setup().click(screen.getByRole('button', { expanded: false }))
    const event = container.querySelector('.fx-aud-event') as HTMLElement
    const body = event.querySelector('.fx-aud-event-body') as HTMLElement
    expect(body).toContainElement(screen.getByRole('button', { expanded: true }))
    expect(body).toContainElement(container.querySelector('.fx-aud-details') as HTMLElement)
    expect(event.querySelector(':scope > .fx-aud-details')).toBeNull()
    expect(screen.getByText(/192\.168\.107\.5/)).toBeInTheDocument()
    expect(screen.getByText(/direct address, no proxy/i)).toBeInTheDocument()
  })
})
