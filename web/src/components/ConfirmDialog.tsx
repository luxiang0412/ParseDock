import { useEffect, useRef } from 'react'
import { Button } from './Button'

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  cancelLabel = 'Cancel',
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: string
  body: string
  confirmLabel: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
}) {
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
      onClose={onCancel}
      aria-labelledby="confirm-title"
      className="w-[min(30rem,92vw)] rounded-xl border border-ink-200 bg-white p-0 shadow-xl backdrop:bg-ink-950/40 dark:border-ink-800 dark:bg-ink-900"
    >
      <div className="px-5 py-4">
        <h2 id="confirm-title" className="text-sm font-semibold">
          {title}
        </h2>
        <p className="mt-2 text-sm text-ink-600 dark:text-ink-300">{body}</p>
      </div>
      <div className="flex justify-end gap-2 border-t border-ink-200 px-5 py-3 dark:border-ink-800">
        <Button size="sm" variant="ghost" onClick={onCancel}>
          {cancelLabel}
        </Button>
        <Button size="sm" variant="primary" onClick={onConfirm} autoFocus>
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  )
}
