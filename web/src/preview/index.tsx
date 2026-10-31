import type { ReactNode } from 'react'
import { previewKind } from '../features/playground/format'
import { CsvPreview } from './CsvPreview'
import { DocxPreview } from './DocxPreview'
import { ImagePreview } from './ImagePreview'
import { PdfPreview } from './PdfPreview'
import { TextPreview } from './TextPreview'
import { noPreviewReason } from './reasons'
import { UnsupportedPreview } from './UnsupportedPreview'
import { XlsxPreview } from './XlsxPreview'

export function Preview({ file }: { file: File }): ReactNode {
  switch (previewKind(file.name)) {
    case 'pdf':
      return <PdfPreview file={file} />
    case 'image':
      return <ImagePreview file={file} />
    case 'csv':
      return <CsvPreview file={file} />
    case 'markdown':
      return <TextPreview file={file} mono={false} />
    case 'text':
      return <TextPreview file={file} />
    case 'html':
      // Never render user HTML: show the source.
      return <TextPreview file={file} />
    case 'docx':
      return <DocxPreview file={file} />
    case 'xlsx':
      return <XlsxPreview file={file} />
    default:
      return <UnsupportedPreview file={file} reason={noPreviewReason(previewKind(file.name))} />
  }
}
