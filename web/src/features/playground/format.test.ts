import { describe, expect, it } from 'vitest'
import type { Translator } from './format'
import { translate } from '../../i18n'
import {
  describeExpiry,
  extensionOf,
  humanBytes,
  humanMs,
  isAccepted,
  previewKind,
} from './format'

describe('previewKind', () => {
  it.each([
    ['a.pdf', 'pdf'],
    ['a.PDF', 'pdf'],
    ['shot.png', 'image'],
    ['shot.JPEG', 'image'],
    ['notes.md', 'markdown'],
    ['data.csv', 'csv'],
    ['readme.txt', 'text'],
    ['page.html', 'html'],
    ['report.docx', 'docx'],
    ['ledger.xlsx', 'xlsx'],
    ['ledger.xlsm', 'xlsx'],
    ['deck.pptx', 'unsupported'],
    ['legacy.doc', 'unsupported'],
    ['archive.zip', 'unsupported'],
    ['noext', 'unsupported'],
  ])('%s → %s', (name, kind) => {
    expect(previewKind(name)).toBe(kind)
  })
})

describe('accept list mirrors the backend allow-list', () => {
  it('accepts the formats the server accepts', () => {
    for (const name of ['a.pdf', 'a.docx', 'a.xlsx', 'a.xlsm', 'a.pptx', 'a.csv', 'a.tsv', 'a.txt', 'a.md', 'a.png', 'a.jpg', 'a.webp', 'a.html', 'a.doc', 'a.xls', 'a.ppt']) {
      expect(isAccepted({ name }), name).toBe(true)
    }
  })
  it('rejects zip and unknown extensions', () => {
    expect(isAccepted({ name: 'x.zip' })).toBe(false)
    expect(isAccepted({ name: 'x.exe' })).toBe(false)
    expect(isAccepted({ name: 'x' })).toBe(false)
  })
})

describe('formatting', () => {
  it('formats bytes with binary units', () => {
    expect(humanBytes(512)).toBe('512 B')
    expect(humanBytes(2048)).toBe('2.0 KiB')
    expect(humanBytes(5 * 1024 * 1024)).toBe('5.0 MiB')
    expect(humanBytes(undefined)).toBe('–')
  })
  it('formats durations', () => {
    expect(humanMs(13)).toBe('13 ms')
    expect(humanMs(1500)).toBe('1.5 s')
    expect(humanMs(65_000)).toBe('1 min 5 s')
    expect(humanMs(undefined)).toBe('–')
  })
  it('describes the expiry window in both directions', () => {
    const now = Date.parse('2026-01-01T12:00:00Z')
    expect(describeExpiry('2026-01-01T12:42:00Z', undefined, now)).toBe('in 42 min')
    expect(describeExpiry('2026-01-01T11:57:00Z', undefined, now)).toBe('3 min ago')
    expect(describeExpiry(null, undefined, now)).toBe('no result window')
  })

  it('localises the expiry window and units through a translator', () => {
    const now = Date.parse('2026-01-01T12:00:00Z')
    const zh: Translator = (key, vars) => translate('zh-CN', key, vars)
    expect(describeExpiry('2026-01-01T12:42:00Z', zh, now)).toBe('42 分钟后')
    expect(describeExpiry(null, zh, now)).toBe('无结果窗口')
    expect(humanMs(1500, zh)).toBe('1.5 秒')
  })
  it('reads extensions case-insensitively', () => {
    expect(extensionOf('A.PDF')).toBe('pdf')
    expect(extensionOf('noext')).toBe('')
  })
})
