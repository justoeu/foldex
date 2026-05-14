import '@testing-library/jest-dom/vitest'
import { afterEach, vi } from 'vitest'
import { cleanup } from '@testing-library/react'

// Initialise i18n before any component renders so `useTranslation()` returns
// real translations. English is the default — tests assert against en.json.
import '../i18n'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

// jsdom does not implement window.matchMedia (MUI uses it for breakpoints).
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

// jsdom's localStorage can be unreliable in some Vitest versions.
if (typeof window !== 'undefined') {
  const store: Record<string, string> = {}
  Object.defineProperty(window, 'localStorage', {
    value: {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => { store[k] = String(v) },
      removeItem: (k: string) => { delete store[k] },
      clear: () => { Object.keys(store).forEach((k) => delete store[k]) },
      get length() { return Object.keys(store).length },
      key: (i: number) => Object.keys(store)[i] ?? null,
    },
    writable: true,
  })
}

// jsdom missing URL.createObjectURL / revokeObjectURL — backup download path
// calls both when the user clicks "Gerar backup completo".
if (typeof URL !== 'undefined' && !URL.createObjectURL) {
  URL.createObjectURL = vi.fn(() => 'blob:mock')
  URL.revokeObjectURL = vi.fn()
}

// jsdom Blob lacks .arrayBuffer in older versions — the backup hook reads it
// to extract manifest counts from the zip.
if (typeof Blob !== 'undefined' && !Blob.prototype.arrayBuffer) {
  Blob.prototype.arrayBuffer = function () {
    return new Promise((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(r.result as ArrayBuffer)
      r.onerror = () => reject(r.error)
      r.readAsArrayBuffer(this as Blob)
    })
  }
}
