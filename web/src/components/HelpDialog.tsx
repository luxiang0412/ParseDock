import { useEffect, useRef } from 'react'
import { Button } from './Button'

export function HelpDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (open && !el.open) el.showModal()
    if (!open && el.open) el.close()
  }, [open])

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => {
        if (e.target === ref.current) onClose()
      }}
      aria-labelledby="help-title"
      className="w-[min(36rem,92vw)] rounded-xl border border-ink-200 bg-white p-0 text-ink-900 shadow-xl backdrop:bg-ink-950/40 dark:border-ink-800 dark:bg-ink-900 dark:text-ink-100"
    >
      <div className="flex items-start justify-between gap-4 border-b border-ink-200 px-5 py-4 dark:border-ink-800">
        <div>
          <h2 id="help-title" className="text-sm font-semibold">
            How the playground works
          </h2>
          <p className="mt-0.5 text-xs text-ink-500">A developer workbench, not a document manager</p>
        </div>
        <Button size="sm" variant="ghost" onClick={onClose} aria-label="Close help">
          Close
        </Button>
      </div>
      <ol className="space-y-3 px-5 py-4 text-sm">
        <li>
          <span className="font-medium">1. Upload.</span> The file is sent to the ParseDock server;
          the left pane keeps a local copy in this tab, so the preview never re-downloads it.
        </li>
        <li>
          <span className="font-medium">2. Parse.</span> The mode is a hint: <em>Fast</em> prefers
          the fastest engine, <em>Accurate</em> prefers structure, and results still vary by format.
        </li>
        <li>
          <span className="font-medium">3. Compare.</span> The right pane shows the Markdown the API
          returned; switch to the source view to copy or download it.
        </li>
        <li>
          <span className="font-medium">4. Release.</span> Nothing is deleted automatically by this
          page. Use <em>Release server copy</em> when you are done; otherwise the server expires the
          result at the time shown in the status bar.
        </li>
      </ol>
      <p className="border-t border-ink-200 px-5 py-3 text-xs text-ink-500 dark:border-ink-800">
        Closing the tab does not release anything: the API key belongs to the deployment, so the
        result is cleaned up by its TTL.
      </p>
    </dialog>
  )
}
