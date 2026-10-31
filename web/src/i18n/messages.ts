import { zhCN } from './zh'

/**
 * All user-visible strings, in English and Simplified Chinese.
 *
 * `Messages` is derived from the English dictionary, so a missing zh-CN key is a type error
 * (and `src/__tests__/i18n.test.ts` asserts both dictionaries have the same keys at runtime).
 * Placeholders use `{name}` and are filled by `t(key, vars)`.
 */
export const en = {
  'app.name': 'ParseDock',
  'app.product': 'Playground',
  'app.tagline': 'Upload a document, compare the original with the parsed Markdown',
  'app.help': 'How it works',
  'app.loadingResult': 'Loading the result view…',
  'app.dismiss': 'Dismiss error',

  'health.checking': 'checking API',
  'health.online': 'API online',
  'health.offline': 'API unreachable',
  'health.titleOnline': 'The API replica answered /healthz',
  'health.titleOffline': 'No answer from /healthz',
  'health.titleChecking': 'Contacting the API',

  'drop.title': 'Drop a document here, or',
  'drop.browse': 'browse',
  'drop.formats':
    'PDF, Word, Excel, PowerPoint, CSV/TSV, text/Markdown, images and HTML. The file is uploaded to the ParseDock server for parsing; the preview on the left stays local.',
  'drop.privacy':
    'No file is retained by the browser beyond this tab · server results follow the API TTL until you release them',
  'drop.rejected': '{name} is not an accepted type. Accepted: {list}',
  'drop.overlayTitle': 'Drop to parse',
  'drop.overlayBody': 'Release the file anywhere on this page.',

  'mode.label': 'Mode',
  'mode.group': 'Parse mode',
  'mode.auto': 'Auto',
  'mode.fast': 'Fast',
  'mode.accurate': 'Accurate',
  'mode.auto.hint': 'Fast engine, escalating scans when OCR quality is low.',
  'mode.fast.hint': 'Fastest engine. Best for text-layer PDFs and Office files.',
  'mode.accurate.hint':
    'Structure/source-preferring engine. Slower; per-format results still vary.',
  'mode.auto.note': 'fast engine, escalating scans when OCR quality is low',
  'mode.fast.note': 'fastest engine; best for text-layer PDFs and Office files',
  'mode.accurate.note':
    'prefers structure and provenance; slower, and results still vary by format',

  'intake.newFile': 'New file',
  'intake.change': 'Change',
  'intake.clear': 'Clear',
  'intake.parse': 'Parse',
  'intake.parsing': 'Parsing…',
  'intake.chooseAnother': 'Choose another document',
  'intake.unknownType': 'unknown type',

  'status.idle': 'Ready',
  'status.selected': 'Ready to parse',
  'status.uploading': 'Uploading',
  'status.queued': 'Queued',
  'status.running': 'Parsing',
  'status.completed': 'Completed',
  'status.failed': 'Failed',
  'status.cancelled': 'Cancelled',
  'status.releasing': 'Releasing server copy',
  'status.released': 'Server copy released',
  'status.expired': 'Result expired',
  'status.error': 'Error',
  'status.expiresIn': 'server copy expires {when}',
  'status.deleting': 'deletion in progress on the server…',
  'status.releasedNote': 'downloaded Markdown stays readable here; new downloads return 410',
  'status.cancel': 'Cancel job',
  'status.release': 'Release server copy',
  'status.releasingAction': 'Releasing…',
  'status.progress': 'Parse progress',
  'status.elapsedTitle': 'Time since the job was created',
  'status.uploadingStage': 'sending file',

  'meta.engine': 'engine',
  'meta.device': 'device',
  'meta.pages': 'pages',
  'meta.chars': 'chars',
  'meta.tables': 'tables',
  'meta.parse': 'parse',
  'meta.warnings': 'warnings',
  'meta.expires': 'expires',
  'meta.version': 'version',

  'md.preview': 'Preview',
  'md.source': 'Markdown source',
  'md.view': 'Markdown view',
  'md.chars': '{count} chars',
  'md.copy': 'Copy',
  'md.copied': 'Copied',
  'md.copyFailed': 'Copy failed',
  'md.download': 'Download .md',
  'md.emptyTitle': 'The parsed Markdown will appear here',
  'md.emptyBody': 'Parse the document to compare it with the original on the left.',
  'md.loading': 'Loading the result…',
  'md.release': 'Release server copy',
  'md.remoteImage': 'Remote images are not loaded',
  'md.image': 'image',
  'md.table': 'table',

  'source.label': 'original',
  'source.noFile': 'No file yet',
  'source.empty': 'Choose a document to see the original next to its Markdown.',
  'source.kind.pdf': 'PDF',
  'source.kind.image': 'image',
  'source.kind.text': 'text',
  'source.kind.markdown': 'markdown',
  'source.kind.csv': 'csv',
  'source.kind.html': 'html',
  'source.kind.docx': 'docx',
  'source.kind.xlsx': 'xlsx',
  'source.kind.unsupported': 'no preview',
  'source.kind.file': 'file',

  'panes.original': 'Original',
  'panes.markdown': 'Markdown',
  'panes.workspace': 'Workspace',
  'panes.resize': 'Resize panes',

  'help.title': 'How the playground works',
  'help.subtitle': 'A developer workbench, not a document manager',
  'help.close': 'Close',
  'help.step1.bold': '1. Upload.',
  'help.step1':
    ' The file is sent to the ParseDock server; the left pane keeps a local copy in this tab, so the preview never re-downloads it.',
  'help.step2.bold': '2. Parse.',
  'help.step2a': ' The mode is a hint: ',
  'help.step2.fast': 'Fast',
  'help.step2.mid': ' prefers the fastest engine, ',
  'help.step2.accurate': 'Accurate',
  'help.step2.end': ' prefers structure, and results still vary by format.',
  'help.step3.bold': '3. Compare.',
  'help.step3':
    ' The right pane shows the Markdown the API returned; switch to the source view to copy or download it.',
  'help.step4.bold': '4. Release.',
  'help.step4':
    ' Nothing is deleted automatically by this page. Use “Release server copy” when you are done; otherwise the server expires the result at the time shown in the status bar.',
  'help.footer':
    'Closing the tab does not release anything: the API key belongs to the deployment, so the result is cleaned up by its TTL.',

  'confirm.replaceTitle': 'Replace the current document?',
  'confirm.replaceBody':
    'Choosing another file discards this tab’s preview; the server copy is cleaned up by its TTL unless you released it.',
  'confirm.replaceBodyHeld':
    'Job {job} is still on the server (result expires {when}). Choosing another file discards this tab’s preview; the server copy is cleaned up by its TTL unless you released it.',
  'confirm.replace': 'Replace',
  'confirm.cancel': 'Cancel',

  'preview.unsupported': 'No preview is available for this format.',
  'preview.pptx':
    'PowerPoint decks have no safe in-browser renderer in this version. The parsed Markdown is shown on the right.',
  'preview.doc':
    'Legacy Word (.doc) is a binary format with no browser renderer. The parsed Markdown is shown on the right.',
  'preview.xls':
    'Legacy Excel (.xls) is a binary format with no browser renderer. The parsed Markdown is shown on the right.',
  'preview.ppt':
    'Legacy PowerPoint (.ppt) has no browser renderer. The parsed Markdown is shown on the right.',
  'preview.errorReading': 'Could not read the file: {error}',
  'preview.reading': 'Reading file…',
  'preview.encodingHint': 'some characters could not be decoded',
  'preview.truncated': 'preview truncated to the first {size}',
  'preview.csvLimit': 'preview limited to the first {rows} rows and {cols} columns',
  'preview.docxRendering': 'Rendering document…',
  'preview.docxError': 'This DOCX could not be rendered here.',
  'preview.docxTooLarge':
    'This DOCX is {size}; previews are limited to {limit}. The parsed Markdown is still available.',
  'preview.xlsxReading': 'Reading workbook…',
  'preview.xlsxError': 'This workbook could not be read here.',
  'preview.xlsxTooLarge':
    'This workbook is {size}; previews are limited to {limit}. The parsed Markdown (tables) is still available.',
  'preview.xlsxNoSheets': 'The workbook has no visible sheets.',
  'preview.xlsxFirstSheets': 'first {count} sheets',
  'preview.pdfReading': 'Rendering PDF…',
  'preview.pdfError': 'This PDF could not be rendered here.',
  'preview.pdfPages': '{count} pages',
  'preview.pdfLimit': 'preview limited to the first {count} pages',
  'preview.pdfOpenNewTab': 'Open the PDF in a new tab',
  'preview.pdfNoPlugin': 'This browser cannot display PDFs inline.',

  'expiry.none': 'no result window',
  'expiry.unknown': 'unknown',
  'expiry.in': 'in {value}',
  'expiry.ago': '{value} ago',
  'expiry.seconds': '{n} s',
  'expiry.minutes': '{n} min',
  'expiry.hours': '{n} h',
  'unit.ms': '{n} ms',
  'unit.seconds': '{n} s',
  'unit.minutes': '{n} min',

  'error.invalid_request': 'The request was rejected as invalid. Check the file and try again.',
  'error.unsupported_type': 'This file type is not accepted by this deployment.',
  'error.malformed_file': 'The file looks damaged or is not what its extension claims.',
  'error.file_too_large': 'The file is larger than the upload limit of this deployment.',
  'error.encrypted_file': 'The document is password protected, so it cannot be parsed.',
  'error.page_limit_exceeded': 'The document has more pages than this deployment allows.',
  'error.suspicious_compression':
    'The archive expands suspiciously far; it was rejected for safety.',
  'error.expanded_too_large': 'The document expands beyond the parser limits (large spreadsheets).',
  'error.quota_exceeded':
    'This owner has reached a quota (concurrent jobs, daily jobs or storage).',
  'error.insufficient_storage': 'The server is low on disk space. Try again later.',
  'error.engine_unavailable': 'No parser engine is available for this document right now.',
  'error.gpu_unavailable': 'A GPU worker was requested but none is available with enough VRAM.',
  'error.timeout': 'Parsing took longer than the limit for this document.',
  'error.cancelled': 'The job was cancelled.',
  'error.internal_error': 'The server hit an internal error while parsing.',
  'error.lease_lost': 'The worker processing this job was lost; the job was retried.',
  'error.not_found': 'That job does not exist for this API key.',
  'error.gone': 'The result was released or expired and can no longer be downloaded.',
  'error.conflict': 'The job is not in a state where that action is allowed.',
  'error.unauthorized': 'The API key is missing or invalid.',
  'error.insufficient_scope': 'This API key does not grant the required scope.',
  'error.rate_limited': 'Too many requests; wait a moment and retry.',
  'error.rate_limited_after': 'Too many requests; retry in about {seconds} s.',
  'error.coordinator_unavailable':
    'The coordinator is unavailable (the API replica cannot reach it).',
  'error.unknown': 'Something went wrong.',
  'error.unexpected': 'Unexpected error ({code}).',
  'error.uploadFailed': 'Upload failed.',
  'error.cancelFailed': 'Cancel failed.',
  'error.releaseFailed': 'Release failed.',

  'lang.label': 'Language',
  'lang.en': 'English',
  'lang.zh': '中文',
} as const

export type MessageKey = keyof typeof en
export type Messages = Record<MessageKey, string>
export type Locale = 'en' | 'zh-CN'

export const MESSAGES: Record<Locale, Messages> = {
  en,
  'zh-CN': zhCN,
}

export const LOCALES: { id: Locale; labelKey: MessageKey }[] = [
  { id: 'en', labelKey: 'lang.en' },
  { id: 'zh-CN', labelKey: 'lang.zh' },
]
