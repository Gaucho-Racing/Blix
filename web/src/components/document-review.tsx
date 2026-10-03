import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Check, X } from "lucide-react"
import { useState } from "react"
import { ReviewConfirmationDialog } from "@/components/review-confirmation-dialog"
import { StatusBadge } from "@/components/status-badge"
import { IdentityDisplay } from "@/components/identity-display"
import { useAuth } from "@/lib/auth"
import {
  documentReadyForReview,
  documentProcessing,
  documentReviewLabel,
  reviewDocument,
  type DocumentDetails,
} from "@/lib/documents"
import { formatTimestamp } from "@/lib/format"
import { errorMessage } from "@/lib/runtime"

export function DocumentReview({ details }: { details: DocumentDetails }) {
  const { document, changesets } = details
  const auth = useAuth()
  const cache = useQueryClient()
  const [decision, setDecision] = useState<"approve" | "reject" | null>(null)
  const [note, setNote] = useState("")
  const member =
    auth.isAuthenticated &&
    (auth.isAdmin || !!auth.user?.groups.includes("BlixUsers"))
  const canReject =
    member && document.review_status === "in_review" && !details.review_blocked
  const canApprove =
    member &&
    auth.isAdmin &&
    document.review_status === "in_review" &&
    documentReadyForReview(document)
  const openChangesets = changesets.filter(
    (changeset) => !["committed", "rejected"].includes(changeset.status),
  )
  const review = useMutation({
    mutationFn: ({
      action,
      reviewNote,
    }: {
      action: "approve" | "reject"
      reviewNote: string
    }) => reviewDocument(document.document_id, action, reviewNote),
    onSuccess: (data) => {
      cache.setQueryData(["document", document.document_id], data)
      setDecision(null)
      setNote("")
    },
    onSettled: async () => {
      await Promise.all([
        cache.invalidateQueries({ queryKey: ["document"] }),
        cache.invalidateQueries({ queryKey: ["documents"] }),
        cache.invalidateQueries({ queryKey: ["knowledge-changeset"] }),
        cache.invalidateQueries({ queryKey: ["knowledge-changesets"] }),
      ])
    },
  })
  const choose = (action: "approve" | "reject") => {
    review.reset()
    setNote("")
    setDecision(action)
  }

  if (
    document.review_status !== "rejected" &&
    (document.review_status !== "in_review" ||
      !documentReadyForReview(document))
  )
    return null

  return (
    <section className="panel document-review-panel">
      <header>
        <div>
          <span className="workspace-kicker">DOCUMENT STATUS</span>
          <h2>{documentReviewLabel(document.review_status)}</h2>
        </div>
        <StatusBadge status={document.review_status} />
      </header>
      <p>
        {document.review_status === "rejected"
          ? "This document is excluded from team knowledge. Its open changesets were rejected, and its original PDF and source evidence are retained for the review record."
          : documentProcessing(document.status)
            ? "Document details are ready for review. A BlixAdmin can approve it while visual extraction and search indexing continue in the background."
            : "A BlixAdmin can accept this document directly or by approving a linked changeset. Rejecting the document also rejects all of its open changesets."}
      </p>
      {document.reviewed_by && (
        <div className="document-review-audit">
          <strong>
            Rejected
            {document.reviewed_at &&
              ` · ${formatTimestamp(document.reviewed_at)}`}
          </strong>
          <IdentityDisplay entityID={document.reviewed_by} />
          {document.review_note && <p>{document.review_note}</p>}
        </div>
      )}
      {document.review_status === "in_review" && details.review_blocked && (
        <p role="status">
          A linked changeset has entered publication. Resolve its outcome before
          rejecting this document.
        </p>
      )}
      {(canApprove || canReject) && (
        <div className="document-review-buttons">
          {canReject && (
            <button
              className="secondary-button knowledge-reject-button"
              type="button"
              disabled={review.isPending}
              onClick={() => choose("reject")}
            >
              <X size={15} /> Reject document
            </button>
          )}
          {canApprove && (
            <button
              className="primary-button"
              type="button"
              disabled={review.isPending}
              onClick={() => choose("approve")}
            >
              <Check size={15} /> Approve document
            </button>
          )}
        </div>
      )}
      {decision && (
        <ReviewConfirmationDialog
          kicker="DOCUMENT STATUS"
          title={
            decision === "reject"
              ? "Reject this document?"
              : "Accept this document?"
          }
          confirmLabel={
            decision === "reject" ? "Reject document" : "Approve document"
          }
          reject={decision === "reject"}
          description={
            <>
              <p>
                <strong>{document.title}</strong>
                {decision === "reject"
                  ? " will be excluded from team knowledge."
                  : " will be marked Ingested and accepted as team knowledge."}
              </p>
              <p>
                {decision === "reject"
                  ? `All ${openChangesets.length} open ${openChangesets.length === 1 ? "changeset" : "changesets"} linked to this document will also be rejected. Further processing will stop.`
                  : "This does not approve or publish any wiki changesets. Review those separately when you are ready."}
              </p>
              {decision === "approve" && documentProcessing(document.status) && (
                <p>Remaining processing will continue in the background. Completed search indexes will become available to Blix.</p>
              )}
              {decision === "reject" && (
                <p>
                  The original PDF and source evidence will be retained for the
                  review record.
                </p>
              )}
              <p>
                Your identity, review note, and decision time will be recorded.
              </p>
            </>
          }
          note={note}
          setNote={setNote}
          pending={review.isPending}
          allowed={decision === "approve" ? canApprove : canReject}
          error={review.isError ? errorMessage(review.error) : null}
          onClose={() => {
            if (!review.isPending) setDecision(null)
          }}
          onSubmit={() => review.mutate({ action: decision, reviewNote: note })}
        />
      )}
    </section>
  )
}
