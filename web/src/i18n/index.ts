import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import en from './locales/en.json'
import pt from './locales/pt.json'
import es from './locales/es.json'

// Supported locales — kept tiny on purpose. Add a new one by dropping a JSON
// next to the others and listing it here. The LocalePicker reads this list
// so the dropdown stays in sync automatically.
export const SUPPORTED_LOCALES = [
  { code: 'en', label: 'English', flag: '🇺🇸' },
  { code: 'pt', label: 'Português', flag: '🇧🇷' },
  { code: 'es', label: 'Español', flag: '🇪🇸' },
] as const

export type LocaleCode = (typeof SUPPORTED_LOCALES)[number]['code']

const STORAGE_KEY = 'foldex.locale'

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      pt: { translation: pt },
      es: { translation: es },
    },
    fallbackLng: 'en',
    supportedLngs: SUPPORTED_LOCALES.map((l) => l.code),
    nonExplicitSupportedLngs: true, // `pt-BR` → `pt`, `en-US` → `en`, etc.
    interpolation: { escapeValue: false }, // React already escapes
    detection: {
      // Persisted user choice wins over the browser hint. Cookie path is
      // unused because foldex is single-user / single-machine — localStorage
      // is the authoritative store.
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      lookupLocalStorage: STORAGE_KEY,
    },
    returnNull: false,
  })

export default i18n
