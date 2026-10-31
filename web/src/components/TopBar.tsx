import { useQuery } from '@tanstack/react-query'
import { api } from '../api/client'
import { Button } from './Button'

export function TopBar({ onHelp }: { onHelp: () => void }) {
  const health = useQuery({
    queryKey: ['health'],
    queryFn: () => api.health(),
    refetchInterval: 30_000,
    staleTime: 15_000,
  })
  const online = health.data === true
  const checked = health.isFetched

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
            ParseDock <span className="text-ink-500">Playground</span>
          </h1>
          <p className="hidden truncate text-[0.7rem] text-ink-500 sm:block">
            Upload a document, compare the original with the parsed Markdown
          </p>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <span
          className="hidden items-center gap-1.5 rounded-full border border-ink-200 px-2.5 py-1 text-xs text-ink-600 sm:inline-flex dark:border-ink-800 dark:text-ink-300"
          title={online ? 'The API replica answered /healthz' : 'No answer from /healthz'}
        >
          <span
            aria-hidden="true"
            className={`h-1.5 w-1.5 rounded-full ${
              !checked ? 'bg-ink-400' : online ? 'bg-ok-500' : 'bg-danger-500'
            }`}
          />
          {!checked ? 'checking API' : online ? 'API online' : 'API unreachable'}
        </span>
        <Button size="sm" variant="ghost" onClick={onHelp} aria-haspopup="dialog">
          How it works
        </Button>
      </div>
    </header>
  )
}
