import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowRight,
  Check,
  FileCode2,
  GitCommitHorizontal,
  LoaderCircle,
  RefreshCw,
  X,
} from "lucide-react"
import { useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { ReviewConfirmationDialog } from "@/components/review-confirmation-dialog"
import { WikiMarkdown } from "@/components/wiki-markdown"
import { StatusBadge } from "@/components/status-badge"
import { formatTimestamp } from "@/lib/format"
import {
  getChangesetBase,
  reviewChangeset,
  type WikiChangeset,
  type WikiChangesetFile,
} from "@/lib/knowledge"
import { useAuth } from "@/lib/auth"
import { errorMessage } from "@/lib/runtime"
import { documentProcessingComplete } from "@/lib/documents"
import type { DocumentCitation, TeamDocument } from "@/lib/documents"

export function ChangesetReview({
  changeset,
  files,
  onRefresh,
  refreshing,
  admin,
  citations,
  documents,
}: {
  documents: TeamDocument[]
  admin: boolean
  citations: Record<string, DocumentCitation>
  changeset: WikiChangeset
  files: WikiChangesetFile[]
  onRefresh: () => void
  refreshing: boolean
}) {
  const auth = useAuth()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!changeset.publication_lease_until) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [changeset.publication_lease_until])
  const [view, setView] = useState<"rendered" | "markdown">("rendered")
  const [selected, setSelected] = useState(files[0]?.path ?? "")
  const [decision, setDecision] = useState<"approve" | "reject" | null>(null)
  const [note, setNote] = useState("")
  const queryClient = useQueryClient()
  const file = files.find((item) => item.path === selected) ?? files[0]
  const mutation = useMutation({
    mutationFn: ({
      action,
      reviewNote,
    }: {
      action: "approve" | "reject"
      reviewNote: string
    }) => reviewChangeset(changeset.changeset_id, action, reviewNote),
    onSuccess: (data) => {
      queryClient.setQueryData(
        ["knowledge-changeset", changeset.changeset_id],
        data,
      )
      setDecision(null)
      setNote("")
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["knowledge-changesets"] }),
        queryClient.invalidateQueries({
          queryKey: ["knowledge-changeset", changeset.changeset_id],
        }),
        queryClient.invalidateQueries({ queryKey: ["document"] }),
        queryClient.invalidateQueries({ queryKey: ["documents"] }),
        queryClient.invalidateQueries({ queryKey: ["wiki-search"] }),
        queryClient.invalidateQueries({ queryKey: ["wiki-page"] }),
      ])
    },
  })
  const publicationInProgress =
    !!changeset.publication_lease_until &&
    new Date(changeset.publication_lease_until).getTime() > now
  const canReview =
    auth.isAuthenticated &&
    (auth.isAdmin || !!auth.user?.groups.includes("BlixUsers"))
  const canApprove =
    canReview &&
    auth.isAdmin &&
    !publicationInProgress &&
    ["proposed", "failed", "prepared"].includes(changeset.status) &&
    documents.every(
      (document) =>
        document.review_status !== "rejected" &&
        (document.review_status === "ingested" ||
          (document.review_status === "in_review" &&
            documentProcessingComplete(document.status))),
    )
  const canReject =
    canReview &&
    !publicationInProgress &&
    ["proposed", "failed"].includes(changeset.status) &&
    !changeset.result_commit_sha
  const retryPublication =
    Boolean(changeset.result_commit_sha) ||
    changeset.review_decision === "approved"
  const choose = (action: "approve" | "reject") => {
    mutation.reset()
    setDecision(action)
    setNote("")
  }

  return (
    <>
      <header className="page-header knowledge-review-heading">
        <div>
          <div className="eyebrow">
            {admin ? "ADMIN · KNOWLEDGE REVIEW" : "KNOWLEDGE CHANGESET"}
          </div>
          <h1>{changeset.summary}</h1>
          <code>{changeset.changeset_id}</code>
        </div>
        <div className="knowledge-review-heading-actions">
          <StatusBadge status={changeset.status} />
        </div>
      </header>
      {changeset.reviewed_by && (
        <section
          className={`panel knowledge-review-audit ${changeset.review_decision === "rejected" ? "is-rejected" : "is-approved"}`}
          aria-label="Changeset review decision"
        >
          <span className="knowledge-review-decision-icon" aria-hidden="true">
            {changeset.review_decision === "rejected" ? (
              <X size={24} />
            ) : (
              <Check size={24} />
            )}
          </span>
          <div>
            <span className="workspace-kicker">CHANGESET DECISION</span>
            <h2>
              {changeset.review_decision === "rejected"
                ? "Rejected"
                : changeset.status === "committed"
                  ? "Approved and published"
                  : "Approved for publication"}
            </h2>
            {changeset.rejected_document_id && (
              <small>Rejected with its source document</small>
            )}
            {changeset.reviewed_at && (
              <time dateTime={changeset.reviewed_at}>
                {formatTimestamp(changeset.reviewed_at)}
              </time>
            )}
            <code>{changeset.reviewed_by}</code>
            {changeset.review_note && <p>{changeset.review_note}</p>}
          </div>
        </section>
      )}
      <section className="panel knowledge-review-context">
        <div>
          <span>Scope</span>
          <strong>{changeset.season_slug || "Season-independent"}</strong>
        </div>
        <div>
          <span>Change type</span>
          <strong>
            {changeset.type} · {files.length}{" "}
            {files.length === 1 ? "file" : "files"}
          </strong>
        </div>
        <div>
          <span>Created</span>
          <strong>{formatTimestamp(changeset.created_at)}</strong>
        </div>
        <div>
          <span>Origin</span>
          <strong>{changeset.origin}</strong>
        </div>
        {changeset.conversation_id && (
          <div className="knowledge-review-provenance">
            <span>Source conversation / session</span>
            <Link to={`/chat/${changeset.conversation_id}`}>
              {changeset.conversation_id}
            </Link>
            <code>{changeset.session_id}</code>
          </div>
        )}
        <div className="knowledge-review-provenance">
          <span>Base revision</span>
          <code>{changeset.base_commit_sha || "Empty repository"}</code>
        </div>
      </section>
      {changeset.error && (
        <div className="knowledge-review-warning" role="alert">
          <strong>Publication did not complete</strong>
          <p>{changeset.error}</p>
          <small>
            Revision conflicts require a fresh proposal from Blix. No
            force-publish is performed.
          </small>
        </div>
      )}
      {changeset.status === "committed" && (
        <div className="knowledge-review-published">
          <GitCommitHorizontal size={19} />
          <div>
            <strong>
              Published to the wiki
              {changeset.committed_at &&
                ` · ${formatTimestamp(changeset.committed_at)}`}
            </strong>
            <code>{changeset.result_commit_sha}</code>
          </div>
        </div>
      )}
      {documents.length > 0 && (
        <section className="panel changeset-document-sources">
          <span className="workspace-kicker">SOURCE DOCUMENTS</span>
          {documents.map((document) => (
            <Link
              key={document.document_id}
              to={`/knowledge/documents/${document.document_id}`}
            >
              <span>{document.title}</span>
              <StatusBadge
                status={
                  document.review_status === "pending"
                    ? document.status
                    : document.review_status
                }
              />
              <ArrowRight size={15} />
            </Link>
          ))}
        </section>
      )}
      {Object.keys(citations).length > 0 && (
        <details className="panel knowledge-review-sources">
          <summary>
            Source evidence · {Object.keys(citations).length} references
          </summary>
          <div>
            {Object.values(citations).map((citation) => (
              <div key={citation.source_record_id}>
                {citation.document_id ? (
                  <Link to={`/knowledge/documents/${citation.document_id}`}>
                    {citation.source_name} · PDF page {citation.pdf_page}
                  </Link>
                ) : citation.external_url ? (
                  <a
                    href={citation.external_url}
                    target="_blank"
                    rel="noreferrer"
                  >
                    {citation.source_name}
                  </a>
                ) : (
                  <span>{citation.source_name}</span>
                )}
                <code>{citation.source_record_id}</code>
              </div>
            ))}
          </div>
        </details>
      )}
      <section className="panel knowledge-review-files">
        <div className="panel-toolbar">
          <div>
            <h2>Proposed files</h2>
            <span>
              Compare with the exact base revision, not the current wiki.
            </span>
          </div>
          <div className="knowledge-comparison-controls">
            <div
              className="knowledge-comparison-toggle"
              role="group"
              aria-label="Comparison format"
            >
              {(["rendered", "markdown"] as const).map((format) => (
                <button
                  type="button"
                  key={format}
                  aria-pressed={view === format}
                  onClick={() => setView(format)}
                >
                  {format === "rendered" ? "Rendered" : "Markdown"}
                </button>
              ))}
            </div>
            <button
              className="secondary-button"
              disabled={refreshing || mutation.isPending}
              onClick={onRefresh}
              type="button"
            >
              <RefreshCw className={refreshing ? "spin" : ""} size={14} />{" "}
              Refresh
            </button>
          </div>
        </div>
        <div className="knowledge-review-file-tabs" aria-label="Changed files">
          {files.map((item) => (
            <button
              aria-pressed={file?.path === item.path}
              className={file?.path === item.path ? "active" : ""}
              key={item.path}
              onClick={() => setSelected(item.path)}
              type="button"
            >
              <FileCode2 size={14} />
              <span>{item.path}</span>
              <small>{item.operation === "delete" ? "delete" : "upsert"}</small>
            </button>
          ))}
        </div>
        {file && (
          <FileComparison
            changesetID={changeset.changeset_id}
            file={file}
            key={file.path}
            view={view}
            proposedPaths={files
              .filter((item) => item.operation !== "delete")
              .map((item) => item.path)}
            onSelectPath={setSelected}
          />
        )}
      </section>
      {(canApprove || canReject || publicationInProgress) && (
        <section className="panel knowledge-review-actions">
          <div>
            <strong>
              {publicationInProgress
                ? "Publication in progress"
                : retryPublication
                  ? "Reconcile publication"
                  : "Ready for review"}
            </strong>
            <p>
              {publicationInProgress
                ? "Blix is publishing these changes. This page will update when it finishes."
                : canReject
                  ? auth.isAdmin
                    ? "Approve to publish these changes, or reject them with a reason."
                    : "You can reject this proposal with a reason. A BlixAdmin can approve and publish it."
                  : "Publication has started. Retry to resolve its outcome."}
            </p>
          </div>
          {(canApprove || canReject) && (
            <div>
              {canReject && (
                <button
                  className="secondary-button knowledge-reject-button"
                  disabled={mutation.isPending}
                  onClick={() => choose("reject")}
                  type="button"
                >
                  <X size={15} /> Reject
                </button>
              )}
              {canApprove && (
                <button
                  className="primary-button"
                  disabled={mutation.isPending}
                  onClick={() => choose("approve")}
                  type="button"
                >
                  <Check size={15} />{" "}
                  {retryPublication ? "Retry publication" : "Approve & publish"}
                </button>
              )}
            </div>
          )}
        </section>
      )}
      {decision && (
        <ReviewConfirmationDialog
          title={
            decision === "reject"
              ? "Reject this changeset?"
              : "Publish these changes?"
          }
          confirmLabel={
            decision === "reject" ? "Reject changeset" : "Approve & publish"
          }
          reject={decision === "reject"}
          description={
            <>
              <p>
                {decision === "reject"
                  ? "This changeset will be rejected and cannot be published. Its source documents and other changesets will keep their current status."
                  : `This will publish ${files.length} ${files.length === 1 ? "file change" : "file changes"} to the wiki. Existing files may be replaced or deleted.`}
              </p>
              {decision === "approve" &&
                documents.some(
                  (document) => document.review_status === "in_review",
                ) && (
                  <p>
                    The following source documents will also be marked Ingested
                    after publication succeeds:{" "}
                    <strong>
                      {documents
                        .filter(
                          (document) => document.review_status === "in_review",
                        )
                        .map((document) => document.title)
                        .join(", ")}
                    </strong>
                    . Their other changesets will still need separate approval.
                  </p>
                )}
              <p>
                Your identity, review note, and decision time will be recorded.
              </p>
            </>
          }
          note={note}
          setNote={setNote}
          pending={mutation.isPending}
          allowed={decision === "approve" ? canApprove : canReject}
          error={mutation.isError ? errorMessage(mutation.error) : null}
          onClose={() => {
            if (!mutation.isPending) setDecision(null)
          }}
          onSubmit={() =>
            mutation.mutate({ action: decision, reviewNote: note })
          }
        />
      )}
    </>
  )
}

