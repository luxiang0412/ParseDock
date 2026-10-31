import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { api } from '../api/client'
import { LOCALES, useI18n } from '../i18n'
import { Button } from './Button'

export function TopBar({ onHelp }: { onHelp: () => void }) {
  const { t, locale, setLocale } = useI18n()
  const health = useQuery({
    queryKey: ['health'],
    queryFn: () => api.health(),
    refetchInterval: 30_000,
    staleTime: 15_000,
  })
  const online = health.data === true
  const checked = health.isFetched
  const healthLabel = !checked
    ? t('health.checking')
    : online
      ? t('health.online')
      : t('health.offline')

  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-4 border-b border-ink-200 bg-white/80 px-4 backdrop-blur dark:border-ink-800 dark:bg-ink-950/80">
      <div className="flex min-w-0 items-center gap-3">
        <span
          aria-hidden="true"
          className="grid h-8 w-8 place-items-center rounded-lg bg-accent-600 text-white shadow-sm"
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
            <path d="M4 7.5h16M4 12h10M4 16.5h7" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" />
            <circle cx="18" cy="16.5" r="2.2" stroke="currentColor" strokeWidth="1.9" />
          </svg>
        </span>
        <div className="min-w-0">
          <h1 className="truncate text-[0.95rem] font-semibold tracking-tight">
            {t('app.name')} <span className="text-ink-500">{t('app.product')}</span>
          </h1>
          <p className="hidden truncate text-[0.7rem] text-ink-500 sm:block">{t('app.tagline')}</p>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <span
          className="hidden items-center gap-1.5 rounded-full border border-ink-200 px-2.5 py-1 text-xs text-ink-600 sm:inline-flex dark:border-ink-800 dark:text-ink-300"
          title={
            !checked
              ? t('health.titleChecking')
              : online
                ? t('health.titleOnline')
                : t('health.titleOffline')
          }
        >
          <span
            aria-hidden="true"
            className={clsx(
              'h-1.5 w-1.5 rounded-full',
              !checked ? 'bg-ink-400' : online ? 'bg-ok-500' : 'bg-danger-500',
            )}
          />
          {healthLabel}
        </span>

        <div
          role="group"
          aria-label={t('lang.label')}
          className="flex rounded-md border border-ink-200 p-0.5 text-xs dark:border-ink-800"
        >
          {LOCALES.map((l) => (
            <button
              key={l.id}
              type="button"
              onClick={() => setLocale(l.id)}
              aria-pressed={locale === l.id}
              lang={l.id}
              className={clsx(
                'rounded px-2 py-1 font-medium',
                locale === l.id
                  ? 'bg-ink-100 text-ink-900 dark:bg-ink-800 dark:text-white'
                  : 'text-ink-500 hover:text-ink-800 dark:hover:text-ink-200',
              )}
            >
              {t(l.labelKey)}
            </button>
          ))}
        </div>

        <Button size="sm" variant="ghost" onClick={onHelp} aria-haspopup="dialog">
          {t('app.help')}
        </Button>
      </div>
    </header>
  )
}
