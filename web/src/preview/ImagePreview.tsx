import { useObjectUrl } from '../hooks/useObjectUrl'

export function ImagePreview({ file }: { file: File }) {
  const url = useObjectUrl(file)
  if (!url) return null
  return (
    <div className="flex h-full items-center justify-center overflow-auto rounded-md bg-[repeating-conic-gradient(#0000_0%_25%,#8881_0%_50%)] bg-[length:20px_20px] p-4">
      <img
        src={url}
        alt={`Original image ${file.name}`}
        className="max-h-full max-w-full object-contain shadow-sm"
      />
    </div>
  )
}
