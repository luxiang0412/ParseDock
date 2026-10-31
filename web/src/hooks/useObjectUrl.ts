import { useEffect, useMemo } from 'react'

/**
 * A blob: URL for a file, revoked when the input changes or the component unmounts. Derived
 * with useMemo so no effect has to write state (the URL is a pure function of the blob).
 */
export function useObjectUrl(blob: Blob | null | undefined): string | null {
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : null), [blob])
  useEffect(() => {
    if (!url) return
    return () => URL.revokeObjectURL(url)
  }, [url])
  return url
}
