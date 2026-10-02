import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowRight,
  Download,
  GitPullRequest,
  LoaderCircle,
} from "lucide-react"
import { useState } from "react"
import { Link, useParams } from "react-router-dom"
import {
  controlDocument,
  documentProcessing,
  documentStatusLabel,
  downloadDocument,
  getDocument,
  getDocumentChunk,
} from "@/lib/documents"
import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"
import type { DocumentDetails } from "@/lib/documents"
import { useAuth } from "@/lib/auth"
import { DocumentMetadata } from "@/components/document-metadata"
import { ProgressBar } from "@/components/progress-bar"
import { errorMessage } from "@/lib/runtime"
import { formatTimestamp } from "@/lib/format"
import { StatusBadge } from "@/components/status-badge"
import { ThemedSelect } from "@/components/themed-select"
import { DocumentTags } from "@/components/document-tags"

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
      <Link className="knowledge-review-back document-back" to="/knowledge">
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
          <DocumentMetadata
            details={query.data}
            key={`${documentID}:${query.data.document.metadata_revision}`}
          />
        </>
      )}
    </div>
  )
}

function DocumentIngestion({ details }: { details: DocumentDetails }) {
  const { document, chunks, notes, changesets } = details
  const cache = useQueryClient()
  const auth = useAuth()
  const canControl = auth.user?.entity_id === document.uploaded_by
  const [selectedChunk, setSelectedChunk] = useState<number | null>(null)
  const completed = chunks.filter((chunk) => chunk.completed_at !== null)
  const currentChunk =
    completed.find((chunk) => chunk.number === selectedChunk)?.number ??
    completed[0]?.number
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
      <p className="ingestion-stage" role="status">
        {ingestionStage(details)}
      </p>
      {chunks.length > 0 && (
        <div className="document-progress">
          <ProgressBar
            max={chunks.length}
            value={completed.length}
            label="Document reading progress"
            valueText={`${completed.length} of ${chunks.length} reading chunks saved`}
          />
          <span>
            {completed.length} / {chunks.length} reading chunks saved ·{" "}
            {document.page_count} PDF pages
          </span>
        </div>
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
      {changesets.length > 0 && (
        <div className="ingestion-proposals">
          {changesets.map((changeset) => (
            <Link
              className="knowledge-review-row ingestion-changeset"
              key={changeset.changeset_id}
              to={`/knowledge/proposals/${changeset.changeset_id}`}
            >
              <span className="knowledge-review-row-icon">
                <GitPullRequest size={20} />
              </span>
              <div className="knowledge-review-row-copy">
                <span className="workspace-kicker">KNOWLEDGE CHANGESET</span>
                <strong>{changeset.summary}</strong>
                <div className="ingestion-changeset-status">
                  <StatusBadge status={changeset.status} />
                </div>
                <code>{changeset.changeset_id}</code>
                <p>
                  {changeset.season_slug || "Season-independent"}
                  <span>·</span>
                  {changeset.type}
                  <span>·</span>
                  {formatTimestamp(changeset.created_at)}
                </p>
              </div>
              <ArrowRight size={18} />
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

function ingestionStage({ document, chunks }: DocumentDetails): string {
  switch (document.status) {
    case "uploading":
      return "Your document is uploading."
    case "upload_failed":
      return "The upload didn’t finish. Import the PDF again to retry."
    case "queued":
      return "Your document is queued for Blix to read."
    case "extracting":
      return "Blix is preparing the document for reading."
    case "reading":
      return chunks.length > 0 &&
        chunks.every((chunk) => chunk.completed_at !== null)
        ? "Blix has finished reading and is identifying the document details."
        : "Blix is reading the document and saving what it learns."
    case "reconciling":
      return "Blix is comparing its learnings with the wiki and preparing updates for review."
    case "ready":
      return "Blix finished learning from this document. Its learnings and knowledge updates are ready for your review."
    case "partial":
      return "Blix finished with some gaps. Review its saved learnings and proposed knowledge updates below."
    case "no_changes":
      return "Blix finished learning from this document. No wiki updates were proposed."
    case "failed":
      return "Blix couldn’t finish this document. Retry to continue from its saved learnings."
    case "cancelling":
      return "Blix is stopping and keeping the learnings it has saved."
    case "cancelled":
      return "Reading stopped. Retry to continue from the saved learnings."
    case "imported":
      return "Your document is available as source material."
  }
}

function DocumentHeader({ details }: { details: DocumentDetails }) {
  const { document } = details
  const download = useMutation({ mutationFn: () => downloadDocument(document) })
  return (
    <>
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">TEAM DOCUMENT</span>
          <h1>{document.title}</h1>
          <p>{document.filename}</p>
          <DocumentTags tags={document.tags} />
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
