import Papa from 'papaparse'
import { useEffect, useMemo, useState } from 'react'
import { decodeTextFile } from './text'
import { useI18n } from '../i18n'

const MAX_ROWS = 200
const MAX_COLS = 30

export function CsvPreview({ file }: { file: File }) {
  const [text, setText] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const { t } = useI18n()

  useEffect(() => {
    let cancelled = false
    decodeTextFile(file, 2 << 20)
      .then((d) => !cancelled && setText(d.text))
      .catch((e: unknown) => !cancelled && setError(String(e)))
    return () => {
      cancelled = true
    }
  }, [file])

  const parsed = useMemo(() => {
    if (text === null) return null
    const out = Papa.parse<string[]>(text, { preview: MAX_ROWS + 1, skipEmptyLines: true })
    return out.data.map((row) => row.slice(0, MAX_COLS))
  }, [text])

  if (error)
    return <p className="p-4 text-sm text-danger-500">{t('preview.errorReading', { error })}</p>
  if (!parsed) return <p className="p-4 text-sm text-ink-500">{t('preview.reading')}</p>
  const [header, ...rows] = parsed

  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-ink-200 px-3 py-2 text-xs text-ink-500 dark:border-ink-800">
        {t('preview.csvLimit', { rows: MAX_ROWS, cols: MAX_COLS })}
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-3">
        <table className="w-max border-collapse text-[0.8rem]">
          {header && (
            <thead>
              <tr>
                {header.map((cell, i) => (
                  <th
                    key={i}
                    className="sticky top-0 border border-ink-200 bg-ink-100 px-2 py-1 text-left font-semibold dark:border-ink-800 dark:bg-ink-900"
                  >
                    {cell}
                  </th>
                ))}
              </tr>
            </thead>
          )}
          <tbody>
            {rows.map((row, r) => (
              <tr key={r} className="odd:bg-ink-50/60 dark:odd:bg-ink-900/40">
                {row.map((cell, c) => (
                  <td key={c} className="border border-ink-200 px-2 py-1 dark:border-ink-800">
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
