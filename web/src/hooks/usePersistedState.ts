import { useState, useEffect, useCallback, useRef, type SetStateAction } from 'react'

// Reads from localStorage on mount, writes back on every change. SSR-safe
// with a typeof guard. Falls back to in-memory state when localStorage is
// unavailable (private browsing, SSR).
export function usePersistedState<T>(
  key: string,
  fallback: T,
  validate: (value: unknown) => value is T = (value): value is T => matchesShape(value, fallback),
): [T, (v: T | ((prev: T) => T)) => void] {
  const persistPending = useRef(false)
  const [value, setValue] = useState<T>(() => {
    if (typeof localStorage === 'undefined') return fallback
    try {
      const raw = localStorage.getItem(key)
      if (raw === null) return fallback
      const parsed: unknown = JSON.parse(raw)
      if (validate(parsed)) return parsed
      localStorage.removeItem(key)
      return fallback
    } catch {
      try { localStorage.removeItem(key) } catch { /* storage unavailable */ }
      return fallback
    }
  })

  useEffect(() => {
    if (!persistPending.current) return
    persistPending.current = false
    if (typeof localStorage === 'undefined') return
    try {
      localStorage.setItem(key, JSON.stringify(value))
    } catch { /* quota exceeded or private browsing — non-fatal */ }
  }, [key, value])

  const setPersistedValue = useCallback((next: SetStateAction<T>) => {
    persistPending.current = true
    setValue(next)
  }, [])

  return [value, setPersistedValue]
}

// Per-context persisted map (e.g. viewMode per 'home' / 'folder.42').
// The entire map lives under one localStorage key; get(key) returns the
// saved value or the fallback; set(key, val) patches one slot.
export function usePersistedMap<T>(
  storageKey: string,
  fallback: T,
  validate: (value: unknown) => value is T = (value): value is T => matchesShape(value, fallback),
): {
  map: Record<string, T>
  get: (ctx: string) => T
  set: (ctx: string, v: T) => void
  setAll: (fn: (prev: Record<string, T>) => Record<string, T>) => void
} {
  const [map, setMap] = usePersistedState<Record<string, T>>(
    storageKey,
    {},
    (value): value is Record<string, T> =>
      isPlainRecord(value) && Object.values(value).every(validate),
  )

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

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function matchesShape(value: unknown, fallback: unknown): boolean {
  if (fallback === null) return value === null
  if (Array.isArray(fallback)) return Array.isArray(value)
  if (isPlainRecord(fallback)) {
    if (!isPlainRecord(value)) return false
    return Object.entries(fallback).every(([key, sample]) =>
      key in value && matchesShape(value[key], sample),
    )
  }
  return typeof value === typeof fallback
}
