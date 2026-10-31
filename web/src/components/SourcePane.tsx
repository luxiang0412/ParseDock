import { Preview } from '../preview'
import { humanBytes, previewKind } from '../features/playground/format'

const KIND_LABEL: Record<string, string> = {
  pdf: 'PDF',
  image: 'image',
  text: 'text',
  markdown: 'markdown',
  csv: 'csv',
  html: 'html',
  docx: 'docx',
  xlsx: 'xlsx',
  unsupported: 'no preview',
}

export function SourcePane({ file }: { file: File | null }) {
  return (
    <section className="flex h-full min-h-0 flex-col bg-white dark:bg-ink-950" aria-label="Original document">
      <div className="flex items-center gap-2 border-b border-ink-200 px-3 py-2 dark:border-ink-800">
        <span className="rounded bg-ink-100 px-1.5 py-0.5 font-mono text-[0.65rem] font-semibold uppercase tracking-wide text-ink-600 dark:bg-ink-900 dark:text-ink-300">
          original
        </span>
        <span className="truncate text-xs text-ink-500">{file ? file.name : 'No file yet'}</span>
        {file && (
          <span className="ml-auto shrink-0 font-mono text-[0.68rem] text-ink-400">
            {KIND_LABEL[previewKind(file.name)] ?? 'file'} · {humanBytes(file.size)}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-hidden">
        {file ? (
          <Preview file={file} />
        ) : (
          <div className="flex h-full items-center justify-center p-8 text-center text-sm text-ink-400">
            Choose a document to see the original next to its Markdown.
          </div>
        )}
      </div>
    </section>
  )
}
