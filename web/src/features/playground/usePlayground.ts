import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { api, type Job, type Mode } from '../../api/client'
import { ApiError } from '../../api/errors'
import { holdsServerCopy, initialState, phaseOf, reducer } from './phase'
import { useJobEvents } from './useJobEvents'

const ACTIVE = new Set(['uploading', 'queued', 'running'])

function isTerminal(job: Job): boolean {
  return (
    job.status === 'completed' ||
    job.status === 'failed' ||
    job.status === 'cancelled' ||
    job.retention_status === 'released' ||
    job.retention_status === 'expired'
  )
}

/**
 * The playground's whole lifecycle: upload → watch → result → explicit release.
 *
 * Deliberate choices:
 *  - the backend job status is the source of truth; `phase` only adds the two local steps
 *    (uploading, releasing);
 *  - nothing here ACKs. `release()` is only reachable from the user's button;
 *  - a job id captured at request time guards every async write, so switching files cannot be
 *    overwritten by a late response from the previous job.
 */
export function usePlayground() {
  const [state, dispatch] = useReducer(reducer, initialState)
  const abortRef = useRef<AbortController | null>(null)
  const pollRef = useRef<number | null>(null)
  const pollDelay = useRef(1000)
  const jobIdRef = useRef<string | null>(null)
  // The SSE hook needs the id during render; refs must not be read there.
  const [activeJobId, setActiveJobId] = useState<string | null>(null)

  const stopWatching = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearTimeout(pollRef.current)
      pollRef.current = null
    }
    pollDelay.current = 1000
  }, [])

  const applyJob = useCallback((job: Job) => {
    if (job.job_id !== jobIdRef.current) return
    dispatch({ type: 'job', job })
  }, [])

  const loadResult = useCallback(async (job: Job) => {
    if (job.status !== 'completed') return
    if (job.retention_status !== 'active' && job.retention_status !== 'release_pending') {
      // Already released/expired server-side: keep whatever was downloaded earlier.
      return
    }
    try {
      const { markdown, resultVersion } = await api.getMarkdown(job.job_id)
      if (job.job_id !== jobIdRef.current) return
      dispatch({ type: 'result', markdown, resultVersion: resultVersion ?? job.result_version ?? null })
      // Metadata is optional and capped: a big workbook's IR is deliberately skipped.
      const ir = await api.getIR(job.job_id)
      if (job.job_id !== jobIdRef.current) return
      dispatch({
        type: 'ir',
        ir: ir
          ? {
              parseMs: ir.metrics?.parse_ms,
              tables: ir.tables?.length,
              device: ir.routing?.actual_device,
              warningCodes: (ir.warnings ?? [])
                .map((w) => w.code)
                .filter((c): c is string => typeof c === 'string' && c.length > 0),
            }
          : null,
      })
    } catch (err) {
      if (job.job_id !== jobIdRef.current) return
      if (err instanceof ApiError && (err.status === 410 || err.status === 404)) {
        // Result vanished between status and download (released/expired by TTL or another tab).
        const fresh = await api.getJob(job.job_id).catch(() => null)
        if (fresh && job.job_id === jobIdRef.current) dispatch({ type: 'job', job: fresh })
        return
      }
      dispatch({
        type: 'error',
        code: 'internal_error',
        message: err instanceof Error ? err.message : 'result download failed',
      })
    }
  }, [])

  const refresh = useCallback(
    async (jobId: string) => {
      try {
        const job = await api.getJob(jobId)
        if (jobId !== jobIdRef.current) return
        applyJob(job)
        if (job.status === 'completed') await loadResult(job)
        if (isTerminal(job)) stopWatching()
      } catch (err) {
        if (jobId !== jobIdRef.current) return
        if (err instanceof ApiError && err.status === 429) {
          // Polling shares the owner budget: back off instead of hammering.
          pollDelay.current = Math.min(pollDelay.current * 2, 8000)
        }
      }
    },
    [applyJob, loadResult, stopWatching],
  )

  const schedulePoll = useCallback(
    (jobId: string) => {
      if (pollRef.current !== null) return
      const tick = async () => {
        if (jobId !== jobIdRef.current) return
        await refresh(jobId)
        if (jobId !== jobIdRef.current) return
        pollRef.current = window.setTimeout(() => void tick(), pollDelay.current)
      }
      pollRef.current = window.setTimeout(() => void tick(), pollDelay.current)
    },
    [refresh],
  )

  const fallBackToPolling = useCallback(() => {
    const id = jobIdRef.current
    if (id) schedulePoll(id)
  }, [schedulePoll])

  // SSE is primary; polling is the safety net (proxy without flushing, tab throttling, 503s).
  useJobEvents(activeJobId, Boolean(state.job && !isTerminal(state.job)), {
    onEvent: ({ type, data }) => {
      if (typeof data?.progress_percent === 'number') {
        dispatch({
          type: 'progress',
          percent: data.progress_percent,
          stage: typeof data.progress_stage === 'string' ? data.progress_stage : undefined,
        })
      }
      if (type === 'completed' || type === 'failed' || type === 'cancelled') {
        const id = jobIdRef.current
        if (id) void refresh(id)
      }
    },
    onStreamEnd: () => {
      const id = jobIdRef.current
      if (id) void refresh(id)
    },
    onFallback: fallBackToPolling,
  })

  useEffect(() => () => stopWatching(), [stopWatching])

  const start = useCallback(
    async (file: File, mode: Mode) => {
      abortRef.current?.abort()
      stopWatching()
      const controller = new AbortController()
      abortRef.current = controller
      jobIdRef.current = null
      setActiveJobId(null)
      dispatch({ type: 'start' })
      try {
        const job = await api.createJob({ file, mode, signal: controller.signal })
        jobIdRef.current = job.job_id
        setActiveJobId(job.job_id)
        dispatch({ type: 'uploaded', job })
        if (isTerminal(job)) {
          if (job.status === 'completed') await loadResult(job)
        } else {
          schedulePoll(job.job_id)
          // One immediate refresh so a very fast job (mock engine) shows progress at once.
          void refresh(job.job_id)
        }
      } catch (err) {
        if (controller.signal.aborted) return
        if (err instanceof ApiError) {
          dispatch({ type: 'error', code: err.code, message: err.message })
        } else {
          dispatch({ type: 'error', message: err instanceof Error ? err.message : 'upload failed' })
        }
      }
    },
    [loadResult, refresh, schedulePoll, stopWatching],
  )

  const cancel = useCallback(async () => {
    const job = state.job
    if (!job || !ACTIVE.has(state.phase)) return
    try {
      const updated = await api.cancel(job.job_id)
      applyJob(updated)
      void refresh(job.job_id)
    } catch (err) {
      dispatch({
        type: 'error',
        code: err instanceof ApiError ? err.code : 'cancelFailed',
        message: err instanceof Error ? err.message : 'cancel failed',
      })
    }
  }, [applyJob, refresh, state.job, state.phase])

  /** Only ever called from the explicit "release" button. */
  const release = useCallback(async () => {
    const job = state.job
    const version = state.resultVersion ?? job?.result_version ?? null
    if (!job || !version) {
      dispatch({ type: 'error', code: 'conflict', message: 'missing result_version' })
      return
    }
    dispatch({ type: 'releasing' })
    try {
      await api.ack(job.job_id, version)
      const refreshed = await api.getJob(job.job_id)
      applyJob(refreshed)
      // Deletion is asynchronous: poll until the tombstone appears (bounded).
      const deadline = Date.now() + 30_000
      const wait = async () => {
        if (Date.now() > deadline) return
        const now = await api.getJob(job.job_id).catch(() => null)
        if (now) applyJob(now)
        if (now && now.retention_status !== 'active' && now.retention_status !== 'release_pending') return
        window.setTimeout(() => void wait(), 800)
      }
      void wait()
    } catch (err) {
      dispatch({
        type: 'error',
        code: err instanceof ApiError ? err.code : 'releaseFailed',
        message: err instanceof Error ? err.message : 'release failed',
      })
    }
  }, [applyJob, state.job, state.resultVersion])

  const reset = useCallback(() => {
    abortRef.current?.abort()
    stopWatching()
    jobIdRef.current = null
    setActiveJobId(null)
    dispatch({ type: 'reset' })
  }, [stopWatching])

  const select = useCallback((file: File) => {
    abortRef.current?.abort()
    stopWatching()
    jobIdRef.current = null
    setActiveJobId(null)
    dispatch({ type: 'select', file })
  }, [stopWatching])

  const setMode = useCallback((mode: Mode) => dispatch({ type: 'mode', mode }), [])

  const flags = useMemo(
    () => ({
      canStart: Boolean(state.file) && !ACTIVE.has(state.phase) && state.phase !== 'releasing',
      canCancel: state.phase === 'queued' || state.phase === 'running',
      canRelease: state.phase === 'completed' && Boolean(state.resultVersion ?? state.job?.result_version),
      holdsServerCopy: holdsServerCopy(state.job),
      isWorking: state.phase === 'uploading' || state.phase === 'queued' || state.phase === 'running',
      hasResult: Boolean(state.markdown),
    }),
    [state],
  )

  return { state, flags, start, cancel, release, reset, select, setMode, phaseOf }
}
