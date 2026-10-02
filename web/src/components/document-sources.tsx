import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, ArrowRight, LoaderCircle } from "lucide-react"
import { DocumentPageImage } from "@/components/document-page-image"
import { useState } from "react"
import { getDocumentPage, type DocumentDetails } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"

export function DocumentSources({ details }: { details: DocumentDetails }) {
  const { document, pages } = details
  const [selectedPage, setSelectedPage] = useState<number | null>(null)
  const page =
    pages.find((page) => page.page_number === selectedPage) ?? pages[0]
  const index = pages.findIndex(
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

  if (!pages.length) return null

  return (
    <section className="panel document-sources">
      <header>
        <h2>Sources</h2>
        <span>
          {pages.length} source {pages.length === 1 ? "record" : "records"}
        </span>
      </header>
      <div className="document-source-workspace">
        <ul
          className="document-source-record-list"
          aria-label="Stored source records"
        >
          {pages.map((record) => (
            <li key={record.source_record_id}>
              <button
                type="button"
                aria-pressed={record.source_record_id === page.source_record_id}
                onClick={() => setSelectedPage(record.page_number)}
              >
                <strong>PDF page {record.page_number}</strong>
                <code>{record.source_record_id}</code>
              </button>
            </li>
          ))}
        </ul>
        <div className="document-source-detail">
          <header className="document-source-navigation">
            <div className="document-source-record-heading">
              <span className="workspace-kicker">SOURCE RECORD</span>
              <h3>PDF page {page.page_number}</h3>
              <code>{page.source_record_id}</code>
            </div>
            <div className="document-source-buttons">
              <button
                className="secondary-button"
                type="button"
                aria-label="Previous source record"
                disabled={index <= 0}
                onClick={() => setSelectedPage(pages[index - 1].page_number)}
              >
                <ArrowLeft size={15} />
              </button>
              <button
                className="secondary-button"
                type="button"
                aria-label="Next source record"
                disabled={index >= pages.length - 1}
                onClick={() => setSelectedPage(pages[index + 1].page_number)}
              >
                <ArrowRight size={15} />
              </button>
            </div>
          </header>
          {source.data && (
            <dl className="document-source-provenance">
              <div>
                <dt>Source</dt>
                <dd>
                  <code>{source.data.source_id}</code>
                </dd>
              </div>
              <div>
                <dt>Recorded</dt>
                <dd>
                  <time dateTime={source.data.ingested_at}>
                    {formatTimestamp(source.data.ingested_at)}
                  </time>
                </dd>
              </div>
              <div>
                <dt>Record type</dt>
                <dd>
                  {source.data.record_type === "pdf_page"
                    ? "PDF page"
                    : source.data.record_type.replaceAll("_", " ")}
                </dd>
              </div>
              <div>
                <dt>Capture method</dt>
                <dd>{source.data.capture_method.replaceAll("_", " ")}</dd>
              </div>
            </dl>
          )}
          {source.data?.page_image && (
            <details className="document-source-visual">
              <summary>Page image</summary>
              <DocumentPageImage src={`/knowledge/documents/${document.document_id}/pages/${page.page_number}/image`}
                alt={`Original PDF page ${page.page_number}`} />
            </details>
          )}
          {source.data?.visual_record && (
            <details className="document-source-visual">
              <summary>Visual findings</summary>
              <p className="document-source-empty">Blix’s interpretation of this page’s diagrams and tables.</p>
              <code>{source.data.visual_record.source_record_id}</code>
              <pre>{source.data.visual_record.content}</pre>
            </details>
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
            ) : source.data.content.trim() ? (
              <pre
                aria-label={`Source record content for PDF page ${page.page_number}`}
              >
                {source.data.content}
              </pre>
            ) : (
              <p className="document-source-empty">
                No text was extracted from this page.
              </p>
            )}
          </div>
        </div>
      </div>
    </section>
  )
}
