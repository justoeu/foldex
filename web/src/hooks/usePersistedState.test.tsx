import { beforeEach, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { isBoolean, usePersistedMap, usePersistedState } from './usePersistedState'

function PersistedValue<T>({
  storageKey,
  fallback,
  validate,
}: {
  storageKey: string
  fallback: T
  validate: (value: unknown) => value is T
}) {
  const [value] = usePersistedState(storageKey, fallback, validate)
  return <div data-testid="value">{JSON.stringify(value)}</div>
}

function PersistedMapValue<T>({
  storageKey,
  fallback,
  validate,
}: {
  storageKey: string
  fallback: T
  validate: (value: unknown) => value is T
}) {
  const persisted = usePersistedMap(storageKey, fallback, validate)
  return <div data-testid="value">{JSON.stringify(persisted.get('home'))}</div>
}

beforeEach(() => localStorage.clear())

describe('usePersistedState', () => {
  it('falls back when decoded state has the wrong shape', () => {
    localStorage.setItem('preference', JSON.stringify({ unexpected: true }))

    render(<PersistedValue storageKey="preference" fallback={false} validate={isBoolean} />)

    expect(screen.getByTestId('value')).toHaveTextContent('false')
    expect(localStorage.getItem('preference')).toBeNull()
  })

  it('rejects same-type values outside the preference domain', () => {
    localStorage.setItem('grid', '7')

    render(
      <PersistedValue
        storageKey="grid"
        fallback={5 as 3 | 5 | 8}
        validate={(value): value is 3 | 5 | 8 => value === 3 || value === 5 || value === 8}
      />,
    )

    expect(screen.getByTestId('value')).toHaveTextContent('5')
    expect(localStorage.getItem('grid')).toBeNull()
  })

  it('rejects null and invalid values in persisted maps', () => {
    localStorage.setItem('map', JSON.stringify({ home: 'garbage', 'folder.1': 'cards' }))
    const { unmount } = render(
      <PersistedMapValue
        storageKey="map"
        fallback={'cards' as 'cards' | 'compact' | 'list'}
        validate={(value): value is 'cards' | 'compact' | 'list' =>
          value === 'cards' || value === 'compact' || value === 'list'}
      />,
    )
    expect(screen.getByTestId('value')).toHaveTextContent('"cards"')
    expect(localStorage.getItem('map')).toBeNull()
    unmount()

    localStorage.setItem('map-null', 'null')
    render(
      <PersistedMapValue
        storageKey="map-null"
        fallback="cards"
        validate={(value): value is string => value === 'cards'}
      />,
    )
    expect(screen.getByTestId('value')).toHaveTextContent('"cards"')
    expect(localStorage.getItem('map-null')).toBeNull()
  })

  it('accepts boolean map entries only when every value is boolean', () => {
    localStorage.setItem('boolean-map', JSON.stringify({ home: 'false' }))

    render(<PersistedMapValue storageKey="boolean-map" fallback={false} validate={isBoolean} />)

    expect(screen.getByTestId('value')).toHaveTextContent('false')
    expect(localStorage.getItem('boolean-map')).toBeNull()
  })
})
