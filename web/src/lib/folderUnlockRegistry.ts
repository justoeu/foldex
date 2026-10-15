// Session-only mirror of the navigation layer's folder unlocks, so the API
// hooks can attach X-Foldex-Folder-Unlock to direct entity reads and
// mutations (the backend gates those exactly like the folder-scoped lists).
// The navigation state stays the source of truth; this registry only ever
// reflects it and is cleared with the tenant's other session state.

export type FolderUnlockEntry = { token: string; expiresAt: number }

const unlocks = new Map<number, FolderUnlockEntry>()

export function syncFolderUnlocks(next: Record<number, FolderUnlockEntry>): void {
  unlocks.clear()
  for (const [rawId, entry] of Object.entries(next)) {
    unlocks.set(Number(rawId), entry)
  }
}

export function folderUnlockTokenFor(folderId: number | null | undefined, now = Date.now()): string | undefined {
  if (folderId == null) return undefined
  const entry = unlocks.get(folderId)
  return entry && entry.expiresAt > now ? entry.token : undefined
}

export function clearFolderUnlocks(): void {
  unlocks.clear()
}
