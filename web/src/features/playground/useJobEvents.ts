import { useEffect, useRef } from 'react'

export interface JobEvent {
  type: string
  data: Record<string, unknown> | null
}

export interface EventHandlers {
  onEvent: (event: JobEvent) => void
  onStreamEnd: (reason?: string) => void
  /** The stream could not be established or died; the caller should fall back to polling. */
  onFallback: () => void
}

const TERMINAL_EVENTS = new Set(['completed', 'failed', 'cancelled'])

/**
 * Server-sent events for one job. `EventSource` cannot send an Authorization header, which is
 * exactly why the playground is served behind a same-origin proxy that injects the key.
 *
 * The hook is deliberately dumb: it forwards events and closes the stream when it cannot work.
 * Ordering and state live in the reducer, so a stale stream cannot overwrite a newer job.
 */
export function useJobEvents(jobId: string | null, enabled: boolean, handlers: EventHandlers) {
  const handlersRef = useRef(handlers)
  useEffect(() => {
    handlersRef.current = handlers
  })

  useEffect(() => {
    if (!jobId || !enabled || typeof EventSource === 'undefined') {
      if (jobId && enabled && typeof EventSource === 'undefined') handlersRef.current.onFallback()
      return
    }
    const source = new EventSource(`/v1/jobs/${encodeURIComponent(jobId)}/events?scope=processing`)
    let closed = false

    const dispatch = (type: string) => (ev: MessageEvent<string>) => {
      let data: Record<string, unknown> | null = null
      try {
        data = JSON.parse(ev.data) as Record<string, unknown>
      } catch {
        data = null
      }
      handlersRef.current.onEvent({ type, data })
      if (type === 'stream_end') {
        closed = true
        source.close()
        handlersRef.current.onStreamEnd(typeof data?.reason === 'string' ? data.reason : undefined)
      }
      if (TERMINAL_EVENTS.has(type)) {
        // Keep the stream open: the backend sends stream_end right after, and progress events
        // may still be queued. The reducer ignores anything for a terminal job.
      }
    }

    // Named events the backend emits; anything else arrives as `message`.
    const named = ['snapshot', 'stream_end', 'cursor_reset', 'created', 'claimed', 'progress', 'completed', 'failed', 'cancelled', 'lease_expired', 'retry_scheduled', 'release_pending']
    const listeners = named.map((n) => {
      const fn = dispatch(n)
      source.addEventListener(n, fn as EventListener)
      return [n, fn] as const
    })
    source.onmessage = dispatch('message')
    source.onerror = () => {
      if (closed) return
      closed = true
      source.close()
      handlersRef.current.onFallback()
    }

    return () => {
      closed = true
      for (const [name, fn] of listeners) source.removeEventListener(name, fn as EventListener)
      source.close()
    }
  }, [jobId, enabled])
}
