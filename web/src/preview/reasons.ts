import type { MessageKey } from '../i18n/messages'

/** Why a format has no original preview; shown instead of a blank pane. */
const REASON_KEY: Record<string, MessageKey> = {
  pptx: 'preview.pptx',
  doc: 'preview.doc',
  xls: 'preview.xls',
  ppt: 'preview.ppt',
}

export function noPreviewReason(kind: string): MessageKey {
  return REASON_KEY[kind] ?? 'preview.unsupported'
}
