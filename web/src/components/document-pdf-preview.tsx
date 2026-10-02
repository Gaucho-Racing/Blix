import { useQuery } from "@tanstack/react-query"
import { LoaderCircle } from "lucide-react"
import { useEffect, useRef } from "react"
import { getDocumentFile, type TeamDocument } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function DocumentPDFPreview({ document }: { document: TeamDocument }) {
  const pdf = useQuery({
    queryKey: ["document-file", document.document_id],
    queryFn: ({ signal }) => getDocumentFile(document.document_id, signal),
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })

  return (
    <section className="panel document-pdf-preview">
      <header>
        <h2 className="workspace-kicker">DOCUMENT PREVIEW</h2>
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
    </section>
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
