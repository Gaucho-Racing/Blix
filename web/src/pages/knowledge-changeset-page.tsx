import { useQuery } from "@tanstack/react-query"
import { ArrowLeft, LoaderCircle } from "lucide-react"
import { Link, useParams } from "react-router-dom"
import { ChangesetReview } from "@/components/changeset-review"
import { useAuth } from "@/lib/auth"
import { getChangeset } from "@/lib/knowledge"
import { errorMessage } from "@/lib/runtime"

export function KnowledgeProposalPage() {
  return <ChangesetPage admin={false} />
}

export function KnowledgeReviewDetailsPage() {
  return <ChangesetPage admin />
}

function ChangesetPage({ admin }: { admin: boolean }) {
  const { changesetID = "" } = useParams()
  const auth = useAuth()
  const allowed =
    auth.isAuthenticated &&
    (auth.isAdmin || !!auth.user?.groups.includes("BlixUsers"))
  const query = useQuery({
    queryKey: ["knowledge-changeset", changesetID],
    queryFn: ({ signal }) => getChangeset(changesetID, signal),
    enabled: allowed,
    refetchInterval: (query) =>
      query.state.data &&
      (!["committed", "rejected"].includes(query.state.data.changeset.status) ||
        query.state.data.documents?.some(
          (document) => document.review_status === "in_review",
        ))
        ? 5000
        : false,
  })
  return (
    <div className="page knowledge-reviews-page">
      <Link
        className="knowledge-review-back"
        to={admin ? "/admin/knowledge" : "/knowledge"}
      >
        <ArrowLeft size={15} /> {admin ? "Wiki changes" : "Team knowledge"}
      </Link>
      {auth.isLoading || (allowed && query.isPending) ? (
        <div className="loading-state">
          <LoaderCircle className="spin" size={18} /> Loading changeset…
        </div>
      ) : !allowed ? (
        <div className="panel error-state" role="alert">
          {auth.isAuthenticated ? (
            "Blix access is required to review knowledge changes."
          ) : (
            <Link className="secondary-button" to="/auth/login">
              Sign in to review this changeset
            </Link>
          )}
        </div>
      ) : query.isError ? (
        <div className="panel error-state" role="alert">
          {errorMessage(query.error)}{" "}
          <button
            className="secondary-button"
            onClick={() => void query.refetch()}
            type="button"
          >
            Retry
          </button>
        </div>
      ) : (
        query.data && (
          <ChangesetReview
            admin={admin}
            changeset={query.data.changeset}
            files={query.data.files}
            documents={query.data.documents ?? []}
            citations={query.data.citations ?? {}}
            key={changesetID}
            onRefresh={() => void query.refetch()}
            refreshing={query.isFetching}
          />
        )
      )}
    </div>
  )
}
