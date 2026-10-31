import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
export const FIXTURES = join(here, '..', '..', 'benchmark', 'fixtures', 'generated')

export const pdf = join(FIXTURES, 'pdf_text_en_3p.pdf')
export const longPdf = join(FIXTURES, 'pdf_text_en_30p.pdf')
export const txt = join(FIXTURES, 'txt_plain.txt')
export const csvGb = join(FIXTURES, 'csv_gb18030.csv')
export const docx = join(FIXTURES, 'docx_report.docx')
export const xlsx = join(FIXTURES, 'xlsx_workbook.xlsx')

/** A file the API must reject with 415 (zip is not an accepted document). */
export function zipFixture(): Buffer {
  // Minimal empty zip archive.
  return Buffer.from([0x50, 0x4b, 0x05, 0x06, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0])
}
