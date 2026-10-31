import { useEffect, useRef, useState } from 'react'

/**
 * PDF rendering with pdf.js instead of the browser's built-in viewer: headless Chromium (used
 * by the E2E suite) has no PDF plugin, and an embedded viewer also gives no control over
 * scaling. Pages are rendered lazily as they scroll into view, and only the first
 * MAX_PAGES pages are offered so a 500-page annexe cannot freeze the tab.
 */
const MAX_PAGES = 60

type Doc = import('pdfjs-dist').PDFDocumentProxy

export function PdfPreview({ file }: { file: File }) {
  const hostRef = useRef<HTMLDivElement>(null)
  const [doc, setDoc] = useState<{ file: File; doc: Doc } | null>(null)
  const [error, setError] = useState<{ file: File } | null>(null)

  useEffect(() => {
    let cancelled = false
    let loaded: Doc | null = null
    void (async () => {
      try {
        const pdfjs = await import('pdfjs-dist')
        const worker = await import('pdfjs-dist/build/pdf.worker.min.mjs?url')
        pdfjs.GlobalWorkerOptions.workerSrc = worker.default
        const next = await pdfjs.getDocument({ data: await file.arrayBuffer() }).promise
        if (cancelled) {
          void next.destroy()
          return
        }
        loaded = next
        setDoc({ file, doc: next })
      } catch {
        if (!cancelled) setError({ file })
      }
    })()
    return () => {
      cancelled = true
      if (loaded) void loaded.destroy()
    }
  }, [file])

  if (error?.file === file) {
    return <p className="p-4 text-sm text-warn-500">This PDF could not be rendered here.</p>
  }
  const current = doc?.file === file ? doc.doc : null
  if (!current) return <p className="p-4 text-sm text-ink-500">Rendering PDF…</p>

  const pages = Math.min(current.numPages, MAX_PAGES)
  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-ink-200 px-3 py-1.5 text-[0.7rem] text-ink-500 dark:border-ink-800">
        <span className="font-mono">{current.numPages} pages</span>
        {current.numPages > MAX_PAGES && <span>preview limited to the first {MAX_PAGES} pages</span>}
      </div>
      <div ref={hostRef} className="min-h-0 flex-1 overflow-auto bg-ink-100/60 p-3 dark:bg-ink-900/40">
        <div className="mx-auto flex max-w-[62rem] flex-col gap-3">
          {Array.from({ length: pages }, (_, i) => (
            <PdfPage key={i} doc={current} pageNumber={i + 1} scrollRoot={hostRef} />
          ))}
        </div>
      </div>
    </div>
  )
}

function PdfPage({
  doc,
  pageNumber,
  scrollRoot,
}: {
  doc: Doc
  pageNumber: number
  scrollRoot: React.RefObject<HTMLDivElement | null>
}) {
  const ref = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [visible, setVisible] = useState(pageNumber <= 2)
  const [rendered, setRendered] = useState(false)

  // Render only what is near the viewport: a 60-page deck must not rasterise on open.
  useEffect(() => {
    const el = ref.current
    if (!el || visible) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) setVisible(true)
      },
      { root: scrollRoot.current ?? null, rootMargin: '600px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [scrollRoot, visible])

  useEffect(() => {
    if (!visible) return
    let cancelled = false
    void (async () => {
      const page = await doc.getPage(pageNumber)
      if (cancelled) return
      const canvas = canvasRef.current
      const host = ref.current
      if (!canvas || !host) return
      const base = page.getViewport({ scale: 1 })
      const width = Math.min(host.clientWidth || base.width, base.width * 2)
      const scale = width / base.width
      const viewport = page.getViewport({ scale: scale * Math.min(window.devicePixelRatio || 1, 2) })
      canvas.width = Math.floor(viewport.width)
      canvas.height = Math.floor(viewport.height)
      canvas.style.width = `${Math.floor(viewport.width / Math.min(window.devicePixelRatio || 1, 2))}px`
      const ctx = canvas.getContext('2d')
      if (!ctx) return
      await page.render({ canvasContext: ctx, viewport, canvas }).promise
      if (!cancelled) setRendered(true)
    })()
    return () => {
      cancelled = true
    }
  }, [doc, pageNumber, visible])

  return (
    <div ref={ref} className="relative rounded-md border border-ink-200 bg-white shadow-sm dark:border-ink-800">
      {!rendered && (
        <div className="grid h-40 place-items-center text-xs text-ink-400">page {pageNumber}</div>
      )}
      <canvas ref={canvasRef} className={`block h-auto w-full ${rendered ? '' : 'hidden'}`} role="img" aria-label={`Page ${pageNumber}`} />
      <span className="pointer-events-none absolute bottom-2 right-2 rounded bg-ink-950/60 px-1.5 py-0.5 font-mono text-[0.65rem] text-white">
        {pageNumber}
      </span>
    </div>
  )
}
