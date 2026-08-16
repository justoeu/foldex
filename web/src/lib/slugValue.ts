export type SlugValue = {
  slug: string
  slugDirty: boolean
}

export function createSlugValue(value: SlugValue): string | undefined {
  const slug = value.slug.trim()
  return value.slugDirty && slug ? slug : undefined
}

export function updateSlugValue(value: SlugValue): string | null | undefined {
  return value.slugDirty ? value.slug.trim() || null : undefined
}
