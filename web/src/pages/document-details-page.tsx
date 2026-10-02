import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  Download,
  FileText,
  LoaderCircle,
} from "lucide-react"
import { useRef, useState } from "react"
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom"
import {
  controlDocument,
  documentProcessing,
  documentStatusLabel,
  downloadDocument,
  getDocument,
  getDocumentPage,
  getDocumentChunk,
} from "@/lib/documents"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import type { DocumentDetails } from "@/lib/documents"
import { useAuth } from "@/lib/auth"
import { appendMessage, createConversation, errorMessage } from "@/lib/runtime"

export function DocumentDetailsPage() {
  const { documentID = "" } = useParams()
  const query = useQuery({
    queryKey: ["document", documentID],
    queryFn: ({ signal }) => getDocument(documentID, signal),
    refetchInterval: (query) =>
      query.state.data && documentProcessing(query.state.data.document.status)
        ? 2000
        : false,
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
        <>
          <DocumentHeader details={query.data} />
          <DocumentIngestion details={query.data} />
          {query.data.pages.length > 0 && (
            <DocumentWorkspace details={query.data} key={documentID} />
          )}
        </>
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
      <div className="knowledge-review-notice">
        <FileText size={20} />
        <div>
          <strong>
            {document.page_count} pages imported as source evidence
          </strong>
          <p>
            Inspect the original page evidence or start an additional focused
            task. Wiki changes become searchable after publication.
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
            For an additional focused task, give Blix an objective. It will read
            the selected evidence, compare it with the wiki, and draft updates
            for review.
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

function DocumentIngestion({ details }: { details: DocumentDetails }) {
  const { document, chunks, notes, changesets } = details
  const cache = useQueryClient()
  const auth = useAuth()
  const canControl = auth.user?.entity_id === document.uploaded_by
  const [selectedChunk, setSelectedChunk] = useState(1)
  const completed = chunks.filter((chunk) => chunk.completed_at !== null)
  const active = documentProcessing(document.status)
  const control = useMutation({
    mutationFn: (operation: "cancel" | "retry" | "complete") =>
      controlDocument(document.document_id, operation),
    onSuccess: (data) => {
      cache.setQueryData(["document", document.document_id], data)
      void cache.invalidateQueries({ queryKey: ["documents"] })
    },
  })
  const reading = useQuery({
    queryKey: ["document-chunk", document.document_id, selectedChunk],
    queryFn: ({ signal }) =>
      getDocumentChunk(document.document_id, selectedChunk, signal),
    enabled: completed.some((chunk) => chunk.number === selectedChunk),
    refetchInterval: active ? 5000 : false,
  })
  const citedNotes = (content: string) =>
    content.replace(/sourcerecord_[0-9a-z]{26}/g, (id) => {
      const page = details.pages.find((page) => page.source_record_id === id)
      return page
        ? `[PDF page ${page.page_number}](/knowledge/documents/${document.document_id}?page=${page.page_number})`
        : id
    })
  return (
    <section className="panel ingestion-panel">
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">DOCUMENT INGESTION</span>
          <h2>
            {active && <LoaderCircle className="spin" size={15} />}{" "}
            {documentStatusLabel(document.status)}
          </h2>
        </div>
        <div className="ingestion-actions">
          {document.conversation_id && (
            <Link
              className="secondary-button"
              to={`/chat/${document.conversation_id}`}
            >
              Discuss with Blix
            </Link>
          )}
          {canControl && active && document.status !== "cancelling" && (
            <button
              className="secondary-button"
              disabled={control.isPending}
              onClick={() => control.mutate("cancel")}
              type="button"
            >
              Cancel processing
            </button>
          )}
          {canControl && ["failed", "cancelled"].includes(document.status) && (
            <button
              className="primary-button"
              disabled={control.isPending}
              onClick={() => control.mutate("retry")}
              type="button"
            >
              Retry unfinished work
            </button>
          )}
          {canControl && document.status === "uploading" && (
            <button
              className="secondary-button"
              disabled={control.isPending}
              onClick={() => control.mutate("complete")}
              type="button"
            >
              Finish uploaded file
            </button>
          )}
        </div>
      </header>
      {chunks.length > 0 && (
        <div className="document-progress">
          <progress max={chunks.length} value={completed.length} />
          <span>
            {completed.length} / {chunks.length} reading chunks saved ·{" "}
            {document.page_count} PDF pages
          </span>
        </div>
      )}
      {active && (
        <p className="document-help">
          Processing continues in the background. You can leave this page and
          return later.
        </p>
      )}
      {document.error && (
        <p className="runtime-error" role="status">
          {document.error}
        </p>
      )}
      {control.error && (
        <p className="runtime-error" role="alert">
          {errorMessage(control.error)}
        </p>
      )}
      {document.objective && (
        <p className="document-help">Objective: {document.objective}</p>
      )}
      <p className="document-help">
        This pass reads extracted text. Images, diagrams, and visual table
        layout still need inspection. Ingestion drafts changes; it does not
        publish them.
      </p>
      {changesets.length > 0 && (
        <div className="ingestion-proposals">
          {changesets.map((changeset) => (
            <Link
              className="wiki-result"
              key={changeset.changeset_id}
              to={`/knowledge/proposals/${changeset.changeset_id}`}
            >
              <strong>{changeset.summary}</strong>
              <small>{changeset.status}</small>
            </Link>
          ))}
        </div>
      )}
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
          <label>
            Section{" "}
            <select
              value={selectedChunk}
              onChange={(event) => setSelectedChunk(Number(event.target.value))}
            >
              {completed.map((chunk) => (
                <option key={chunk.number} value={chunk.number}>
                  Chunk {chunk.number} · PDF pages {chunk.first_page}–
                  {chunk.last_page}
                </option>
              ))}
            </select>
          </label>
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

function DocumentHeader({ details }: { details: DocumentDetails }) {
  const { document } = details
  const download = useMutation({ mutationFn: () => downloadDocument(document) })
  return (
    <>
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">
            TEAM DOCUMENT · {document.kind.toUpperCase()}
          </span>
          <h1>{document.title}</h1>
          <p>
            {document.publisher} · {document.version} · Revised{" "}
            {document.published_at.slice(0, 10)}
          </p>
        </div>
        <button
          className="secondary-button"
          disabled={download.isPending || document.size_bytes === 0}
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
    </>
  )
}
