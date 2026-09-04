export function entryAnchor(kind: 'link' | 'note', id: number): string {
  return `${kind}-${id}`
}
