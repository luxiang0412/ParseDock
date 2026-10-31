import { Preview } from '../preview'
import { humanBytes, previewKind } from '../features/playground/format'
import { useI18n } from '../i18n'
import type { MessageKey } from '../i18n/messages'

const KIND_KEY: Record<string, MessageKey> = {
  pdf: 'source.kind.pdf',
  image: 'source.kind.image',
  text: 'source.kind.text',
  markdown: 'source.kind.markdown',
  csv: 'source.kind.csv',
  html: 'source.kind.html',
  docx: 'source.kind.docx',
  xlsx: 'source.kind.xlsx',
  unsupported: 'source.kind.unsupported',
}

export function SourcePane({ file }: { file: File | null }) {
  const { t } = useI18n()
  const kind = file ? previewKind(file.name) : null
  return (
    <section
      data-testid="source-pane"
      className="flex h-full min-h-0 flex-col bg-white dark:bg-ink-950"
      aria-label={t('panes.original')}
    >
      <div className="flex items-center gap-2 border-b border-ink-200 px-3 py-2 dark:border-ink-800">
        <span className="rounded bg-ink-100 px-1.5 py-0.5 font-mono text-[0.65rem] font-semibold uppercase tracking-wide text-ink-600 dark:bg-ink-900 dark:text-ink-300">
          {t('source.label')}
        </span>
        <span className="truncate text-xs text-ink-500">{file ? file.name : t('source.noFile')}</span>
        {file && kind && (
          <span className="ml-auto shrink-0 font-mono text-[0.68rem] text-ink-400">
            {t(KIND_KEY[kind] ?? 'source.kind.file')} · {humanBytes(file.size)}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-hidden">
        {file ? (
          <Preview file={file} />
        ) : (
          <div className="flex h-full items-center justify-center p-8 text-center text-sm text-ink-400">
            {t('source.empty')}
          </div>
        )}
      </div>
    </section>
  )
}
