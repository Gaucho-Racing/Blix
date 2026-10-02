import { useQuery } from "@tanstack/react-query"
import { LoaderCircle } from "lucide-react"
import { useEffect, useRef } from "react"
import { getDocumentPageImage } from "@/lib/documents"

export function DocumentPageImage({ src, alt }: { src?: string; alt?: string }) {
  const match = src?.match(/^\/knowledge\/documents\/(document_[0-9a-z]{26})\/pages\/(\d{1,3})\/image$/)
  const documentID = match?.[1] ?? ""
  const page = Number(match?.[2])
  const valid = !!match && page >= 1 && page <= 500
  const image = useQuery({
    queryKey: ["document-page-image", documentID, page],
    queryFn: ({ signal }) => getDocumentPageImage(documentID, page, signal),
    enabled: valid,
    staleTime: Infinity,
  })
  const element = useRef<HTMLImageElement>(null)
  useEffect(() => {
    if (!image.data || !element.current) return
    const objectURL = URL.createObjectURL(image.data)
    element.current.src = objectURL
    return () => URL.revokeObjectURL(objectURL)
  }, [image.data])
  if (!valid) return <span>{alt || "Image unavailable"}</span>
  if (image.isError) return <span role="alert">Page image unavailable. <button type="button" onClick={() => void image.refetch()}>Retry</button></span>
  if (!image.data) return <span className="loading-state" role="status"><LoaderCircle className="spin" size={14} /> Loading page image…</span>
  return <a className="document-page-image" href={`/knowledge/documents/${documentID}?preview=1&page=${page}`} target="_blank" rel="noreferrer">
    <img ref={element} alt={alt || `PDF page ${page}`} />
  </a>
}
