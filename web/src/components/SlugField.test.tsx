import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SlugField } from './SlugField'
import { createSlugValue, updateSlugValue } from '../lib/slugValue'
import { renderWithProviders } from '../test/renderWithProviders'

function Harness({ routePrefix, i18nPrefix }: { routePrefix: '/go/' | '/n/'; i18nPrefix: 'link_dialog' | 'note_dialog' }) {
  const [slug, setSlug] = useState('derived-title')
  const [dirty, setDirty] = useState(false)
  return (
    <SlugField
      title="Derived title"
      slug={slug}
      slugDirty={dirty}
      setSlug={setSlug}
      setSlugDirty={setDirty}
      routePrefix={routePrefix}
      i18nPrefix={i18nPrefix}
      fallback="fallback"
    />
  )
}

describe('SlugField', () => {
  it.each([
    ['/go/', 'link_dialog', /short URL slug/i],
    ['/n/', 'note_dialog', /note slug/i],
  ] as const)('shares dirty and reset behavior for %s', async (routePrefix, i18nPrefix, ariaName) => {
    renderWithProviders(<Harness routePrefix={routePrefix} i18nPrefix={i18nPrefix} />)
    const user = userEvent.setup()
    const input = screen.getByRole('textbox', { name: ariaName })

    await user.clear(input)
    await user.type(input, 'custom-slug')
    expect(screen.getByText(routePrefix)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /reset/i }))
    expect(input).toHaveValue('derived-title')
  })

  it('shares create omission and update regeneration wire semantics', () => {
    expect(createSlugValue({ slug: ' ignored ', slugDirty: false })).toBeUndefined()
    expect(createSlugValue({ slug: ' custom ', slugDirty: true })).toBe('custom')
    expect(createSlugValue({ slug: ' ', slugDirty: true })).toBeUndefined()
    expect(updateSlugValue({ slug: ' ignored ', slugDirty: false })).toBeUndefined()
    expect(updateSlugValue({ slug: ' custom ', slugDirty: true })).toBe('custom')
    expect(updateSlugValue({ slug: ' ', slugDirty: true })).toBeNull()
  })
})
