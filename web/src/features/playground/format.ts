export type PreviewKind =
  | 'pdf'
  | 'image'
  | 'text'
  | 'markdown'
  | 'csv'
  | 'html'
  | 'docx'
  | 'xlsx'
  | 'unsupported'

const IMAGE = new Set(['png', 'jpg', 'jpeg', 'webp'])
const TEXT = new Set(['txt', 'log', 'tsv'])
const MARKDOWN = new Set(['md', 'markdown'])
const CSV = new Set(['csv'])
const HTML = new Set(['html', 'htm'])

export function extensionOf(name: string): string {
  const i = name.lastIndexOf('.')
  return i < 0 ? '' : name.slice(i + 1).toLowerCase()
}

export function previewKind(name: string): PreviewKind {
  const ext = extensionOf(name)
  if (ext === 'pdf') return 'pdf'
  if (IMAGE.has(ext)) return 'image'
  if (MARKDOWN.has(ext)) return 'markdown'
  if (CSV.has(ext)) return 'csv'
  if (TEXT.has(ext)) return 'text'
  if (HTML.has(ext)) return 'html'
  if (ext === 'docx') return 'docx'
  if (ext === 'xlsx' || ext === 'xlsm') return 'xlsx'
  return 'unsupported'
}

/** Formats the backend accepts (mirrors internal/storage/sniff.go). */
export const ACCEPTED_EXTENSIONS = [
  '.pdf',
  '.docx',
  '.xlsx',
  '.xlsm',
  '.pptx',
  '.csv',
  '.tsv',
  '.txt',
  '.md',
  '.markdown',
  '.png',
  '.jpg',
  '.jpeg',
  '.webp',
  '.html',
  '.htm',
  '.doc',
  '.dot',
  '.xls',
  '.xlt',
  '.ppt',
  '.pot',
] as const

export function isAccepted(file: { name: string }): boolean {
  return ACCEPTED_EXTENSIONS.includes(('.' + extensionOf(file.name)) as (typeof ACCEPTED_EXTENSIONS)[number])
}

export function humanBytes(n: number | undefined): string {
  if (n === undefined || Number.isNaN(n)) return '–'
  if (n < 1024) return `${n} B`
  const units = ['KiB', 'MiB', 'GiB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i += 1
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function humanMs(ms: number | undefined): string {
  if (ms === undefined || Number.isNaN(ms)) return '–'
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

/** "in 42 min" / "expired 3 min ago" for the result TTL. */
export function describeExpiry(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return 'no result window'
  const at = Date.parse(iso)
  if (Number.isNaN(at)) return 'unknown'
  const diff = at - now
  const abs = Math.abs(diff)
  const mins = Math.round(abs / 60_000)
  const text =
    abs < 90_000
      ? `${Math.max(1, Math.round(abs / 1000))} s`
      : mins < 90
        ? `${mins} min`
        : `${Math.round(abs / 3_600_000)} h`
  return diff >= 0 ? `in ${text}` : `${text} ago`
}

export function formatClock(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000))
  const m = Math.floor(total / 60)
  const s = total % 60
  return m > 0 ? `${m}:${String(s).padStart(2, '0')}` : `${s}s`
}
