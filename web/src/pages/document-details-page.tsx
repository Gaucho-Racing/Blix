import { useMutation, useQuery } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  Download,
  FileText,
  LoaderCircle,
} from "lucide-react"
import { useRef, useState } from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { downloadDocument, getDocument, getDocumentPage } from "@/lib/documents"
import type { DocumentDetails } from "@/lib/documents"
import { appendMessage, createConversation, errorMessage } from "@/lib/runtime"

export function DocumentDetailsPage() {
  const { documentID = "" } = useParams()
  const query = useQuery({
    queryKey: ["document", documentID],
    queryFn: ({ signal }) => getDocument(documentID, signal),
  })
  return (
    <div className="workspace-page document-page">
      <Link className="knowledge-review-back" to="/knowledge">
        <ArrowLeft size={15} /> Team knowledge
      </Link>
      {query.isPending ? (
        <p className="loading-state">
          <LoaderCircle className="spin" size={18} /> Loading document…
        </p>
      ) : query.isError ? (
        <p className="runtime-error" role="alert">
          {errorMessage(query.error)}{" "}
          <button onClick={() => void query.refetch()} type="button">
            Retry
          </button>
        </p>
      ) : (
        <DocumentWorkspace details={query.data} key={documentID} />
      )}
    </div>
  )
}

