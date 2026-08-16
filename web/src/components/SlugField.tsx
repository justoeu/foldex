import { useEffect, useState, type Dispatch, type SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'
import { slugifyClient } from '../lib/slugify'
import type { SlugValue } from '../lib/slugValue'
import { Icon, I } from './icons'

type SlugState = SlugValue & { identity: string }

export function useSlugFieldState(
  open: boolean,
  title: string,
  initialSlug: string | undefined,
  entityId: number | null,
) {
  const identity = `${open ? 'open' : 'closed'}:${entityId ?? 'create'}`
  const initial = (): SlugState => ({
    identity,
    slug: initialSlug ?? '',
    slugDirty: Boolean(initialSlug),
  })
  const [state, setState] = useState<SlugState>(initial)
  const current = state.identity === identity ? state : initial()
  useEffect(() => {
    setState({ identity, slug: initialSlug ?? '', slugDirty: Boolean(initialSlug) })
  }, [identity, initialSlug])

  const slug = current.slugDirty ? current.slug : slugifyClient(title)
  const setSlug: Dispatch<SetStateAction<string>> = (next) => {
    setState((previous) => {
      const active = previous.identity === identity ? previous : current
      const activeSlug = active.slugDirty ? active.slug : slugifyClient(title)
      return {
        ...active,
        identity,
        slug: typeof next === 'function' ? next(activeSlug) : next,
      }
    })
  }
  const setSlugDirty: Dispatch<SetStateAction<boolean>> = (next) => {
    setState((previous) => {
      const active = previous.identity === identity ? previous : current
      return {
        ...active,
        identity,
        slugDirty: typeof next === 'function' ? next(active.slugDirty) : next,
      }
    })
  }

  return { slug, slugDirty: current.slugDirty, setSlug, setSlugDirty }
}

type Props = SlugValue & {
  title: string
  setSlug: Dispatch<SetStateAction<string>>
  setSlugDirty: Dispatch<SetStateAction<boolean>>
  routePrefix: '/go/' | '/n/'
  i18nPrefix: 'link_dialog' | 'note_dialog'
  fallback: string
}

export function SlugField({
  title,
  slug,
  slugDirty,
  setSlug,
  setSlugDirty,
  routePrefix,
  i18nPrefix,
  fallback,
}: Props) {
  const { t } = useTranslation()
  const reset = () => {
    setSlug(slugifyClient(title))
    setSlugDirty(false)
  }

  return (
    <label className="fx-field">
      <span className="fx-field-label">{t(`${i18nPrefix}.slug_label`)}</span>
      <div className="fx-input">
        <span style={{ color: 'var(--fx-ink-4)', fontFamily: 'var(--fx-mono)', fontSize: 12, paddingRight: 4 }}>{routePrefix}</span>
        <input
          value={slug}
          onChange={(event) => {
            setSlug(event.target.value)
            setSlugDirty(true)
          }}
          placeholder={slugifyClient(title) || fallback}
          aria-label={t(`${i18nPrefix}.slug_aria`)}
          pattern="[a-z0-9]+(-[a-z0-9]+)*"
          style={{ fontFamily: 'var(--fx-mono)' }}
        />
        {slugDirty && (
          <button
            type="button"
            className="fx-iconbtn"
            onClick={reset}
            data-tooltip={t(`${i18nPrefix}.slug_reset_tooltip`)}
            aria-label={t(`${i18nPrefix}.slug_reset_tooltip`)}
          >
            <Icon d={I.refresh} size={13} />
          </button>
        )}
      </div>
      <span className="fx-field-hint">{t(`${i18nPrefix}.slug_hint`)}</span>
    </label>
  )
}
