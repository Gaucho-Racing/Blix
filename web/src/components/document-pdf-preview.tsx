import { useQuery } from "@tanstack/react-query"
import { LoaderCircle, X } from "lucide-react"
import { useEffect, useId, useRef } from "react"
import { getDocumentFile, type TeamDocument } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function DocumentPDFPreview({
  document,
  onClose,
}: {
  document: TeamDocument
  onClose: () => void
}) {
  const dialog = useRef<HTMLDialogElement>(null)
  const titleID = useId()
  useEffect(() => {
    const modal = dialog.current
    modal?.showModal()
    return () => modal?.close()
  }, [])
  const pdf = useQuery({
    queryKey: ["document-file", document.document_id],
    queryFn: ({ signal }) => getDocumentFile(document.document_id, signal),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })

  return (
    <dialog
      ref={dialog}
      className="document-pdf-preview"
      aria-labelledby={titleID}
      onCancel={(event) => {
        event.preventDefault()
        onClose()
      }}
    >
      <header>
        <div>
          <h2 id={titleID}>Document preview</h2>
          <p>{document.title || document.filename}</p>
        </div>
        <button className="secondary-button" type="button" onClick={onClose}>
          <X size={15} /> Close
        </button>
      </header>
      {pdf.isPending ? (
        <p className="loading-state" role="status">
          <LoaderCircle className="spin" size={18} /> Loading PDF…
        </p>
      ) : pdf.isError ? (
        <p className="runtime-error" role="alert">
          {errorMessage(pdf.error)}{" "}
          <button type="button" onClick={() => void pdf.refetch()}>
            Retry preview
          </button>
        </p>
      ) : (
        <PDFFrame blob={pdf.data} title={document.title || document.filename} />
      )}
    </dialog>
  )
}

function PDFFrame({ blob, title }: { blob: Blob; title: string }) {
  const frame = useRef<HTMLIFrameElement>(null)

  useEffect(() => {
    const viewer = frame.current
    if (!viewer) return
    const url = URL.createObjectURL(blob)
    viewer.src = url
    return () => {
      viewer.removeAttribute("src")
      URL.revokeObjectURL(url)
    }
  }, [blob])

  return <iframe ref={frame} title={`PDF preview: ${title}`} />
}
