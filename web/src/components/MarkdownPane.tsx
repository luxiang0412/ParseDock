import clsx from 'clsx'
import { useMemo, useState } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import rehypeSanitize from 'rehype-sanitize'
import remarkGfm from 'remark-gfm'
import { useI18n } from '../i18n'
import { Button } from './Button'

type Tab = 'rendered' | 'source'

/**
 * Remote images are never fetched: the document is untrusted, and loading them would leak the
 * fact that a document was read (and the reader's IP) to a third party. Links open in a new
 * tab without a referrer.
 */
function markdownComponents(title: string, image: string): Components {
  return {
    a: ({ href, children }) => (
      <a href={href} target="_blank" rel="noopener noreferrer nofollow">
        {children}
      </a>
    ),
    img: ({ src, alt }) => (
      <span className="remote-image" title={title}>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <rect x="3.5" y="5" width="17" height="14" rx="2" stroke="currentColor" strokeWidth="1.6" />
          <path d="M3.5 16l5-5 4 4 3-3 5 5" stroke="currentColor" strokeWidth="1.6" />
        </svg>
        {alt || image} · {typeof src === 'string' ? shorten(src) : '—'}
      </span>
    ),
  }
}

function shorten(v: string): string {
  try {
    return new URL(v, location.href).host
  } catch {
    return 'not loaded'
  }
}

export function MarkdownPane({
  markdown,
  fileName,
  phase,
  onRelease,
  canRelease,
}: {
  markdown: string | null
  fileName: string
  phase: string
  onRelease: () => void
  canRelease: boolean
}) {
  const { t } = useI18n()
  const components = useMemo(
    () => markdownComponents(t('md.remoteImage'), t('md.image')),
    [t],
  )
  const [tab, setTab] = useState<Tab>('rendered')
  const [copied, setCopied] = useState<'idle' | 'ok' | 'fail'>('idle')
  const chars = useMemo(() => (markdown ? [...markdown].length : 0), [markdown])

  const copy = async () => {
    if (!markdown) return
    try {
      await navigator.clipboard.writeText(markdown)
      setCopied('ok')
    } catch {
      // Clipboard API needs a secure context and permission; fall back to a selection copy.
      const ta = document.createElement('textarea')
      ta.value = markdown
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.append(ta)
      ta.select()
      setCopied(document.execCommand('copy') ? 'ok' : 'fail')
      ta.remove()
    }
    window.setTimeout(() => setCopied('idle'), 1600)
  }

  const download = () => {
    if (!markdown) return
    const base = fileName.replace(/\.[^.]+$/, '')
    const blob = new Blob([markdown], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${base || 'parsedock'}.md`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <section
      data-testid="markdown-pane"
      className="flex h-full min-h-0 flex-col bg-white dark:bg-ink-950"
      aria-label={t('md.preview')}
    >
      <div className="flex flex-wrap items-center gap-2 border-b border-ink-200 px-3 py-2 dark:border-ink-800">
        <div role="tablist" aria-label={t('md.view')} className="flex rounded-md border border-ink-200 p-0.5 dark:border-ink-800">
          {(['rendered', 'source'] as Tab[]).map((t_) => (
            <button
              key={t_}
              role="tab"
              aria-selected={tab === t_}
              type="button"
              onClick={() => setTab(t_)}
              className={clsx(
                'rounded px-2.5 py-1 text-xs font-medium',
                tab === t_
                  ? 'bg-ink-100 text-ink-900 dark:bg-ink-800 dark:text-white'
                  : 'text-ink-500 hover:text-ink-800 dark:hover:text-ink-200',
              )}
            >
              {t_ === 'rendered' ? t('md.preview') : t('md.source')}
            </button>
          ))}
        </div>

        <span className="hidden font-mono text-[0.7rem] text-ink-400 sm:inline">
          {chars ? t('md.chars', { count: chars.toLocaleString() }) : ''}
        </span>

        <div className="ml-auto flex items-center gap-1.5">
          <Button size="sm" variant="ghost" onClick={copy} disabled={!markdown}>
            {copied === 'ok' ? t('md.copied') : copied === 'fail' ? t('md.copyFailed') : t('md.copy')}
          </Button>
          <Button size="sm" variant="ghost" onClick={download} disabled={!markdown}>
            {t('md.download')}
          </Button>
          {canRelease && (
            // The status bar carries the same action; on a phone one copy is enough.
            <Button
              size="sm"
              variant="secondary"
              onClick={onRelease}
              className="hidden sm:inline-flex"
            >
              {t('md.release')}
            </Button>
          )}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {!markdown ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
            <p className="text-sm font-medium text-ink-500">
              {phase === 'completed' ? t('md.loading') : t('md.emptyTitle')}
            </p>
            <p className="max-w-xs text-xs text-ink-400">{t('md.emptyBody')}</p>
          </div>
        ) : tab === 'rendered' ? (
          <article className="md px-5 py-5">
            <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]} components={components}>
              {markdown}
            </ReactMarkdown>
          </article>
        ) : (
          <pre className="whitespace-pre-wrap break-words px-5 py-5 font-mono text-[0.8rem] leading-6">
            {markdown}
          </pre>
        )}
      </div>
    </section>
  )
}
