import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import { AvailabilityHint } from './AvailabilityHint'
import { renderWithProviders } from '../test/renderWithProviders'
import type { Availability } from '../hooks/useAvailability'

function render(result: Availability, shapeText?: string) {
  return renderWithProviders(<AvailabilityHint result={result} shapeText={shapeText} />)
}

describe('the availability hint', () => {
  it('renders nothing while idle, so the row does not jump as you type', () => {
    const { container } = render({ state: 'idle' })
    expect(container).toBeEmptyDOMElement()
  })

  // The backend spends a separate code path and its own test case to tell
  // "reserved" from "wrong shape", because the two have different fixes. If the
  // hint collapses them, someone who typed `admin` goes hunting for a bad
  // character that is not there.
  it('tells a reserved name apart from a malformed one', () => {
    const { unmount } = render({ state: 'refused', reason: 'reserved' })
    expect(screen.getByText(/reserved/i)).toBeInTheDocument()
    unmount()

    render({ state: 'refused', reason: 'shape' })
    expect(screen.queryByText(/reserved/i)).not.toBeInTheDocument()
    expect(screen.getByText(/not a usable value/i)).toBeInTheDocument()
  })

  // `empty` used to land on the shape message only by accident of a ternary's
  // final `else`; the lookup makes it deliberate.
  it('maps an empty reason to the shape message deliberately', () => {
    render({ state: 'refused', reason: 'empty' })
    expect(screen.getByText(/not a usable value/i)).toBeInTheDocument()
  })

  it('lets a field state its own shape rule instead of the generic one', () => {
    render({ state: 'refused', reason: 'shape' }, 'A username is 3 to 32 characters.')
    expect(screen.getByText(/3 to 32 characters/i)).toBeInTheDocument()
    expect(screen.queryByText(/not a usable value/i)).not.toBeInTheDocument()
  })

  it('shows the free and checking arms', () => {
    const { unmount } = render({ state: 'free' })
    expect(screen.getByText(/available/i)).toBeInTheDocument()
    unmount()
    render({ state: 'checking' })
    expect(screen.getByText(/checking/i)).toBeInTheDocument()
  })

  // A failed probe is not a verdict on the value, and the copy promises the
  // save still works. It must not read as a refusal.
  it('says a failed check is not a refusal', () => {
    render({ state: 'error' })
    expect(screen.getByText(/you can still save/i)).toBeInTheDocument()
  })

  it('renders a warning that is not a refusal', () => {
    render({ state: 'warn', reason: 'pending' })
    expect(screen.getByText(/already migrating|already moving/i)).toBeInTheDocument()
  })

  // Its own doc comment argues against `alert`: this narrates a field the user
  // is looking at, and interrupting on every debounce would make the field
  // unusable for exactly the people who cannot see the check mark.
  it('is a status region, never an alert', () => {
    render({ state: 'refused', reason: 'taken' })
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
