import { useEffect, useRef, useState } from 'react'
import { stripActiveContent } from './sanitizeDOM'
import { useI18n } from '../i18n'
import { humanBytes } from '../features/playground/format'

/** Above this size a DOCX is reported as too large to preview rather than parsed in JS. */
export const DOCX_PREVIEW_LIMIT = 20 << 20

type Status = { file: File; state: 'ready' | 'error' }

export function DocxPreview({ file }: { file: File }) {
  const hostRef = useRef<HTMLDivElement>(null)
  const styleRef = useRef<HTMLDivElement>(null)
  const [status, setStatus] = useState<Status | null>(null)
  const { t } = useI18n()
  const tooLarge = file.size > DOCX_PREVIEW_LIMIT

  useEffect(() => {
    if (tooLarge) return
    let cancelled = false
    // The renderer is ~200 KB and only needed once a result exists: load it on demand.
    void import('docx-preview')
      .then(async ({ renderAsync }) => {
        if (cancelled || !hostRef.current) return
        hostRef.current.replaceChildren()
        const container = document.createElement('div')
        hostRef.current.append(container)
        await renderAsync(await file.arrayBuffer(), container, styleRef.current ?? undefined, {
          className: 'docx',
          inWrapper: true,
          breakPages: true,
          ignoreLastRenderedPageBreak: true,
          experimental: true,
        })
        if (cancelled) return
        stripActiveContent(container)
        setStatus({ file, state: 'ready' })
      })
      .catch(() => {
        if (!cancelled) setStatus({ file, state: 'error' })
      })
    return () => {
      cancelled = true
    }
  }, [file, tooLarge])

  if (tooLarge) {
    return (
      <p className="p-4 text-sm text-ink-500">
        {t('preview.docxTooLarge', {
          size: humanBytes(file.size),
          limit: humanBytes(DOCX_PREVIEW_LIMIT),
        })}
      </p>
    )
  }
  const current = status?.file === file ? status.state : null
  if (current === 'error') {
    return <p className="p-4 text-sm text-warn-500">{t('preview.docxError')}</p>
  }

  return (
    <div className="relative h-full">
      <div ref={styleRef} className="hidden" />
      {current !== 'ready' && (
        <p className="absolute inset-x-0 top-0 p-3 text-xs text-ink-500">
          {t('preview.docxRendering')}
        </p>
      )}
      <div className="docx-wrap h-full overflow-auto p-4" ref={hostRef} />
    </div>
  )
}
