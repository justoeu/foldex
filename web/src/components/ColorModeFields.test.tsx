import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { ColorModeFields, DEFAULT_ENTITY_COLORS } from './ColorModeFields'
import { GradientPicker } from './GradientPicker'
import { renderWithProviders } from '../test/renderWithProviders'

describe('ColorModeFields', () => {
  it('solid and gradient pickers share one swatch list', () => {
    renderWithProviders(
      <>
        <ColorModeFields
          mode="solid"
          onModeChange={() => {}}
          solid="#6366F1"
          onSolidChange={() => {}}
          gradFrom="#6366F1"
          gradTo="#EC4899"
          onGradientChange={() => {}}
          i18nPrefix="tag_dialog"
        />
        <GradientPicker from="#6366F1" to="#EC4899" onChange={vi.fn()} />
      </>,
    )

    for (const color of DEFAULT_ENTITY_COLORS) {
      expect(screen.getByLabelText(`color ${color}`)).toBeInTheDocument()
      expect(screen.getByLabelText(`Start ${color}`)).toBeInTheDocument()
      expect(screen.getByLabelText(`End ${color}`)).toBeInTheDocument()
    }
  })
})
