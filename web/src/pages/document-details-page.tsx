import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  Download,
  Eye,
  LoaderCircle,
} from "lucide-react"
import { Link, useParams, useSearchParams } from "react-router-dom"
import { useState } from "react"
import {
  controlDocument,
  documentProcessing,
  documentIngestionProgress,
  documentStatusLabel,
  downloadDocument,
  getDocument,
} from "@/lib/documents"
import type { DocumentDetails } from "@/lib/documents"
import { useAuth } from "@/lib/auth"
import { DocumentChat } from "@/components/document-chat"
import { DocumentReview } from "@/components/document-review"
import { DocumentMetadata } from "@/components/document-metadata"
import { DocumentPDFPreview } from "@/components/document-pdf-preview"
import { DocumentReadingNotes } from "@/components/document-reading-notes"
import { DocumentChangesets } from "@/components/document-changesets"
import { DocumentSources } from "@/components/document-sources"
import { ProgressBar } from "@/components/progress-bar"
import { errorMessage } from "@/lib/runtime"
import { DocumentTags } from "@/components/document-tags"
import { DocumentIngestionTimeline } from "@/components/document-ingestion-timeline"

export function DocumentDetailsPage() {
  const { documentID = "" } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const requestedPage = Number(searchParams.get("page") ?? 1)
  const previewPage =
    Number.isInteger(requestedPage) &&
    requestedPage >= 1 &&
    requestedPage <= 500
      ? requestedPage
      : 1
  const [previewDocumentID, setPreviewDocumentID] = useState<string | null>(
    null,
  )
  const query = useQuery({
    queryKey: ["document", documentID],
    queryFn: ({ signal }) => getDocument(documentID, signal),
    refetchInterval: (query) =>
      query.state.data && documentProcessing(query.state.data.document.status)
        ? 2000
        : ["pending", "in_review"].includes(
              query.state.data?.document.review_status ?? "",
            ) ||
            query.state.data?.changesets.some(
              (changeset) =>
                !["committed", "rejected"].includes(changeset.status),
            )
          ? 5000
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
          <DocumentHeader
            details={query.data}
            onPreview={() => setPreviewDocumentID(documentID)}
          />
          <DocumentReview details={query.data} />
          {query.data.document.review_status === "ingested" ? (
            <DocumentChangesets changesets={query.data.changesets} />
          ) : (
            <DocumentIngestion details={query.data} />
          )}
          <div className={
            query.data.ready_for_questions &&
            query.data.document.review_status !== "rejected" &&
            (query.data.document.extracted_metadata ||
              (!documentProcessing(query.data.document.status) && query.data.document.status !== "uploading"))
              ? "document-info-layout with-document-chat"
              : "document-info-layout"
          }>
            <DocumentMetadata
              details={query.data}
              key={`${documentID}:${query.data.document.metadata_revision}`}
            />
            {query.data.ready_for_questions && query.data.document.review_status !== "rejected" && (
              <DocumentChat details={query.data} key={`chat:${documentID}`} />
            )}
          </div>
          <DocumentReadingNotes details={query.data} key={documentID} />
          <DocumentSources details={query.data} key={`sources:${documentID}`} />
          {(previewDocumentID === documentID ||
            searchParams.get("preview") === "1") &&
            query.data.document.size_bytes > 0 && (
              <DocumentPDFPreview
                document={query.data.document}
                page={Math.min(
                  previewPage,
                  query.data.document.page_count || 500,
                )}
                onClose={() => {
                  setPreviewDocumentID(null)
                  if (searchParams.has("preview")) {
                    setSearchParams(
                      (params) => {
                        params.delete("preview")
                        params.delete("page")
                        return params
                      },
                      { replace: true },
                    )
                  }
                }}
                key={documentID}
              />
            )}
        </>
      )}
    </div>
  )
}

function DocumentIngestion({ details }: { details: DocumentDetails }) {
  const { document } = details
  const cache = useQueryClient()
  const auth = useAuth()
  const canControl =
    auth.user?.entity_id === document.uploaded_by &&
    document.review_status !== "rejected"
  const progress = documentIngestionProgress(details)
  const active = documentProcessing(document.status)
  const control = useMutation({
    mutationFn: (operation: "cancel" | "retry" | "complete") =>
      controlDocument(document.document_id, operation),
    onSuccess: (data) => {
      cache.setQueryData(["document", document.document_id], data)
      void cache.invalidateQueries({ queryKey: ["documents"] })
    },
  })
  return (
    <section className="panel ingestion-panel">
      <div className="ingestion-status-layout">
        <div className="ingestion-status-content">
          <header className="workspace-toolbar">
            <div>
              <span className="workspace-kicker">DOCUMENT INGESTION</span>
              <h2>
                {active && <LoaderCircle className="spin" size={15} />}{" "}
                {documentStatusLabel(document.status)}
              </h2>
            </div>
            <div className="ingestion-actions">
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
              {canControl &&
                ["failed", "cancelled"].includes(document.status) && (
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
          {document.page_count > 0 && (
            <div className="document-progress">
              <ProgressBar
                max={progress.max}
                value={progress.value}
                label="Document processing progress"
                valueText={progress.label}
              />
              <span>
                {progress.label} ·{" "}
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
        </div>
        <DocumentIngestionTimeline document={document} />
      </div>
    </section>
  )
}

function ingestionStage({ document }: DocumentDetails): string {
  if (document.review_status === "rejected")
    return "This document was rejected. Its saved evidence is retained for the review record."
  switch (document.status) {
    case "uploading":
      return "Your document is uploading."
    case "upload_failed":
      return "The upload didn’t finish. Import the PDF again to retry."
    case "queued":
      return "Your document is queued for processing."
    case "extracting":
      return "Blix is extracting page text and images, using OCR where needed."
    case "indexing":
      return "Blix is saving page evidence and making the document searchable."
    case "analyzing_visuals":
      return "Blix is analyzing page images and saving searchable visual evidence."
    case "identifying_metadata":
      return "Blix is identifying the document details."
    case "ready":
      return "Text, visual evidence, and document details are ready for your review."
    case "partial":
      return "Processing finished with some coverage gaps. Review the saved source evidence below."
    case "no_changes":
      return "Document evidence and details are ready for your review."
    case "failed":
      return "Blix couldn’t finish this document. Retry to continue from its saved progress."
    case "cancelling":
      return "Blix is stopping and keeping the evidence it has saved."
    case "cancelled":
      return "Processing stopped. Retry to continue from the saved progress."
    case "imported":
      return "Your document is available as source material."
  }
}

function DocumentHeader({
  details,
  onPreview,
}: {
  details: DocumentDetails
  onPreview: () => void
}) {
  const { document } = details
  const download = useMutation({
    mutationFn: () => downloadDocument(document),
  })
  return (
    <>
      <header className="workspace-toolbar">
        <div>
          <span className="workspace-kicker">TEAM DOCUMENT</span>
          <h1>{document.title}</h1>
          <p>{document.filename}</p>
          <DocumentTags tags={document.tags} />
        </div>
        <div className="ingestion-actions document-header-actions">
          <button
            className="secondary-button"
            disabled={document.size_bytes === 0}
            onClick={onPreview}
            type="button"
          >
            <Eye size={15} /> Preview
          </button>
          <button
            className="secondary-button"
            disabled={download.isPending || document.size_bytes === 0}
            onClick={() => download.mutate()}
            type="button"
          >
            <Download size={15} /> Download
          </button>
        </div>
      </header>
      {download.error && (
        <p role="alert" className="runtime-error">
          {errorMessage(download.error)}
        </p>
      )}
    </>
  )
}
