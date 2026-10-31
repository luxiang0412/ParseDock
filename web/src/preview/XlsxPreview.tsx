import { useEffect, useState } from 'react'
import { useI18n } from '../i18n'
import { humanBytes } from '../features/playground/format'

export const XLSX_PREVIEW_LIMIT = 16 << 20
const MAX_SHEETS = 5
const MAX_ROWS = 200
const MAX_COLS = 30

interface Sheet {
  name: string
  rows: string[][]
  truncated: boolean
}

type Loaded = { file: File; sheets: Sheet[] } | 'error'

export function XlsxPreview({ file }: { file: File }) {
  const { t } = useI18n()
  const tooLarge = file.size > XLSX_PREVIEW_LIMIT
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [active, setActive] = useState<{ file: File; index: number } | null>(null)

  useEffect(() => {
    if (tooLarge) return
    let cancelled = false
    // The Excel reader is ~1 MB: load it only when a workbook is actually previewed.
    void import('exceljs')
      .then(async ({ Workbook }) => {
        const wb = new Workbook()
        await wb.xlsx.load(await file.arrayBuffer())
        if (cancelled) return
        const sheets: Sheet[] = []
        for (const ws of wb.worksheets) {
          if (sheets.length >= MAX_SHEETS) break
          const rows: string[][] = []
          const truncated = ws.rowCount > MAX_ROWS || ws.columnCount > MAX_COLS
          ws.eachRow({ includeEmpty: false }, (row, rowNumber) => {
            if (rows.length >= MAX_ROWS) return
            const cells: string[] = []
            for (let c = 1; c <= Math.min(ws.columnCount, MAX_COLS); c += 1) {
              cells.push(cellText(row.getCell(c).value))
            }
            rows.push([String(rowNumber), ...cells])
          })
          sheets.push({ name: ws.name, rows, truncated })
        }
        setLoaded({ file, sheets })
      })
      .catch(() => {
        if (!cancelled) setLoaded('error')
      })
    return () => {
      cancelled = true
    }
  }, [file, tooLarge])

  if (tooLarge) {
    return (
      <p className="p-4 text-sm text-ink-500">
        {t('preview.xlsxTooLarge', {
          size: humanBytes(file.size),
          limit: humanBytes(XLSX_PREVIEW_LIMIT),
        })}
      </p>
    )
  }
  if (loaded === 'error') {
    return <p className="p-4 text-sm text-warn-500">{t('preview.xlsxError')}</p>
  }
  const current = loaded && loaded.file === file ? loaded.sheets : null
  if (!current) return <p className="p-4 text-sm text-ink-500">{t('preview.xlsxReading')}</p>

  const index = active?.file === file ? active.index : 0
  const sheet = current[index] ?? current[0]
  if (!sheet) return <p className="p-4 text-sm text-ink-500">{t('preview.xlsxNoSheets')}</p>

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-1 border-b border-ink-200 px-2 py-1.5 dark:border-ink-800">
        {current.map((s, i) => (
          <button
            key={s.name}
            type="button"
            onClick={() => setActive({ file, index: i })}
            aria-pressed={i === index}
            className={`rounded px-2 py-1 text-xs font-medium ${
              i === index
                ? 'bg-accent-600 text-white'
                : 'text-ink-600 hover:bg-ink-100 dark:text-ink-300 dark:hover:bg-ink-900'
            }`}
          >
            {s.name}
          </button>
        ))}
        {current.length === MAX_SHEETS && (
          <span className="ml-2 text-xs text-ink-500">
            {t('preview.xlsxFirstSheets', { count: MAX_SHEETS })}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-2">
        <table className="w-max border-collapse text-[0.78rem]">
          <tbody>
            {sheet.rows.map((row, r) => (
              <tr key={r} className={r === 0 ? 'font-semibold' : undefined}>
                {row.map((cell, c) => (
                  <td
                    key={c}
                    className={`border border-ink-200 px-1.5 py-1 align-top dark:border-ink-800 ${
                      c === 0 ? 'bg-ink-100 text-right text-ink-500 dark:bg-ink-900' : ''
                    }`}
                  >
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
        {sheet.truncated && (
          <p className="mt-2 text-xs text-ink-500">
            {t('preview.csvLimit', { rows: MAX_ROWS, cols: MAX_COLS })}
          </p>
        )}
      </div>
    </div>
  )
}

function cellText(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (value instanceof Date) return value.toISOString().slice(0, 10)
  if (typeof value === 'object') {
    const v = value as {
      formula?: string
      result?: unknown
      text?: unknown
      richText?: { text: string }[]
    }
    if (Array.isArray(v.richText)) return v.richText.map((r) => r.text).join('')
    if (typeof v.formula === 'string') return `=${v.formula}`
    if (typeof v.text === 'string') return v.text
    if (v.result !== undefined) return cellText(v.result)
  }
  return ''
}