function DocumentWorkspace({ details }: { details: DocumentDetails }) {
  const { document, pages } = details
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const requestedPage = Number(params.get("page") || 1)
  const [preview, setPreview] = useState(
    Number.isInteger(requestedPage) &&
      requestedPage >= 1 &&
      requestedPage <= document.page_count
      ? requestedPage
      : 1,
  )
  const [first, setFirst] = useState(preview)
  const [last, setLast] = useState(
    params.has("page") ? preview : Math.min(8, document.page_count),
  )
  const [objective, setObjective] = useState("")
  const conversation = useRef("")
  const page = useQuery({
    queryKey: ["document-page", document.document_id, preview],
    queryFn: ({ signal }) =>
      getDocumentPage(document.document_id, preview, signal),
    staleTime: Infinity,
  })
  const download = useMutation({ mutationFn: () => downloadDocument(document) })
  const selection = pages.filter(
    (page) => page.page_number >= first && page.page_number <= last,
  )
  const validRange =
    Number.isInteger(first) &&
    Number.isInteger(last) &&
    first >= 1 &&
    last >= first &&
    last <= document.page_count &&
    selection.length <= 20
  const emptyPages = selection.filter(
    (page) => page.character_count < 20,
  ).length
  const curate = useMutation({
    mutationFn: async () => {
      if (!validRange || !objective.trim() || emptyPages === selection.length)
        throw new Error(
          "Select 1–20 pages with readable text and describe the wiki update.",
        )
      if (!conversation.current)
        conversation.current = (await createConversation()).conversation_id
      const sourceIDs = selection.map((page) => page.source_record_id)
      const content = `Draft wiki updates from this imported document. Delegate to knowledge_curator_v1 with mode="propose" and these source_record_ids: ${JSON.stringify(sourceIDs)}. Do not publish.\n\nDocument metadata: ${JSON.stringify({ title: document.title, publisher: document.publisher, version: document.version, kind: document.kind, published_at: document.published_at.slice(0, 10), document_id: document.document_id, pdf_pages: [first, last] })}\n\nObjective: ${objective.trim()}\n\nRead the original page records before drafting. Preserve rule identifiers, PDF page references, revision and source citations. Treat roadmap items as future guidance rather than current requirements. These documents do not establish Gaucho Racing's actual vehicle configuration or compliance. Compare with existing wiki pages, separate requirements from team decisions, and report proposal links and uncertainties.`
      await appendMessage(
        conversation.current,
        content,
        `${objective.trim()}\n\nSource: ${document.title} · ${document.version} · PDF pages ${first}–${last}. Draft wiki updates for review.`,
      )
      return conversation.current
    },
    onSuccess: (id) => navigate(`/chat/${id}`),
  })
  return (
    <>
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">
            IMPORTED DOCUMENT · {document.kind.toUpperCase()}
          </span>
          <h1>{document.title}</h1>
          <p>
            {document.publisher} · Version {document.version} · Revised{" "}
            {document.published_at.slice(0, 10)}
          </p>
        </div>
        <button
          className="secondary-button"
          disabled={download.isPending}
          onClick={() => download.mutate()}
          type="button"
        >
          <Download size={15} /> Original PDF
        </button>
      </header>
      {download.error && (
        <p role="alert" className="runtime-error">
          {errorMessage(download.error)}
        </p>
      )}
      <div className="knowledge-review-notice">
        <FileText size={20} />
        <div>
          <strong>
            {document.page_count} pages imported as source evidence
          </strong>
          <p>
            Check the extracted text, select a focused range, and ask Blix to
            propose wiki changes. Imported evidence becomes searchable in the
            wiki after publication.
          </p>
          {document.external_url && (
            <a href={document.external_url} target="_blank" rel="noreferrer">
              Official source ↗
            </a>
          )}
        </div>
      </div>
      <div className="document-workspace">
        <section className="panel document-preview">
          <header>
            <div>
              <h2>Extracted evidence</h2>
              <span>PDF page numbers include the cover.</span>
            </div>
            <label>
              Page{" "}
              <select
                aria-label="Preview page"
                value={preview}
                onChange={(event) => setPreview(Number(event.target.value))}
              >
                {pages.map((page) => (
                  <option key={page.page_number} value={page.page_number}>
                    {page.page_number}
                    {page.character_count < 20 ? " · little or no text" : ""}
                  </option>
                ))}
              </select>
            </label>
          </header>
          {page.isPending ? (
            <p className="loading-state">
              <LoaderCircle className="spin" size={18} /> Loading page…
            </p>
          ) : page.isError ? (
            <p className="runtime-error" role="alert">
              {errorMessage(page.error)}
            </p>
          ) : (
            <pre>
              {page.data.content ||
                "No text was extracted from this page. It may need OCR or contain a diagram."}
            </pre>
          )}
          <footer>
            <button
              className="secondary-button"
              disabled={preview <= 1}
              onClick={() => setPreview(preview - 1)}
              type="button"
            >
              <ArrowLeft size={14} /> Previous
            </button>
            <span>
              {preview} / {document.page_count}
            </span>
            <button
              className="secondary-button"
              disabled={preview >= document.page_count}
              onClick={() => setPreview(preview + 1)}
              type="button"
            >
              Next <ArrowRight size={14} />
            </button>
          </footer>
        </section>
        <section className="panel document-curate">
          <span className="workspace-kicker">NEXT STEP</span>
          <h2>Make this useful to the team.</h2>
          <p>
            Give Blix a focused objective. It will read the selected evidence,
            compare it with the wiki, and draft updates for review.
          </p>
          <form
            className="document-form"
            onSubmit={(event) => {
              event.preventDefault()
              if (validRange && objective.trim()) curate.mutate()
            }}
          >
            <fieldset disabled={curate.isPending}>
              <div className="document-form-grid">
                <label>
                  From PDF page
                  <input
                    min={1}
                    max={document.page_count}
                    required
                    type="number"
                    value={first || ""}
                    onChange={(event) => setFirst(Number(event.target.value))}
                  />
                </label>
                <label>
                  Through PDF page
                  <input
                    min={first || 1}
                    max={document.page_count}
                    required
                    type="number"
                    value={last || ""}
                    onChange={(event) => setLast(Number(event.target.value))}
                  />
                </label>
              </div>
              <small className="document-help">
                Up to 20 pages per focused task. Start with a section, then
                curate additional sections as needed.
              </small>
              {!validRange && (
                <p role="alert" className="runtime-error">
                  Choose a valid range of 1–20 pages.
                </p>
              )}
              {emptyPages > 0 && (
                <p className="document-help">
                  {emptyPages} selected pages have little or no text. Check
                  diagrams in the original PDF.
                </p>
              )}
              <label>
                What should Blix learn?
                <textarea
                  required
                  rows={6}
                  maxLength={4000}
                  value={objective}
                  placeholder="Summarize the braking requirements with exact rule numbers. Separate mandatory requirements from team design decisions."
                  onChange={(event) => setObjective(event.target.value)}
                />
              </label>
              {curate.error && (
                <p role="alert" className="runtime-error">
                  {errorMessage(curate.error)}
                </p>
              )}
              <button
                className="primary-button"
                disabled={
                  !validRange ||
                  !objective.trim() ||
                  emptyPages === selection.length
                }
                type="submit"
              >
                {curate.isPending ? (
                  <LoaderCircle className="spin" size={16} />
                ) : (
                  <ArrowRight size={16} />
                )}
                {curate.isPending ? "Starting Blix…" : "Draft wiki updates"}
              </button>
            </fieldset>
          </form>
        </section>
      </div>
    </>
  )
}
