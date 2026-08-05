import { useEffect } from 'react'
import { usePersistedState } from './usePersistedState'

/**
 * Dark-mode state plus the document-level class it drives.
 *
 * Extracted out of App so it can run ABOVE the auth gate. Left inside App, the
 * effect only mounts once a session exists — so a user who prefers dark would
 * get a white login screen, then a flash to dark the moment they signed in.
 * The preference describes the device, not the account, and the very first
 * paint should honour it.
 */
export function useDarkMode(): [boolean, (v: boolean | ((p: boolean) => boolean)) => void] {
  const [dark, setDark] = usePersistedState('foldex.dark', false)

  useEffect(() => {
    document.documentElement.classList.toggle('fx-dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    return () => {
      document.documentElement.classList.remove('fx-dark')
      document.documentElement.style.colorScheme = ''
    }
  }, [dark])

  return [dark, setDark]
}
