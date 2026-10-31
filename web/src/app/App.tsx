import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import type { Mode } from '../api/client'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { ErrorNote } from '../components/ErrorNote'
import { HelpDialog } from '../components/HelpDialog'
import { Intake } from '../components/Intake'
// The markdown stack (react-markdown + remark + rehype) is only needed once a result exists.
const MarkdownPane = lazy(() =>
  import('../components/MarkdownPane').then((m) => ({ default: m.MarkdownPane })),
)
import { MetaStrip } from '../components/MetaStrip'
import { Panes } from '../components/Panes'
import { SourcePane } from '../components/SourcePane'
import { StatusStrip } from '../components/StatusStrip'
import { TopBar } from '../components/TopBar'
import { usePlayground } from '../features/playground/usePlayground'
import { useI18n } from '../i18n'
import { describeExpiry } from '../features/playground/format'

export function App() {
  const { t } = useI18n()
  const { state, flags, start, cancel, release, reset, select, setMode } = usePlayground()
  const [help, setHelp] = useState(false)
  const [pendingFile, setPendingFile] = useState<File | null>(null)
  // 'auto' follows the result (markdown once it exists); a click pins an explicit choice.
  const [mobileChoice, setMobileChoice] = useState<'auto' | 'original' | 'markdown'>('auto')
  const [dragging, setDragging] = useState(false)
  const [now, setNow] = useState(() => Date.now())
  const mobileTab: 'original' | 'markdown' =
    mobileChoice === 'auto' ? (state.markdown ? 'markdown' : 'original') : mobileChoice

  // One clock for the elapsed timer and the "expires in" text; only ticks while needed.
  const ticking = flags.isWorking || state.phase === 'completed'
  useEffect(() => {
    if (!ticking) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [ticking])

  const elapsed = useMemo(() => {
    if (!state.job) return null
    const from = Date.parse(state.job.created_at)
    if (Number.isNaN(from)) return null
    const to = state.finishedAt ?? now
    return Math.max(0, to - from)
  }, [state.job, state.finishedAt, now])

  const holdsCopy = flags.holdsServerCopy

  const chooseFile = useCallback(
    (file: File) => {
      // Switching files while a result still exists server-side loses the preview of the old
      // document; ask first instead of silently orphaning it.
      if (holdsCopy && state.job) {
        setPendingFile(file)
        return
      }
      select(file)
      setMobileChoice('auto')
    },
    [holdsCopy, select, state.job],
  )
  // Drag a file anywhere on the page: the drop zone is small, and a page-wide target is what
  // people expect from a drop-based tool. The overlay is pointer-events-none so the drop lands
  // on the window listener.
  useEffect(() => {
    let depth = 0
    const hasFiles = (e: DragEvent) => Boolean(e.dataTransfer?.types.includes('Files'))
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth += 1
      setDragging(true)
    }
    const over = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault()
    }
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth = Math.max(0, depth - 1)
      if (depth === 0) setDragging(false)
    }
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth = 0
      setDragging(false)
      const file = e.dataTransfer?.files?.[0]
      if (file) chooseFile(file)
    }
    window.addEventListener('dragenter', enter)
    window.addEventListener('dragover', over)
    window.addEventListener('dragleave', leave)
    window.addEventListener('drop', drop)
    return () => {
      window.removeEventListener('dragenter', enter)
      window.removeEventListener('dragover', over)
      window.removeEventListener('dragleave', leave)
      window.removeEventListener('drop', drop)
    }
  }, [chooseFile])

  const begin = useCallback(() => {
    if (!state.file) return
    setMobileChoice('auto')
    void start(state.file, state.mode)
  }, [start, state.file, state.mode])

  const onMode = useCallback((m: Mode) => setMode(m), [setMode])

  return (
    <div className="flex h-full min-h-0 flex-col">
      {dragging && (
        <div className="pointer-events-none fixed inset-0 z-50 grid place-items-center bg-accent-600/10 backdrop-blur-[1px]">
          <div className="rounded-2xl border-2 border-dashed border-accent-500 bg-white/95 px-10 py-8 text-center shadow-lg dark:bg-ink-900/95">
            <p className="text-base font-semibold">{t('drop.overlayTitle')}</p>
            <p className="mt-1 text-xs text-ink-500">{t('drop.overlayBody')}</p>
          </div>
        </div>
      )}
      <TopBar onHelp={() => setHelp(true)} />

      <Intake
        file={state.file}
        mode={state.mode}
        busy={state.phase === 'uploading'}
        onSelect={chooseFile}
        onMode={onMode}
        onStart={begin}
        onReset={reset}
        hasJob={Boolean(state.job)}
      />

      {state.job && (
        <StatusStrip
          phase={state.phase}
          job={state.job}
          elapsedMs={elapsed}
          canCancel={flags.canCancel}
          canRelease={flags.canRelease}
          onCancel={() => void cancel()}
          onRelease={() => void release()}
          releasing={state.phase === 'releasing'}
        />
      )}
      <MetaStrip job={state.job} ir={state.ir} markdown={state.markdown} />

      {state.error && (
        <div className="px-4 pt-3">
          <ErrorNote code={state.error.code} message={state.error.message} />
        </div>
      )}

      {state.file ? (
        <main className="min-h-0 flex-1 border-t border-ink-200 dark:border-ink-800">
          <Panes
            mobileTab={{ value: mobileTab, onChange: setMobileChoice }}
            left={<SourcePane file={state.file} />}
            right={
              <Suspense
                fallback={
                  <div className="grid h-full place-items-center text-sm text-ink-500">
                    {t('app.loadingResult')}
                  </div>
                }
              >
                <MarkdownPane
                  markdown={state.markdown}
                  fileName={state.file.name}
                  phase={state.phase}
                  onRelease={() => void release()}
                  canRelease={flags.canRelease}
                />
              </Suspense>
            }
          />
        </main>
      ) : (
        <main className="flex min-h-0 flex-1 items-start justify-center overflow-auto" />
      )}

      <HelpDialog open={help} onClose={() => setHelp(false)} />
      <ConfirmDialog
        open={pendingFile !== null}
        title={t('confirm.replaceTitle')}
        body={
          state.job
            ? t('confirm.replaceBodyHeld', {
                job: `${state.job.job_id.slice(0, 12)}…`,
                when: describeExpiry(state.job.result_expires_at, t),
              })
            : t('confirm.replaceBody')
        }
        confirmLabel={t('confirm.replace')}
        cancelLabel={t('confirm.cancel')}
        onConfirm={() => {
          if (pendingFile) select(pendingFile)
          setPendingFile(null)
          setMobileChoice('auto')
        }}
        onCancel={() => setPendingFile(null)}
      />
    </div>
  )
}
