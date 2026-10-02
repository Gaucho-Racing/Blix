import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, ArrowRight, LoaderCircle } from "lucide-react"
import { DocumentPageImage } from "@/components/document-page-image"
import { useState } from "react"
import { getDocumentPage, type DocumentDetails } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"

export function DocumentSources({ details }: { details: DocumentDetails }) {
  const { document, pages } = details
  const [view, setView] = useState<"text" | "visual">("text")
  const visualPages = pages.filter((page) => page.visual_source_record_id)
  const records = view === "visual" ? visualPages : pages
  const [selectedPage, setSelectedPage] = useState<number | null>(null)
  const page =
    records.find((page) => page.page_number === selectedPage) ?? records[0]
  const index = records.findIndex(
    (item) => item.page_number === page?.page_number,
  )
  const source = useQuery({
    queryKey: [
      "document-page",
      document.document_id,
      page?.page_number,
      page?.source_record_id,
      page?.visual_source_record_id,
    ],
    queryFn: ({ signal }) =>
      getDocumentPage(document.document_id, page!.page_number, signal),
    enabled: !!page,
    staleTime: Infinity,
  })

  const record = view === "visual" ? source.data?.visual_record : source.data
  const sourceID = view === "visual" ? page?.visual_source_record_id : page?.source_record_id
  const analyzed = pages.filter((page) => page.visual_analyzed_at).length

  if (!pages.length) return null

  return (
    <section className="panel document-sources">
      <header>
        <h2>Sources</h2>
        <span>
          {pages.length + visualPages.length} source records
        </span>
      </header>
      <div className="knowledge-comparison-toggle document-source-views" role="group" aria-label="Source type">
        <button type="button" aria-pressed={view === "text"} onClick={() => setView("text")}>
          Page text · {pages.length}
        </button>
        <button type="button" aria-pressed={view === "visual"} onClick={() => setView("visual")}>
          Visual extractions · {visualPages.length}
        </button>
      </div>
      {!page ? (
        <p className="document-source-empty" role="status">
          {analyzed === pages.length
            ? "No visual evidence was identified in this document."
            : "Visual extractions will appear as Blix analyzes the page images."}
        </p>
      ) : <div className="document-source-workspace">
        <ul
          className="document-source-record-list"
          aria-label="Stored source records"
        >
          {records.map((record) => (
            <li key={view === "visual" ? record.visual_source_record_id : record.source_record_id}>
              <button
                type="button"
                aria-pressed={record.page_number === page.page_number}
                onClick={() => setSelectedPage(record.page_number)}
              >
                <strong>PDF page {record.page_number}</strong>
                <code>{view === "visual" ? record.visual_source_record_id : record.source_record_id}</code>
              </button>
            </li>
          ))}
        </ul>
        <div className="document-source-detail">
          <header className="document-source-navigation">
            <div className="document-source-record-heading">
              <span className="workspace-kicker">{view === "visual" ? "VISUAL EXTRACTION" : "SOURCE RECORD"}</span>
              <h3>PDF page {page.page_number}</h3>
              <code>{sourceID}</code>
            </div>
            <div className="document-source-buttons">
              <button
                className="secondary-button"
                type="button"
                aria-label="Previous source record"
                disabled={index <= 0}
                onClick={() => setSelectedPage(records[index - 1].page_number)}
              >
                <ArrowLeft size={15} />
              </button>
              <button
                className="secondary-button"
                type="button"
                aria-label="Next source record"
                disabled={index >= records.length - 1}
                onClick={() => setSelectedPage(records[index + 1].page_number)}
              >
                <ArrowRight size={15} />
              </button>
            </div>
          </header>
          {record && (
            <dl className="document-source-provenance">
              <div>
                <dt>Source</dt>
                <dd>
                  <code>{record.source_id}</code>
                </dd>
              </div>
              <div>
                <dt>Recorded</dt>
                <dd>
                  <time dateTime={record.ingested_at}>
                    {formatTimestamp(record.ingested_at)}
                  </time>
                </dd>
              </div>
              <div>
                <dt>Record type</dt>
                <dd>
                  {record.record_type === "pdf_page"
                    ? "PDF page"
                    : record.record_type.replaceAll("_", " ")}
                </dd>
              </div>
              <div>
                <dt>Capture method</dt>
                <dd>{record.capture_method.replaceAll("_", " ")}</dd>
              </div>
            </dl>
          )}
          {view === "text" && source.data?.page_image && (
            <details className="document-source-visual">
              <summary>Page image</summary>
              <DocumentPageImage src={`/knowledge/documents/${document.document_id}/pages/${page.page_number}/image`}
                alt={`Original PDF page ${page.page_number}`} />
            </details>
          )}
          {view === "visual" && record && (
            <div className="document-visual-evidence">
              <DocumentPageImage src={`/knowledge/documents/${document.document_id}/pages/${page.page_number}/image`}
                alt={`Visual evidence from PDF page ${page.page_number}`} />
              <div>
                {!!record.metadata.section_ids?.length && (
                  <div className="document-visual-sections" aria-label="Related sections">
                    {record.metadata.section_ids.map((section) => <span key={section}>{section}</span>)}
                  </div>
                )}
                <pre>{record.content}</pre>
              </div>
            </div>
          )}
          <div
            className="document-source-content"
            aria-busy={source.isFetching}
          >
            {source.isPending ? (
              <p className="loading-state" role="status">
                <LoaderCircle className="spin" size={16} /> Loading source
                record…
              </p>
            ) : source.isError ? (
              <p className="runtime-error" role="alert">
                {errorMessage(source.error)}{" "}
                <button type="button" onClick={() => void source.refetch()}>
                  Retry
                </button>
              </p>
            ) : view === "visual" ? null : record?.content.trim() ? (
              <pre
                aria-label={`Source record content for PDF page ${page.page_number}`}
              >
                {record.content}
              </pre>
            ) : (
              <p className="document-source-empty">
                No text was extracted from this page.
              </p>
            )}
          </div>
        </div>
      </div>}
    </section>
  )
}
