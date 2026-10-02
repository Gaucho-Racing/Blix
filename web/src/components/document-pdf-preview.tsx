import { useQuery } from "@tanstack/react-query"
import { LoaderCircle, X } from "lucide-react"
import { useEffect, useId, useRef, useState } from "react"
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
  const pointerStartedOutside = useRef(false)
  const [closing, setClosing] = useState(false)
  const titleID = useId()
  useEffect(() => {
    const modal = dialog.current
    modal?.showModal()
    return () => modal?.close()
  }, [])
  useEffect(() => {
    if (!closing) return
    let cancelled = false
    const animations = dialog.current?.getAnimations() ?? []
    void Promise.allSettled(
      animations.map((animation) => animation.finished),
    ).then(() => {
      if (!cancelled) onClose()
    })
    return () => {
      cancelled = true
    }
  }, [closing, onClose])
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
      className={`document-pdf-preview${closing ? " is-closing" : ""}`}
      aria-labelledby={titleID}
      onPointerDown={(event) => {
        pointerStartedOutside.current = outsideDialog(
          event.currentTarget,
          event.clientX,
          event.clientY,
        )
      }}
      onPointerCancel={() => {
        pointerStartedOutside.current = false
      }}
      onClick={(event) => {
        const outside =
          pointerStartedOutside.current &&
          outsideDialog(event.currentTarget, event.clientX, event.clientY)
        pointerStartedOutside.current = false
        if (outside) setClosing(true)
      }}
      onCancel={(event) => {
        event.preventDefault()
        setClosing(true)
      }}
    >
      <header>
        <div>
          <h2 id={titleID}>Document preview</h2>
          <p>{document.title || document.filename}</p>
        </div>
        <button
          className="secondary-button"
          type="button"
          onClick={() => setClosing(true)}
        >
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

function outsideDialog(dialog: HTMLDialogElement, x: number, y: number) {
  const bounds = dialog.getBoundingClientRect()
  return (
    x < bounds.left || x > bounds.right || y < bounds.top || y > bounds.bottom
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
