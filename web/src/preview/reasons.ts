/** Why a format has no original preview; shown instead of a blank pane. */
export const NO_PREVIEW_REASON: Record<string, string> = {
  pptx: 'PowerPoint decks have no safe in-browser renderer in this version. The parsed Markdown is shown on the right.',
  doc: 'Legacy Word (.doc) is a binary format with no browser renderer. The parsed Markdown is shown on the right.',
  xls: 'Legacy Excel (.xls) is a binary format with no browser renderer. The parsed Markdown is shown on the right.',
  ppt: 'Legacy PowerPoint (.ppt) has no browser renderer. The parsed Markdown is shown on the right.',
  other: 'This format has no in-browser preview yet. The parsed Markdown is shown on the right.',
}

export function noPreviewReason(kind: string): string {
  return NO_PREVIEW_REASON[kind] ?? NO_PREVIEW_REASON['other'] ?? 'No preview is available for this format.'
}
