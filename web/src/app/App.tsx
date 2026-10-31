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
import { describeExpiry } from '../features/playground/format'
import { usePlayground } from '../features/playground/usePlayground'

export function App() {
  const { state, flags, start, cancel, release, reset, select, setMode } = usePlayground()
  const [help, setHelp] = useState(false)
  const [pendingFile, setPendingFile] = useState<File | null>(null)
  // 'auto' follows the result (markdown once it exists); a click pins an explicit choice.
  const [mobileChoice, setMobileChoice] = useState<'auto' | 'original' | 'markdown'>('auto')
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

  const chooseFile = useCallback(
    (file: File) => {
      // Switching files while a result still exists server-side loses the preview of the old
      // document; ask first instead of silently orphaning it.
      if (flags.holdsServerCopy && state.job) {
        setPendingFile(file)
        return
      }
      select(file)
      setMobileChoice('auto')
    },
    [flags.holdsServerCopy, select, state.job],
  )

  const begin = useCallback(() => {
    if (!state.file) return
    setMobileChoice('auto')
    void start(state.file, state.mode)
  }, [start, state.file, state.mode])

  const onMode = useCallback((m: Mode) => setMode(m), [setMode])

  return (
    <div className="flex h-full min-h-0 flex-col">
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
                    Loading the result view…
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
        title="Replace the current document?"
        body={`${
          state.job ? `Job ${state.job.job_id.slice(0, 12)}… is still on the server (result expires ${describeExpiry(state.job.result_expires_at)}). ` : ''
        }Choosing another file discards this tab's preview; the server copy is cleaned up by its TTL unless you released it.`}
        confirmLabel="Replace"
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