function FileComparison({
  changesetID,
  file,
  view,
  proposedPaths,
  onSelectPath,
}: {
  view: "rendered" | "markdown"
  proposedPaths: string[]
  onSelectPath: (path: string) => void
  changesetID: string
  file: WikiChangesetFile
}) {
  const query = useQuery({
    queryKey: ["knowledge-base", changesetID, file.path],
    queryFn: () => getChangesetBase(changesetID, file.path),
    staleTime: Infinity,
  })
  if (query.isPending) return <Loading label="Loading base revision" />
  if (query.isError)
    return (
      <div className="error-state" role="alert">
        Cannot load the original file. {errorMessage(query.error)}{" "}
        <button
          className="secondary-button"
          onClick={() => void query.refetch()}
          type="button"
        >
          Retry
        </button>
      </div>
    )
  return (
    <div className="knowledge-file-comparison">
      <section>
        <header>
          <span>Before</span>
          <small>{query.data.exists ? "Base revision" : "New file"}</small>
        </header>
        {query.data.exists ? (
          view === "markdown" ? (
            <pre>{query.data.content || "(empty file)"}</pre>
          ) : (
            <div className="knowledge-rendered-file">
              <WikiMarkdown
                content={query.data.content || "*Empty file*"}
                path={file.path}
              />
            </div>
          )
        ) : (
          <div className="knowledge-file-absent">
            This file does not exist in the base revision.
          </div>
        )}
      </section>
      <section
        className={file.operation === "delete" ? "is-deleted" : "is-proposed"}
      >
        <header>
          <span>{file.operation === "delete" ? "Deleted" : "After"}</span>
          <small>Proposed change</small>
        </header>
        {file.operation === "delete" ? (
          <div className="knowledge-file-absent">
            This file will be removed from the wiki.
          </div>
        ) : view === "markdown" ? (
          <pre>{file.content || "(empty file)"}</pre>
        ) : (
          <div className="knowledge-rendered-file">
            <WikiMarkdown
              content={file.content || "*Empty file*"}
              path={file.path}
              proposedPaths={proposedPaths}
              onSelectPath={onSelectPath}
            />
          </div>
        )}
      </section>
    </div>
  )
}

function Loading({ label }: { label: string }) {
  return (
    <div className="loading-state">
      <LoaderCircle className="spin" size={18} /> {label}
    </div>
  )
}
