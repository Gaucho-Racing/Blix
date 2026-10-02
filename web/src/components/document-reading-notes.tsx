import { useQuery } from "@tanstack/react-query"
import { LoaderCircle } from "lucide-react"
import { useState } from "react"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import { ThemedSelect } from "@/components/themed-select"
import {
  documentProcessing,
  getDocumentChunk,
  type DocumentDetails,
} from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

export function DocumentReadingNotes({
  details,
}: {
  details: DocumentDetails
}) {
  const { document, chunks, notes } = details
  const completed = chunks.filter((chunk) => chunk.completed_at !== null)
  const [selectedChunk, setSelectedChunk] = useState<number | null>(null)
  const currentChunk =
    completed.find((chunk) => chunk.number === selectedChunk)?.number ??
    completed[0]?.number
  const active = documentProcessing(document.status)
  const reading = useQuery({
    queryKey: ["document-chunk", document.document_id, currentChunk],
    queryFn: ({ signal }) =>
      getDocumentChunk(document.document_id, currentChunk!, signal),
    enabled: currentChunk !== undefined,
    refetchInterval: active ? 5000 : false,
  })
  const pageLabel = (id: string) => {
    const page = details.pages.find((page) => page.source_record_id === id)
    return page ? `PDF page ${page.page_number}` : id
  }
  const citedNotes = (content: string) =>
    content
      .replace(
        /\[([^\]]+)\]\((sourcerecord_[0-9a-z]{26})\)/g,
        (_, label: string, id: string) => `${label} (${pageLabel(id)})`,
      )
      .replace(/sourcerecord_[0-9a-z]{26}/g, pageLabel)
  if (!notes && !completed.length) return null

  return (
    <section className="panel document-reading-notes">
      <header>
        <h2>Reading notes</h2>
      </header>
      {notes && (
        <details className="ingestion-notes">
          <summary>Working notes · revision {notes.revision}</summary>
          <div className="wiki-markdown">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>
              {citedNotes(notes.notes)}
            </ReactMarkdown>
          </div>
        </details>
      )}
      {completed.length > 0 && (
        <details className="ingestion-notes">
          <summary>Saved section findings</summary>
          <div className="ingestion-section-selector">
            <ThemedSelect
              label="Reading section"
              value={String(currentChunk)}
              onValueChange={(value) => setSelectedChunk(Number(value))}
              options={completed.map((chunk) => ({
                value: String(chunk.number),
                label: `Chunk ${chunk.number} · PDF pages ${chunk.first_page}–${chunk.last_page}`,
              }))}
            />
          </div>
          {reading.isPending && (
            <p className="loading-state">
              <LoaderCircle className="spin" size={16} /> Loading section…
            </p>
          )}
          {reading.error && (
            <p role="alert" className="runtime-error">
              {errorMessage(reading.error)}
            </p>
          )}
          {reading.data && (
            <div className="wiki-markdown">
              <ReactMarkdown remarkPlugins={[remarkGfm]}>
                {citedNotes(reading.data.chunk.findings)}
              </ReactMarkdown>
            </div>
          )}
        </details>
      )}
    </section>
  )
}
