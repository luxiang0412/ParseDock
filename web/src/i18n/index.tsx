import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { MESSAGES, type Locale, type MessageKey } from './messages'

export type { Locale, MessageKey }
export { LOCALES, MESSAGES } from './messages'

const STORAGE_KEY = 'parsedock.playground.locale'

function detectLocale(): Locale {
  if (typeof window === 'undefined') return 'en'
  const stored = window.localStorage.getItem(STORAGE_KEY)
  if (stored === 'en' || stored === 'zh-CN') return stored
  const nav = window.navigator.language || ''
  return nav.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

export type Vars = Record<string, string | number>

export function interpolate(template: string, vars?: Vars): string {
  if (!vars) return template
  return template.replace(/\{(\w+)\}/g, (whole, key: string) =>
    key in vars ? String(vars[key]) : whole,
  )
}

/** Translation outside React (tests, error mapping, non-component helpers). */
export function translate(locale: Locale, key: MessageKey, vars?: Vars): string {
  const dictionary = MESSAGES[locale]
  return interpolate(dictionary[key] ?? MESSAGES.en[key] ?? String(key), vars)
}

interface I18nValue {
  locale: Locale
  setLocale: (locale: Locale) => void
  t: (key: MessageKey, vars?: Vars) => string
}

const I18nContext = createContext<I18nValue | null>(null)

export function I18nProvider({
  children,
  initialLocale,
}: {
  children: ReactNode
  /** Tests pass a locale explicitly; the app detects it from the browser. */
  initialLocale?: Locale
}) {
  const [locale, setLocaleState] = useState<Locale>(() => initialLocale ?? detectLocale())

  useEffect(() => {
    document.documentElement.lang = locale
    window.localStorage.setItem(STORAGE_KEY, locale)
  }, [locale])

  const setLocale = useCallback((next: Locale) => setLocaleState(next), [])

  const value = useMemo<I18nValue>(() => {
    const dictionary = MESSAGES[locale]
    return {
      locale,
      setLocale,
      t: (key, vars) => interpolate(dictionary[key] ?? MESSAGES.en[key] ?? String(key), vars),
    }
  }, [locale, setLocale])

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export function useI18n(): I18nValue {
  const value = useContext(I18nContext)
  if (!value) throw new Error('useI18n must be used inside <I18nProvider>')
  return value
}
