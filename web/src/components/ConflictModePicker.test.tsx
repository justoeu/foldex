import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConflictModePicker } from './ConflictModePicker'
import { renderWithProviders } from '../test/renderWithProviders'

const labels = {
  skipTitle: 'Skip conflicts',
  skipDesc: 'keep current',
  duplicateTitle: 'Duplicate',
  duplicateDesc: 'rename',
  wipeTitle: '⚠ Wipe everything',
  wipeDesc: 'destructive',
}

describe('ConflictModePicker', () => {
  it('encodes wipe as danger and skip/duplicate as accent', async () => {
    const onChange = vi.fn()
    const { rerender } = renderWithProviders(
      <ConflictModePicker value="skip" onChange={onChange} wipeDanger labels={labels} />,
    )

    const skip = screen.getByRole('button', { name: /skip conflicts/i })
    const duplicate = screen.getByRole('button', { name: /^duplicate/i })
    const wipe = screen.getByRole('button', { name: /wipe everything/i })

    expect(skip.getAttribute('style')).toContain('--fx-accent')
    expect(wipe.querySelector('span')?.getAttribute('style')).toContain('--fx-danger')
    expect(wipe).toHaveTextContent('⚠')

    await userEvent.setup().click(wipe)
    expect(onChange).toHaveBeenCalledWith('wipe')

    rerender(<ConflictModePicker value="wipe" onChange={onChange} wipeDanger labels={labels} />)
    const activeWipe = screen.getByRole('button', { name: /wipe everything/i })
    expect(activeWipe.getAttribute('style')).toContain('--fx-danger')
    expect(duplicate.getAttribute('style')).not.toContain('--fx-danger')
  })
})
