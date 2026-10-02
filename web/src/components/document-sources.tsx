import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, ArrowRight, LoaderCircle } from "lucide-react"
import { useState } from "react"
import { ThemedSelect } from "@/components/themed-select"
import { getDocumentPage, type DocumentDetails } from "@/lib/documents"
import { errorMessage } from "@/lib/runtime"

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
          {pages.length} PDF {pages.length === 1 ? "page" : "pages"}
        </span>
      </header>
      <div className="document-source-navigation">
        <div className="document-source-selector">
          <ThemedSelect
            label="Source page"
            value={String(page.page_number)}
            onValueChange={(value) => setSelectedPage(Number(value))}
            options={pages.map((page) => ({
              value: String(page.page_number),
              label: `PDF page ${page.page_number}`,
            }))}
          />
        </div>
        <div className="document-source-buttons">
          <button
            className="secondary-button"
            type="button"
            aria-label="Previous source page"
            disabled={index <= 0}
            onClick={() => setSelectedPage(pages[index - 1].page_number)}
          >
            <ArrowLeft size={15} />
          </button>
          <button
            className="secondary-button"
            type="button"
            aria-label="Next source page"
            disabled={index >= pages.length - 1}
            onClick={() => setSelectedPage(pages[index + 1].page_number)}
          >
            <ArrowRight size={15} />
          </button>
        </div>
      </div>
      <div className="document-source-content" aria-busy={source.isFetching}>
        {source.isPending ? (
          <p className="loading-state" role="status">
            <LoaderCircle className="spin" size={16} /> Loading source…
          </p>
        ) : source.isError ? (
          <p className="runtime-error" role="alert">
            {errorMessage(source.error)}{" "}
            <button type="button" onClick={() => void source.refetch()}>
              Retry
            </button>
          </p>
        ) : source.data.content.trim() ? (
          <pre aria-label={`Extracted text from PDF page ${page.page_number}`}>
            {source.data.content}
          </pre>
        ) : (
          <p className="document-source-empty">
            No text was extracted from this page.
          </p>
        )}
      </div>
    </section>
  )
}
