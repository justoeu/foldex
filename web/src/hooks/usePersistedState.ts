import { useState, useEffect, useCallback } from 'react'

// Reads from localStorage on mount, writes back on every change. SSR-safe
// with a typeof guard. Falls back to in-memory state when localStorage is
// unavailable (private browsing, SSR).
export function usePersistedState<T>(
  key: string,
  fallback: T,
): [T, (v: T | ((prev: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    if (typeof localStorage === 'undefined') return fallback
    try {
      const raw = localStorage.getItem(key)
      return raw !== null ? JSON.parse(raw) : fallback
    } catch {
      return fallback
    }
  })

  useEffect(() => {
    if (typeof localStorage === 'undefined') return
    try {
      localStorage.setItem(key, JSON.stringify(value))
    } catch { /* quota exceeded or private browsing — non-fatal */ }
  }, [key, value])

  return [value, setValue]
}

// Per-context persisted map (e.g. viewMode per 'home' / 'folder.42').
// The entire map lives under one localStorage key; get(key) returns the
// saved value or the fallback; set(key, val) patches one slot.
export function usePersistedMap<T>(
  storageKey: string,
  fallback: T,
): {
  map: Record<string, T>
  get: (ctx: string) => T
  set: (ctx: string, v: T) => void
  setAll: (fn: (prev: Record<string, T>) => Record<string, T>) => void
} {
  const [map, setMap] = usePersistedState<Record<string, T>>(storageKey, {})

  const get = useCallback(
    (ctx: string): T => (ctx in map ? map[ctx] : fallback),
    [map, fallback],
  )

  const set = useCallback(
    (ctx: string, v: T) => setMap((prev) => ({ ...prev, [ctx]: v })),
    [setMap],
  )

  return { map, get, set, setAll: setMap }
}
